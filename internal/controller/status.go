package controller

import (
	"math"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

// Status is a memory copy; it reads the store (never the provider) to
// fill in document-sourced fields like GPUName and SpendInstanceUSD.
func (c *Controller) Status() Snapshot {
	c.mu.Lock()
	state, gen, inFlight, failing, since := c.state, c.generation, c.inFlight, c.failing, c.since
	c.mu.Unlock()

	snap := Snapshot{
		State: state, Public: PublicState(state, failing), Generation: gen,
		Since: since, InFlight: inFlight,
		InstallationID: c.deps.InstallationID,
		LabelPrefix:    "vkllm:" + c.deps.InstallationID + ":",
		Provider:       "vastai",
	}

	doc, _, err := c.deps.Store.Load(c.effCtx())
	if err != nil {
		snap.LastError = err.Error()
		return snap
	}
	snap.Desired = doc.Desired
	snap.AttemptsThisHour = doc.AttemptsThisHour.Count
	snap.SpendMTDUSD = c.deps.Budget.EstimatedMTD(doc)
	snap.NextCreateAt = nextCreateAllowedAt(doc.Ops)

	if doc.Instance == nil {
		if snap.LastError == "" {
			snap.LastError = c.getLastError()
		}
		return snap
	}
	snap.GPUName = doc.Instance.Offer.GPU
	snap.MachineID = doc.Instance.Offer.MachineID
	snap.Hourly = provider.Money(math.Round(doc.Instance.Offer.DPH * 1_000_000))
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
	for _, e := range doc.Ledger.Entries {
		if e.Gen == doc.Generation && e.End == nil {
			snap.SpendInstanceUSD = e.EstUSD
			break
		}
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

// setLastError and getLastError hold the controller-level last error —
// transient failures (a failed search, create or worker check) that have
// no document field to live in once the instance they touched is gone.
// doc.Instance.LastError (set at T24) takes priority in Status when both
// exist; this is only the fallback.
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
