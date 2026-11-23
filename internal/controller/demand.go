package controller

import (
	"context"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

const (
	originExplicit = "explicit"
	originDemand   = "demand"
)

func originOf(ev Event) string {
	if ev.Origin == originDemand {
		return originDemand
	}
	return originExplicit
}

// Demand starts the GPU through the same gates as Start and keeps the
// start alive until until; it never moves the idle deadline.
func (c *Controller) Demand(ctx context.Context, keyID string, until time.Time) (Snapshot, error) {
	if c.deps.Config.ProvisioningMode != "on-demand" {
		return c.Status(), ErrBackendUnavailable
	}
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	switch {
	case inService(state):
		return c.Status(), nil
	case state == Draining || state == Destroying:
		return c.Status(), ErrDraining
	}
	c.demandMu.Lock()
	c.demandKeyID = keyID
	c.demandMu.Unlock()
	c.extendDemand(until)
	return c.callerEvent(ctx, Event{Kind: EvStart, Origin: originDemand, KeyID: keyID})
}

func (c *Controller) extendDemand(until time.Time) {
	c.demandMu.Lock()
	if until.After(c.demandUntil) {
		c.demandUntil = until
	}
	at := c.demandUntil
	c.demandMu.Unlock()
	c.armTimer(timerDemand, at)
}

// cancelUnattendedStart stops a demand-origin start that nobody waits
// for any more; processEvent re-checks the origin under procMu.
func (c *Controller) cancelUnattendedStart(ctx context.Context) {
	now := c.deps.Clock.Now()
	c.demandMu.Lock()
	until := c.demandUntil
	c.demandMu.Unlock()
	if now.Before(until) {
		c.armTimer(timerDemand, until)
		return
	}
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	if inService(state) {
		return
	}
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil {
		c.armTimer(timerDemand, now.Add(requeueDelay))
		return
	}
	if doc.StartOrigin != originDemand || doc.Desired != store.DesiredRunning {
		return
	}
	// A failure teardown keeps desired RUNNING so the backoff retries; wait it
	// out and cancel that retry instead of letting it provision unattended.
	if state == Draining || state == Destroying {
		c.armTimer(timerDemand, now.Add(requeueDelay))
		return
	}
	// No other path resends this stop, so even a stale CAS retries.
	if err := c.processEvent(ctx, Event{Kind: EvStop, Gen: c.currentGen(), Origin: originDemand}); err != nil {
		c.armTimer(timerDemand, now.Add(requeueDelay))
	}
}

// rearmDemandAfterRecovery gives a demand start that survived a restart
// one grace period, because the restart dropped every held request.
func (c *Controller) rearmDemandAfterRecovery(ctx context.Context) {
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil || doc.StartOrigin != originDemand || doc.Desired != store.DesiredRunning {
		return
	}
	c.extendDemand(c.deps.Clock.Now().Add(c.deps.Config.DemandGrace))
}
