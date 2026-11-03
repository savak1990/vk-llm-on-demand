package controller

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

const (
	pollInterval        = 5 * time.Second
	metricsPullInterval = 15 * time.Second
	flushDelay          = 10 * time.Second
	requeueDelay        = 5 * time.Second
	callerAttempts      = 8
)

// Same-instant order: deadlines first, so an Admit at the exact idle
// deadline loses to the idle timer.
var timerPriority = []timerKind{
	timerIdle, timerStartup, timerDrain,
	timerDestroyRetry, timerReconcile, timerPoll, timerFlush, timerMetricsPull,
}

func (c *Controller) armTimer(k timerKind, at time.Time) {
	c.timersMu.Lock()
	c.timers[k] = at
	c.timersMu.Unlock()
}

func (c *Controller) clearTimer(k timerKind) {
	c.timersMu.Lock()
	delete(c.timers, k)
	c.timersMu.Unlock()
}

func (c *Controller) dueTimer() (timerKind, bool) {
	now := c.deps.Clock.Now()
	c.timersMu.Lock()
	defer c.timersMu.Unlock()
	for _, k := range timerPriority {
		if due, ok := c.timers[k]; ok && !due.After(now) {
			return k, true
		}
	}
	return 0, false
}

func (c *Controller) nextDue() time.Time {
	c.timersMu.Lock()
	defer c.timersMu.Unlock()
	var next time.Time
	for _, due := range c.timers {
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	return next
}

func (c *Controller) effCtx() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runCtx != nil {
		return c.runCtx
	}
	return context.Background()
}

func holdsConn(s State) bool {
	switch s {
	case Booting, Loading, Ready, Serving, Draining:
		return true
	default:
		return false
	}
}

func inStartup(s State) bool { return s == Creating || s == Booting || s == Loading }

func inService(s State) bool { return s == Ready || s == Serving }

// processEvent is the one path through decide for the loop and for
// Start/Stop alike. errStale is returned, not swallowed: the loop drops
// it (the next tick re-reads), a caller has no next tick and retries.
func (c *Controller) processEvent(ctx context.Context, ev Event) error {
	c.procMu.Lock()
	defer c.procMu.Unlock()

	c.mu.Lock()
	state, inFlight, failing := c.state, c.inFlight, c.failing
	c.mu.Unlock()

	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil {
		return err
	}

	next, mutate, effects := decide(state, ev, Facts{
		Now: c.deps.Clock.Now(), Doc: doc, InFlight: inFlight, Failing: failing,
		Cfg: c.deps.Config, Budget: c.deps.Budget,
	})

	if mutate != nil {
		committed, txErr := store.Transition(ctx, c.deps.Store, c.flushAndRun(mutate))
		switch {
		case errors.Is(txErr, errStale):
			return errStale
		case errors.Is(txErr, store.ErrRetriesExhausted):
			c.requeue(ev)
			return nil
		case errors.Is(txErr, store.ErrStoreReset):
			c.dispatchReconcile(c.effCtx())
			return nil
		case txErr != nil:
			return txErr
		}
		doc = committed
	}

	// Nothing can be admitted before a generation reaches Ready, so a new
	// generation starts with no requests and no suspect probe of its own.
	c.mu.Lock()
	if doc.Generation != c.generation {
		c.inFlight = 0
		c.suspect = false
	}
	c.generation = doc.Generation
	if next != state {
		c.state = next
	}
	c.mu.Unlock()

	if next == Destroying && state != Destroying {
		c.destroyRetryMu.Lock()
		c.destroyRetryN = 0
		c.destroyRetryMu.Unlock()
		c.armTimer(timerDestroyRetry, c.deps.Clock.Now())
	}
	if holdsConn(state) && !holdsConn(next) {
		go dispatchCloseConn(c)
	}
	c.syncTimers(state, next, doc)

	c.queueEffects(effects)
	return nil
}

// syncTimers arms each state-scoped timer on entry and clears it on
// exit. Startup and drain both raise DeadlineExpired, so neither may
// outlive its own state.
func (c *Controller) syncTimers(prev, next State, doc store.Document) {
	now := c.deps.Clock.Now()

	if inStartup(next) && !inStartup(prev) {
		c.armTimer(timerPoll, now.Add(pollInterval))
		if doc.Instance != nil && c.deps.Config.StartupTimeout > 0 {
			c.armTimer(timerStartup, doc.Instance.CreatedAt.Add(c.deps.Config.StartupTimeout))
		}
	} else if !inStartup(next) {
		c.clearTimer(timerStartup)
	}

	if next == Draining && prev != Draining {
		c.armTimer(timerDrain, now.Add(c.deps.Config.ShutdownDrainTimeout))
	} else if next != Draining {
		c.clearTimer(timerDrain)
	}

	if inService(next) {
		if !inService(prev) {
			c.armTimer(timerMetricsPull, now.Add(metricsPullInterval))
		}
		c.armIdle(doc)
	} else {
		c.clearTimer(timerIdle)
		c.clearTimer(timerMetricsPull)
	}
}

func (c *Controller) armIdle(doc store.Document) {
	if doc.Instance != nil && doc.Instance.IdleDeadline != nil {
		c.armTimer(timerIdle, *doc.Instance.IdleDeadline)
	}
}

// requeue retries an event whose CAS exhausted its conflict budget.
func (c *Controller) requeue(ev Event) {
	ctx := c.effCtx()
	go func() {
		select {
		case <-c.deps.Clock.After(requeueDelay):
			c.postEvent(ev)
		case <-ctx.Done():
		}
	}()
}

func (c *Controller) flushAndRun(mutate func(*store.Document) error) func(*store.Document) error {
	return func(d *store.Document) error {
		if at, pending := c.pendingActivity(); pending && d.Instance != nil {
			t := at
			d.Instance.LastInferenceFinishedAt = &t
			if dl := computeIdleDeadline(d.Instance.ReadyAt, &t, c.deps.Config.IdleShutdownTimeout); !dl.IsZero() {
				d.Instance.IdleDeadline = &dl
			}
		}
		return mutate(d)
	}
}

func (c *Controller) flushActivity(ctx context.Context) {
	at, pending := c.pendingActivity()
	if !pending {
		return
	}
	committed, err := store.Transition(ctx, c.deps.Store, c.flushAndRun(func(d *store.Document) error {
		if d.Instance == nil {
			return errStale
		}
		return nil
	}))
	if err != nil && !errors.Is(err, errStale) {
		c.armTimer(timerFlush, c.deps.Clock.Now().Add(flushDelay))
		return
	}
	if !c.clearActivity(at) {
		c.armTimer(timerFlush, c.deps.Clock.Now().Add(flushDelay))
	}
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	if err == nil && inService(state) {
		c.armIdle(committed)
	}
}

func (c *Controller) queueEffects(effects []Effect) {
	ctx := c.effCtx()
	for _, e := range effects {
		go c.runEffect(ctx, e)
	}
}

func (c *Controller) runEffect(ctx context.Context, e Effect) {
	switch v := e.(type) {
	case effSearch:
		dispatchSearch(ctx, c, v)
	case effCreate:
		dispatchCreate(ctx, c, v)
	case effGet:
		dispatchGet(ctx, c, v)
	case effDial:
		dispatchDial(ctx, c, v)
	case effProbe:
		dispatchProbe(ctx, c, v)
	}
}

func dispatchCloseConn(c *Controller) {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *Controller) armDestroyRetry() {
	c.destroyRetryMu.Lock()
	c.destroyRetryN++
	n := c.destroyRetryN
	c.destroyRetryMu.Unlock()
	backoff := 5 * time.Minute
	if n < 5 {
		backoff = min(backoff, 10*time.Second*time.Duration(int64(1)<<uint(n)))
	}
	c.armTimer(timerDestroyRetry, c.deps.Clock.Now().Add(backoff))
}

func providerIDOf(doc store.Document) (provider.InstanceID, bool) {
	if doc.Instance == nil || doc.Instance.ProviderID == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(doc.Instance.ProviderID, 10, 64)
	if err != nil {
		return 0, false
	}
	return provider.InstanceID(id), true
}

func (c *Controller) currentProviderID(ctx context.Context) (provider.InstanceID, bool) {
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil {
		return 0, false
	}
	return providerIDOf(doc)
}

func dispatchDestroy(ctx context.Context, c *Controller, id provider.InstanceID, gen int64) {
	err := c.deps.Provider.DestroyInstance(ctx, id)
	confirmed := errors.Is(err, provider.ErrNotFound)
	if err == nil {
		if _, getErr := c.deps.Provider.GetInstance(ctx, id); errors.Is(getErr, provider.ErrNotFound) {
			confirmed = true
		}
	}
	if confirmed {
		c.postEvent(Event{Kind: EvGone, Gen: gen})
		return
	}
	c.armDestroyRetry()
}

// fireDestroyRetry treats a failed read as a failed round: only the
// provider confirming absence, or a record with no provider id at all,
// may end Destroying.
func (c *Controller) fireDestroyRetry(ctx context.Context) {
	c.mu.Lock()
	gen := c.generation
	c.mu.Unlock()
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil {
		c.armDestroyRetry()
		return
	}
	if id, ok := providerIDOf(doc); ok {
		go dispatchDestroy(c.effCtx(), c, id, gen)
		return
	}
	c.handleEvent(ctx, Event{Kind: EvGone, Gen: gen})
}

func (c *Controller) pollTick(ctx context.Context) {
	c.mu.Lock()
	state, gen := c.state, c.generation
	c.mu.Unlock()
	switch state {
	case Creating, Booting:
		if id, ok := c.currentProviderID(ctx); ok {
			go dispatchGet(c.effCtx(), c, effGet{id: id, gen: gen})
		}
	case Loading:
		go dispatchProbe(c.effCtx(), c, effProbe{gen: gen})
	}
	if inStartup(state) {
		c.armTimer(timerPoll, c.deps.Clock.Now().Add(pollInterval))
	}
}

func (c *Controller) dispatchScrape(ctx context.Context) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return
	}
	go func() {
		if err := c.deps.Worker.Scrape(ctx, conn.RoundTripper()); err != nil && c.deps.Log != nil {
			c.deps.Log.Warn("controller: metrics scrape failed", "err", err)
		}
	}()
}

func (c *Controller) handleEvent(ctx context.Context, ev Event) {
	err := c.processEvent(ctx, ev)
	if err == nil || errors.Is(err, errStale) {
		return
	}
	if c.deps.Log != nil {
		c.deps.Log.Error("controller: process event failed", "kind", ev.Kind, "err", err)
	}
}

func (c *Controller) fireTimer(ctx context.Context, k timerKind) {
	switch k {
	case timerIdle:
		c.clearTimer(timerIdle)
		c.handleEvent(ctx, Event{Kind: EvIdleTimer})
	case timerStartup, timerDrain:
		c.clearTimer(k)
		c.handleEvent(ctx, Event{Kind: EvDeadlineExpired})
	case timerDestroyRetry:
		c.clearTimer(timerDestroyRetry)
		c.fireDestroyRetry(ctx)
	case timerReconcile:
		c.armTimer(timerReconcile, c.deps.Clock.Now().Add(c.deps.Config.ReconcileInterval))
		c.dispatchReconcile(c.effCtx())
	case timerPoll:
		c.clearTimer(timerPoll)
		c.pollTick(ctx)
	case timerFlush:
		c.clearTimer(timerFlush)
		c.flushActivity(ctx)
	case timerMetricsPull:
		c.clearTimer(timerMetricsPull)
		c.mu.Lock()
		state := c.state
		c.mu.Unlock()
		if inService(state) {
			c.armTimer(timerMetricsPull, c.deps.Clock.Now().Add(metricsPullInterval))
			c.dispatchScrape(c.effCtx())
		}
	}
}

// step runs at most one due timer, in same-instant priority order, or
// else one queued event, and reports whether it did anything.
func (c *Controller) step(ctx context.Context) bool {
	if k, ok := c.dueTimer(); ok {
		c.fireTimer(ctx, k)
		return true
	}
	select {
	case ev := <-c.events:
		c.handleEvent(ctx, ev)
		return true
	default:
		return false
	}
}

// Run drives the loop until ctx ends. It never destroys on the way
// out: the persisted deadline still binds the next process.
func (c *Controller) Run(ctx context.Context) error {
	c.mu.Lock()
	c.runCtx = ctx
	c.mu.Unlock()

	if err := c.recover(ctx); err != nil {
		return err
	}

	for {
		for c.step(ctx) {
		}
		var timer <-chan time.Time
		if due := c.nextDue(); !due.IsZero() {
			timer = c.deps.Clock.After(max(due.Sub(c.deps.Clock.Now()), 0))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.wake:
		case ev := <-c.events:
			c.handleEvent(ctx, ev)
		case <-timer:
		}
	}
}

// Start records desired=RUNNING through processEvent and returns within
// the CAS time; the loop dispatches whatever effect that produced.
func (c *Controller) Start(ctx context.Context) (Snapshot, error) {
	return c.callerEvent(ctx, Event{Kind: EvStart})
}

// Stop drains or cancels through processEvent; force is rejected unless
// lifecycle.shutdown.allowForce is set.
func (c *Controller) Stop(ctx context.Context, force bool) (Snapshot, error) {
	if force && !c.deps.Config.ShutdownAllowForce {
		return c.Status(), ErrForceNotEnabled
	}
	return c.callerEvent(ctx, Event{Kind: EvStop})
}

// callerEvent re-decides against the current document when a racing
// write made the guard stale; a run of stale attempts is reported, not
// passed off as success.
func (c *Controller) callerEvent(ctx context.Context, ev Event) (Snapshot, error) {
	for attempt := 0; attempt < callerAttempts; attempt++ {
		err := c.processEvent(ctx, ev)
		if !errors.Is(err, errStale) {
			return c.Status(), err
		}
	}
	return c.Status(), store.ErrRetriesExhausted
}

// Status, dispatchReconcile and recover are placeholders until the
// status, reconcile and restart-recovery work replaces them.
func (c *Controller) Status() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{State: c.state, Public: PublicState(c.state, c.failing)}
}

func (c *Controller) dispatchReconcile(context.Context) {}

func (c *Controller) recover(context.Context) error { return nil }
