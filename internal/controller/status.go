package controller

import (
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// Status is a memory copy; it reads the store (never the provider) to
// fill in document-sourced fields like GPUName and SpendInstanceUSD.
func (c *Controller) Status() Snapshot {
	c.mu.Lock()
	state, gen, inFlight, failing, since, unavailable := c.state, c.generation, c.inFlight, c.failing, c.since, c.unavailable
	c.mu.Unlock()

	snap := Snapshot{
		State: state, Public: PublicState(state, failing), Generation: gen,
		Since: since, InFlight: inFlight,
		InstallationID: c.deps.InstallationID,
		LabelPrefix:    "vkllm:" + c.deps.InstallationID + ":",
		Provider:       "vastai",
		Unavailable:    unavailable,
	}
	snap.BudgetCeilingUSD = c.deps.Budget.CeilingUSD()
	c.progressMu.Lock()
	if state == Loading && c.progressGen == gen {
		snap.DownloadedBytes = c.progress.Bytes
		snap.WorkerStages = append([]string(nil), c.progress.Stages...)
	}
	c.progressMu.Unlock()

	doc, _, err := c.deps.Store.Load(c.effCtx())
	if err != nil {
		snap.LastError = err.Error()
		return snap
	}
	snap.Desired = doc.Desired
	if doc.Cache != nil {
		snap.CacheVolumes = append([]store.CacheVolume(nil), doc.Cache.Volumes...)
	}
	snap.StorageMTDUSD = doc.Ledger.StorageTotalUSD
	for _, e := range doc.Ledger.Entries {
		if e.Volume != "" && e.End == nil {
			snap.StorageMTDUSD += e.EstUSD
		}
	}
	snap.CacheHit = doc.Instance != nil && doc.Instance.Cache == "hit"
	snap.Rentals = append([]store.Rental(nil), doc.Rentals...)
	snap.AttemptsThisHour = doc.AttemptsThisHour.Count
	snap.SpendMTDUSD = c.deps.Budget.EstimatedMTD(doc)
	snap.BudgetMonthUSD = c.deps.Budget.CapUSD(doc)
	c.balanceMu.Lock()
	if c.lastBalanceKnown {
		usd := c.lastBalance.USD()
		snap.ProviderBalanceUSD = &usd
	}
	c.balanceMu.Unlock()
	snap.NextCreateAt = nextCreateAllowedAt(doc.Ops)
	recordDocGauges(doc, state, inFlight, c.deps.Clock.Now())

	if doc.Instance == nil {
		if snap.LastError == "" {
			snap.LastError = c.getLastError()
		}
		return snap
	}
	snap.GPUName = doc.Instance.Offer.GPU
	snap.Geolocation = doc.Instance.Offer.Geo
	snap.MachineID = doc.Instance.Offer.MachineID
	if doc.Instance.Offer.Estimate != nil {
		snap.ModelBytes = doc.Instance.Offer.Estimate.ModelBytes
	}
	snap.Hourly = provider.FromUSD(doc.Instance.Offer.DPH)
	if id, ok := providerIDOf(doc); ok {
		snap.ProviderID = id
	}
	snap.CreatedAt = doc.Instance.CreatedAt
	snap.ReadyAt = doc.Instance.ReadyAt
	snap.LastInferenceFinishedAt = doc.Instance.LastInferenceFinishedAt
	snap.IdleDeadline = doc.Instance.IdleDeadline
	snap.LastError = doc.Instance.LastError
	snap.Ready = snap.Public == "READY"
	if !doc.Instance.CreatedAt.IsZero() {
		snap.Uptime = c.deps.Clock.Now().Sub(doc.Instance.CreatedAt)
	}
	if doc.Instance.IdleDeadline != nil {
		snap.UntilIdleShutdown = doc.Instance.IdleDeadline.Sub(c.deps.Clock.Now())
	}
	snap.Prices, snap.Estimate, snap.InetDownMbps = doc.Instance.Offer.Prices, doc.Instance.Offer.Estimate, doc.Instance.Offer.InetDown
	if e, ok := openEntry(doc); ok {
		snap.SpendInstanceUSD = e.EstUSD
		snap.SpendGPUUSD, snap.SpendStorageUSD, snap.SpendDownloadUSD = splitSpend(e, snap.Prices)
	}
	if snap.LastError == "" {
		snap.LastError = c.getLastError()
	}

	c.stagesMu.Lock()
	snap.Stages = make(map[string]time.Duration, len(c.stages))
	for k, v := range c.stages {
		snap.Stages[k] = v
	}
	c.stagesMu.Unlock()
	return snap
}

// splitSpend divides an entry's time cost between GPU and storage in the
// ratio of their hourly prices; the fixed part is the download.
func splitSpend(e store.LedgerEntry, p *store.OfferPrices) (gpu, storage, download float64) {
	timeUSD := e.EstUSD - e.FixedUSD
	gpu = timeUSD
	if p != nil && e.DPH > 0 && p.GPUUSDPerHour < e.DPH {
		gpu = timeUSD * p.GPUUSDPerHour / e.DPH
	}
	return gpu, timeUSD - gpu, e.FixedUSD
}

// openEntry returns the current generation's still-open ledger entry,
// if any; Status and the per-instance spend gate both read a
// document's running spend through this one lookup.
func openEntry(doc store.Document) (store.LedgerEntry, bool) {
	for _, e := range doc.Ledger.Entries {
		if e.Gen == doc.Generation && e.Volume == "" && e.End == nil {
			return e, true
		}
	}
	return store.LedgerEntry{}, false
}

// setLastError/getLastError hold the controller-level last error, the
// Status fallback for a failure with no document field left to live in.
func (c *Controller) setLastError(msg string) {
	c.lastErrorMu.Lock()
	c.lastError = msg
	c.lastErrorMu.Unlock()
}

func (c *Controller) getLastError() string {
	c.lastErrorMu.Lock()
	defer c.lastErrorMu.Unlock()
	return c.lastError
}
