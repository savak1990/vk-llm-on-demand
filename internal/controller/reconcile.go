package controller

import (
	"context"
	"errors"
	"slices"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// dispatchReconcile runs one pass off the loop goroutine; an overlapping
// pass is skipped so one moment is never counted as two observations.
func (c *Controller) dispatchReconcile(ctx context.Context) {
	if !c.reconciling.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer c.reconciling.Store(false)
		c.reconcileOnce(ctx)
	}()
}

// reconcileOnce loads the document before listing, so any instance newer
// than the snapshot shows up with a generation above it and is never destroyed.
func (c *Controller) reconcileOnce(ctx context.Context) {
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil {
		return
	}
	instances, listErr := c.deps.Provider.ListInstances(ctx, "vkllm:"+c.deps.InstallationID+":")
	b, balErr := c.deps.Provider.Balance(ctx)

	c.balanceMu.Lock()
	if balErr == nil {
		c.lastBalance, c.lastBalanceKnown = b, true
	}
	balance, known := c.lastBalance, c.lastBalanceKnown
	c.balanceMu.Unlock()

	c.reconcilePruneAndBudget(ctx, balance, known)
	if listErr == nil {
		c.reconcileCompareInstances(ctx, doc, instances)
	}
}

// reconcilePruneAndBudget is the tick's document-only half. The Tick it
// raises is the only periodic trigger for the lifetime and drain-complete rules.
func (c *Controller) reconcilePruneAndBudget(ctx context.Context, balance provider.Money, known bool) {
	now := c.deps.Clock.Now()
	_, _ = store.Transition(ctx, c.deps.Store, c.flushAndRun(func(d *store.Document) error {
		d.MachineDenylist = slices.DeleteFunc(d.MachineDenylist, func(e store.DenyEntry) bool { return !e.Until.After(now) })
		c.deps.Budget.Rollover(d, now)
		c.deps.Budget.RefreshOpenEntry(d, now)
		return nil
	}))
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil {
		return
	}
	c.postEvent(Event{Kind: EvTick, Gen: doc.Generation})
	if doc.Instance == nil {
		return
	}
	if reason := c.deps.Budget.Allow(doc, balance, known); reason != "" {
		c.postEvent(Event{Kind: EvBudgetExhausted, Gen: doc.Generation, Reason: reason})
	}
}

// reconcileCompareInstances is the list-versus-document half. Every event it
// posts carries doc.Generation, since T8, T14, T23 and T27 all fence on it.
func (c *Controller) reconcileCompareInstances(ctx context.Context, doc store.Document, instances []provider.Instance) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()

	activeID, hasID := providerIDOf(doc)
	var active, adopt *provider.Instance
	var ahead int64
	seen := map[provider.InstanceID]bool{}
	for i := range instances {
		inst := &instances[i]
		seen[inst.ID] = true
		if doc.Instance != nil {
			if hasID && inst.ID == activeID {
				active = inst
				continue
			}
			// An id-less instance is a create whose outcome never came back; adopt the lowest id.
			if !hasID && inst.Label == doc.Instance.Label {
				if adopt == nil || inst.ID < adopt.ID {
					adopt = inst
				}
				continue
			}
		}
		id, gen, ok := parseLabel(inst.Label)
		if !ok || id != c.deps.InstallationID {
			continue
		}
		if gen <= doc.Generation && createOpOpen(doc, gen) {
			continue
		}
		c.orphanObserved[inst.ID]++
		if gen > doc.Generation {
			ahead = max(ahead, gen)
			continue
		}
		if c.orphanObserved[inst.ID] >= 2 && mayDestroy(inst.Label, c.deps.InstallationID, doc.Generation) {
			go destroyConfirmed(ctx, c, inst.ID)
		}
	}
	for id := range c.orphanObserved {
		if !seen[id] {
			delete(c.orphanObserved, id)
		}
	}
	if ahead > 0 {
		c.advanceFence(ctx, ahead)
	}

	if doc.Instance == nil {
		c.activeLabel, c.activeObserved = "", 0
		return
	}
	if doc.Instance.Label != c.activeLabel {
		c.activeLabel, c.activeObserved = doc.Instance.Label, 0
	}
	switch {
	case adopt != nil:
		c.activeObserved = 0
		c.postEvent(Event{Kind: EvCreated, Gen: doc.Generation, ProviderID: adopt.ID})
	case active != nil:
		c.activeObserved = 0
		if active.Status.Terminal() {
			c.postEvent(Event{Kind: EvProviderObserved, Gen: doc.Generation, Status: active.Status, RawStatus: active.RawStatus})
		}
	default:
		c.activeObserved++
		if c.activeObserved >= 2 {
			c.activeObserved = 0
			c.postEvent(Event{Kind: EvVanished, Gen: doc.Generation})
		}
	}
}

// advanceFence raises the generation so a label from a lost or reset document
// becomes destroyable. Moving it under a live instance would strand that
// instance's in-flight events and inFlight count, so it waits until none is active.
func (c *Controller) advanceFence(ctx context.Context, gen int64) {
	// procMu keeps this write out of processEvent's Load-to-Transition window.
	c.procMu.Lock()
	defer c.procMu.Unlock()
	_, err := store.Transition(ctx, c.deps.Store, func(d *store.Document) error {
		if d.Instance != nil {
			return errStale
		}
		d.Generation = max(d.Generation, gen)
		return nil
	})
	if err != nil && !errors.Is(err, errStale) && c.deps.Log != nil {
		c.deps.Log.Warn("controller: fence advance failed", "err", err)
	}
}
