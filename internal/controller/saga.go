package controller

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

// dispatchSearch is saga step 1.
func dispatchSearch(ctx context.Context, c *Controller, e effSearch) {
	offers, err := c.deps.Provider.SearchOffers(ctx, e.filter)
	if err != nil {
		c.postEvent(Event{Kind: EvNoOffer, Err: err, Reason: "provider_error"})
		return
	}
	if len(offers) == 0 {
		c.postEvent(Event{Kind: EvNoOffer, Reason: "no_offer"})
		return
	}
	c.postEvent(Event{Kind: EvOfferChosen, Offer: offers[0]})
}

// Price is asserted before the provider call so an over-cap offer never reaches it.
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
// the document, so a stale or edited document can't redirect the
// connection. A repeat poll can start a second dial while the first is
// still in flight; only the dial that lands first while still Booting
// with no Conn installed wins, so a late or duplicate success is closed
// instead of leaking or overwriting the live Conn.
func dispatchDial(ctx context.Context, c *Controller, e effDial) {
	conn, err := c.deps.Dialer.Dial(ctx, e.inst)
	if err != nil {
		if errors.Is(err, tunnel.ErrHostKeyMismatch) {
			c.postEvent(Event{Kind: EvWorkerFailed, Gen: e.gen, Err: err})
		}
		return
	}
	c.mu.Lock()
	live := e.gen == c.generation && c.state == Booting && c.conn == nil
	if live {
		c.conn = conn
	}
	c.mu.Unlock()
	if !live {
		_ = conn.Close()
		return
	}
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
