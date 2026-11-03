// internal/controller/saga.go
package controller

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

// dispatchSearch is saga step 1.
func dispatchSearch(ctx context.Context, c *Controller, e effSearch) {
	offers, err := c.deps.Provider.SearchOffers(ctx, e.filter)
	if err != nil {
		c.postEvent(Event{Kind: EvNoOffer, Reason: "provider_error"})
		return
	}
	if len(offers) == 0 {
		c.postEvent(Event{Kind: EvNoOffer, Reason: "no_offer"})
		return
	}
	c.postEvent(Event{Kind: EvOfferChosen, Offer: offers[0]})
}

// dispatchCreate is saga step 2 (price assertion before the call) and
// step 3 (the call itself); the op and label were already committed by
// T5's mutate before this ever runs (C-2).
func dispatchCreate(ctx context.Context, c *Controller, e effCreate) {
	if err := c.deps.Budget.AssertPrice(e.offer.Hourly.USD(), c.deps.Config.MaxHourlyPriceUSD); err != nil {
		c.postEvent(Event{Kind: EvCreateFailed, Gen: e.gen, Err: err, Class: provider.Fatal})
		return
	}
	seed, err := c.deps.WorkerSeed()
	if err != nil {
		c.postEvent(Event{Kind: EvCreateFailed, Gen: e.gen, Err: err, Class: provider.Fatal})
		return
	}
	key := c.deps.Worker.WorkerKey(seed, c.deps.InstallationID, e.gen)
	req := c.deps.Worker.BuildCreate(e.offer, e.label, key)
	id, err := c.deps.Provider.CreateInstance(ctx, req)
	if err != nil {
		class := provider.Retryable
		var perr *provider.Error
		if errors.As(err, &perr) {
			class = perr.Class
		}
		c.postEvent(Event{Kind: EvCreateFailed, Gen: e.gen, Err: err, Class: class, Duplicate: errors.Is(err, provider.ErrDuplicateLabel)})
		return
	}
	c.postEvent(Event{Kind: EvCreated, Gen: e.gen, ProviderID: id})
}

// dispatchGet is saga step 5's boot-polling GetInstance call. A failed
// read posts nothing (C-9); the next poll tick retries.
func dispatchGet(ctx context.Context, c *Controller, e effGet) {
	inst, err := c.deps.Provider.GetInstance(ctx, e.id)
	if err != nil {
		return
	}
	c.postEvent(Event{
		Kind: EvProviderObserved, Gen: e.gen,
		Status: inst.Status, RawStatus: inst.RawStatus,
		SSHHost: inst.SSHHost, SSHPort: inst.SSHPort,
	})
}

// dispatchDial is saga step 5's link step: dial using only the address
// from the GetInstance result the caller already observed (NFR-8.4),
// never from the document.
func dispatchDial(ctx context.Context, c *Controller, e effDial) {
	conn, err := c.deps.Dialer.Dial(ctx, e.inst)
	if err != nil {
		if errors.Is(err, tunnel.ErrHostKeyMismatch) {
			c.postEvent(Event{Kind: EvWorkerFailed, Gen: e.gen, Err: err})
		}
		return
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	c.postEvent(Event{Kind: EvLinkUp, Gen: e.gen, HostKey: conn.HostKey()})
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

// buildSearchFilter is saga step 1's filter: the configured shape plus
// the live, unexpired denylist (expired entries are pruned elsewhere,
// in the reconcile tick's own CAS — this function just skips them).
func buildSearchFilter(f config.ProviderFilter, maxHourlyUSD float64, denylist []store.DenyEntry, now time.Time) provider.OfferFilter {
	excl := make([]int64, 0, len(denylist))
	for _, d := range denylist {
		if d.Until.After(now) {
			excl = append(excl, d.MachineID)
		}
	}
	return provider.OfferFilter{
		GPUName: f.GPUName, NumGPUs: f.NumGPUs, MinCPUCores: f.MinCPUCores,
		MinCPURAMGB: f.MinCPURAMGB, MinDiskGB: f.MinDiskGB, MinReliability: f.MinReliability,
		MinInetDownMBps: f.MinInetDownMBps, MinDiskBwMBps: f.MinDiskBwMBps, MinCUDA: f.MinCUDA,
		Geolocation: f.Geolocation, MaxHourly: provider.Money(maxHourlyUSD * 1_000_000),
		ExcludeMachineIDs: excl,
	}
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
