package controller

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Admit is T18: it only ever raises inFlight in memory — never a CAS —
// so concurrent admissions can only increase inFlight, never trigger a
// premature shutdown.
func (c *Controller) Admit(gen int64) (http.RoundTripper, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.generation || c.state == Draining {
		return nil, ErrDraining
	}
	if (c.state != Ready && c.state != Serving) || c.conn == nil {
		return nil, ErrBackendUnavailable
	}
	c.inFlight++
	c.state = Serving
	return c.conn.RoundTripper(), nil
}

// RequestEnd is T19/T25: inFlight only ever decrements (floored at 0);
// the activity timestamp and the Serving->Ready drop apply only to the
// current generation while Serving or Draining.
func (c *Controller) RequestEnd(gen int64) {
	c.mu.Lock()
	if c.inFlight > 0 {
		c.inFlight--
	}
	if gen == c.generation && (c.state == Serving || c.state == Draining) {
		if !c.activityPending {
			c.armTimer(timerFlush, c.deps.Clock.Now().Add(flushDelay))
		}
		c.activityPending = true
		c.activityAt = c.deps.Clock.Now()
		if c.inFlight == 0 && c.state == Serving {
			c.state = Ready
		}
		if c.inFlight == 0 && c.state == Draining {
			c.armTimer(timerDrain, c.deps.Clock.Now())
		}
	}
	c.mu.Unlock()
	c.signalWake()
}

// signalWake nudges the loop without blocking; several signals before
// the loop drains collapse into one.
func (c *Controller) signalWake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// pendingActivity reports the unflushed lastInferenceFinishedAt, if any.
func (c *Controller) pendingActivity() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activityAt, c.activityPending
}

// clearActivity marks the activity recorded at at flushed, unless a newer
// RequestEnd replaced it meanwhile; the loop calls this only after the
// CAS that carried at has committed.
func (c *Controller) clearActivity(at time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.activityAt.Equal(at) {
		return false
	}
	c.activityPending = false
	return true
}

// computeIdleDeadline returns max(readyAt, lastInferenceFinishedAt) +
// timeout; both nil yields the zero time.
func computeIdleDeadline(readyAt, lastInferenceFinishedAt *time.Time, timeout time.Duration) time.Time {
	var base time.Time
	if readyAt != nil {
		base = *readyAt
	}
	if lastInferenceFinishedAt != nil && lastInferenceFinishedAt.After(base) {
		base = *lastInferenceFinishedAt
	}
	if base.IsZero() {
		return time.Time{}
	}
	return base.Add(timeout)
}

// ObserveFirstToken records the first_token stage once per generation;
// a later call for the same generation is a no-op. mu is held for the
// whole check so a generation change can't land between the read and
// the write.
func (c *Controller) ObserveFirstToken(gen int64, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.generation {
		return
	}
	c.stagesMu.Lock()
	if _, already := c.stages["first_token"]; !already {
		c.stages["first_token"] = d
		coldstartStageSeconds.WithLabelValues("first_token").Observe(d.Seconds())
	}
	c.stagesMu.Unlock()
}

// ReportUpstreamFailure is single-flight: it probes twice, 5s apart on
// the clock, and raises WorkerFailed (T24) only if both miss; a call
// while a probe is already in flight is a no-op.
func (c *Controller) ReportUpstreamFailure(gen int64) {
	c.mu.Lock()
	cur, conn := c.generation, c.conn
	live := gen == cur && (c.state == Ready || c.state == Serving) && conn != nil && !c.suspect
	if live {
		c.suspect = true
	}
	c.mu.Unlock()
	if !live {
		return
	}
	c.spawn(func() { c.suspectProbe(gen, conn) })
}

func (c *Controller) suspectProbe(gen int64, conn interface {
	RoundTripper() http.RoundTripper
}) {
	defer func() {
		c.mu.Lock()
		c.suspect = false
		c.mu.Unlock()
	}()
	seed, err := c.deps.WorkerSeed()
	if err != nil {
		if c.deps.Log != nil {
			c.deps.Log.Error("controller: worker seed read failed", "gen", gen, "err", err)
		}
		return
	}
	key := c.deps.Worker.WorkerKey(seed, c.deps.InstallationID, gen)
	misses := 0
	for i := 0; i < 2; i++ {
		if i == 1 {
			<-c.deps.Clock.After(5 * time.Second)
		}
		status, err := c.deps.Worker.Probe(context.Background(), conn.RoundTripper(), key, c.deps.ModelAlias)
		if err != nil || status != ProbeReady {
			misses++
			continue
		}
		return
	}
	if misses == 2 {
		c.postEvent(Event{Kind: EvWorkerFailed, Gen: gen, Err: errors.New("controller: worker suspect probe failed twice")})
	}
}
