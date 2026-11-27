package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/offercost"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

var (
	errOfferOutsideFilter = errors.New("controller: offer above a price cap or below the disk size")
	errDiskBelowModel     = errors.New("controller: disk below the model's need")
)

// dispatchSearch is saga step 1: size the model, size the disk, search,
// then rank by estimated session cost.
func dispatchSearch(ctx context.Context, c *Controller, e effSearch) {
	started := c.deps.Clock.Now()
	cfg := c.deps.Config
	bytes, err := c.deps.ModelSize.Bytes(ctx, c.deps.ModelURL)
	if err != nil {
		c.warn("controller: offer search skipped", "reason", "model_size_unknown", "err", err)
		c.postEvent(Event{Kind: EvNoOffer, Err: err, Reason: "provider_error"})
		return
	}
	disk := offercost.DiskGB(bytes, cfg.ProviderFilter.MinDiskGB)
	if disk > cfg.MaxDiskGB {
		c.warn("controller: offer search skipped", "reason", "model_too_large", "diskGB", disk, "maxDiskGB", cfg.MaxDiskGB)
		c.postEvent(Event{Kind: EvNoOffer, Reason: "no_offer"})
		return
	}
	f := e.filter
	f.MinDiskGB = disk
	offers, err := c.deps.Provider.SearchOffers(ctx, f)
	if err != nil {
		c.postEvent(Event{Kind: EvNoOffer, Err: err, Reason: "provider_error"})
		return
	}
	ranked := offercost.Rank(offers, offercost.Inputs{ModelBytes: bytes, Horizon: horizon(cfg), DiskGB: disk})
	if cfg.MaxInstanceSpendUSD > 0 {
		ranked = slices.DeleteFunc(ranked, func(r offercost.Ranked) bool {
			return r.Breakdown.DownloadUSD.USD() >= cfg.MaxInstanceSpendUSD
		})
	}
	if len(ranked) == 0 {
		c.postEvent(Event{Kind: EvNoOffer, Reason: "no_offer"})
		return
	}
	choice, plan := c.cacheChoice(ctx, ranked, bytes, disk)
	c.postEvent(Event{Kind: EvOfferChosen, Offer: choice.Offer, Breakdown: choice.Breakdown, Filter: f, SearchStarted: started, CachePlan: plan})
}

func horizon(cfg config.Controller) time.Duration {
	if cfg.IdleShutdownEnabled {
		return cfg.IdleShutdownTimeout
	}
	return cfg.MaxLifetime
}

func (c *Controller) warn(msg string, args ...any) {
	if c.deps.Log != nil {
		c.deps.Log.Warn(msg, args...)
	}
}

// Price and filter are asserted before the provider call so an over-cap offer never reaches it.
func dispatchCreate(ctx context.Context, c *Controller, e effCreate) {
	if err := c.deps.Budget.AssertPrice(e.offer.Hourly.USD(), c.deps.Config.MaxHourlyPriceUSD); err != nil {
		c.postEvent(Event{Kind: EvCreateFailed, Gen: e.gen, Err: err, Class: provider.Fatal})
		return
	}
	if !provider.Admit(e.offer, e.filter) {
		c.postEvent(Event{Kind: EvCreateFailed, Gen: e.gen, Err: errOfferOutsideFilter, Class: provider.Fatal})
		return
	}
	if e.breakdown.DiskGB <= 0 || e.breakdown.DiskGB < e.filter.MinDiskGB {
		c.postEvent(Event{Kind: EvCreateFailed, Gen: e.gen, Err: errDiskBelowModel, Class: provider.Fatal})
		return
	}
	seed, err := c.deps.WorkerSeed()
	if err != nil {
		c.postEvent(Event{Kind: EvCreateFailed, Gen: e.gen, Err: err, Class: provider.Fatal})
		return
	}
	key := c.deps.Worker.WorkerKey(seed, c.deps.InstallationID, e.gen)
	req := c.deps.Worker.BuildCreate(e.offer, e.breakdown.DiskGB, e.label, key)
	req.Volume = c.volumeAttachFor(ctx, e.gen)
	id, err := c.deps.Provider.CreateInstance(ctx, req)
	dropped := false
	if err != nil && req.Volume != nil && refusedRequest(err) && !errors.Is(err, provider.ErrDuplicateLabel) {
		// A refused volume must not fail the start, and a retry with it
		// would pick the same volume again.
		req.Volume, dropped = nil, true
		id, err = c.deps.Provider.CreateInstance(ctx, req)
	}
	if err != nil {
		c.postEvent(Event{Kind: EvCreateFailed, Gen: e.gen, Err: err, Class: createClass(err), Duplicate: errors.Is(err, provider.ErrDuplicateLabel)})
		return
	}
	c.postEvent(Event{Kind: EvCreated, Gen: e.gen, ProviderID: id, VolumeDropped: dropped})
}

func createClass(err error) provider.Class {
	var perr *provider.Error
	if errors.As(err, &perr) {
		return perr.Class
	}
	return provider.Retryable
}

// A failed read posts nothing, so the next poll tick retries instead of this goroutine retrying blindly.
func dispatchGet(ctx context.Context, c *Controller, e effGet) {
	inst, err := c.deps.Provider.GetInstance(ctx, e.id)
	if err != nil {
		return
	}
	if !c.confirmPollStatus(e.gen, inst.Status.Terminal()) {
		return
	}
	c.postEvent(Event{
		Kind: EvProviderObserved, Gen: e.gen,
		Status:  inst.Status,
		SSHHost: inst.SSHHost, SSHPort: inst.SSHPort,
	})
}

// confirmPollStatus holds back a terminal status until two consecutive gets
// for the same generation report one; a destroy is never one observation.
func (c *Controller) confirmPollStatus(gen int64, terminal bool) bool {
	c.pollTerminalMu.Lock()
	defer c.pollTerminalMu.Unlock()
	if gen != c.pollTerminalGen || !terminal {
		c.pollTerminalGen, c.pollTerminalSeen = gen, 0
	}
	if !terminal {
		return true
	}
	c.pollTerminalSeen++
	if c.pollTerminalSeen < 2 {
		return false
	}
	c.pollTerminalSeen = 0
	return true
}

// Dials the address the caller just observed, never one read back from
// the document. A repeat poll can start a second dial while the first
// is still in flight; only the one that lands first wins, so a late or duplicate success is closed instead of overwriting the live Conn.
func dispatchDial(ctx context.Context, c *Controller, e effDial) {
	conn, err := c.deps.Dialer.Dial(ctx, e.inst)
	if err != nil {
		if errors.Is(err, tunnel.ErrHostKeyMismatch) {
			c.postEvent(Event{Kind: EvWorkerFailed, Gen: e.gen, Err: err})
		}
		return
	}
	c.mu.Lock()
	stillBooting := e.gen == c.generation && c.state == Booting
	duplicate := stillBooting && c.conn != nil
	installed := c.conn
	if stillBooting && c.conn == nil {
		c.conn, installed = conn, conn
	}
	c.mu.Unlock()
	if !stillBooting {
		_ = conn.Close()
		return
	}
	if duplicate {
		// A second dial lands after an earlier one already installed a
		// Conn: close this one, but still re-post EvLinkUp against the
		// installed Conn so a dropped first EvLinkUp self-heals here
		// instead of stalling until StartupTimeout.
		_ = conn.Close()
	}
	c.postEvent(Event{Kind: EvLinkUp, Gen: e.gen, HostKey: installed.HostKey()})
}

// dispatchProbe is saga step 6. Unreachable/Loading posts nothing — the
// caller's own timer (poll while Loading, suspect while Ready/Serving)
// decides what a non-Ready result means; this function only reports success.
func dispatchProbe(ctx context.Context, c *Controller, e effProbe) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return
	}
	seed, err := c.deps.WorkerSeed()
	if err != nil {
		return
	}
	key := c.deps.Worker.WorkerKey(seed, c.deps.InstallationID, e.gen)
	status, err := c.deps.Worker.Probe(ctx, conn.RoundTripper(), key, c.deps.ModelAlias)
	if err != nil || status != ProbeReady {
		return
	}
	c.postEvent(Event{Kind: EvHealthOK, Gen: e.gen})
}

// nextCreateAllowedAt is saga step 7's backoff: n trailing create-* ops
// since the last one that reached "ready"; the result is the zero time
// when there is nothing to back off from.
func nextCreateAllowedAt(ops []store.Op) time.Time {
	n := 0
	var lastFinish time.Time
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		if !strings.HasPrefix(op.Key, "create-") {
			continue
		}
		if op.Outcome == "ready" {
			break
		}
		n++
		if op.FinishedAt != nil && op.FinishedAt.After(lastFinish) {
			lastFinish = *op.FinishedAt
		}
	}
	if n == 0 {
		return time.Time{}
	}
	backoff := time.Minute * time.Duration(int64(1)<<uint(n-1))
	if backoff > 30*time.Minute {
		backoff = 30 * time.Minute
	}
	return lastFinish.Add(backoff)
}

// refusedRequest is a definitive 4xx: the provider created nothing, so a
// second create without the volume cannot make a second instance.
func refusedRequest(err error) bool {
	var perr *provider.Error
	return errors.As(err, &perr) && perr.Status >= 400 && perr.Status < 500 && perr.Status != 429
}
