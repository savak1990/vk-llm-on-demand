package controller

import (
	"context"
	"errors"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

const recoverRetry = 5 * time.Second

// recover maps the stored document onto memory before Run's loop starts.
// Until a Load succeeds nothing is mapped or armed, so no provider call can
// act on a state the process has not read; ErrInstallation stops Run.
func (c *Controller) recover(ctx context.Context) error {
	for {
		err := c.recoverOnce(ctx)
		if err == nil || errors.Is(err, store.ErrInstallation) {
			return err
		}
		if c.deps.Log != nil {
			c.deps.Log.Warn("controller: recovery failed, retrying", "err", err)
		}
		select {
		case <-c.deps.Clock.After(recoverRetry):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Controller) recoverOnce(ctx context.Context) error {
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil {
		return err
	}
	now := c.deps.Clock.Now()
	inst := doc.Instance

	// The loop resumes from Booting, whose T12/T13 guards require the stored state to match.
	if inst != nil && (inst.State == Loading.String() || (inst.State == Creating.String() && inst.ProviderID != "")) {
		if doc, err = c.rewriteState(ctx, doc, Booting); err != nil {
			return err
		}
		inst = doc.Instance
	}

	var next State
	switch {
	case inst == nil:
		next = Idle
	case inst.State == Creating.String():
		next = Creating
	case inst.State == Booting.String():
		next = Booting
	case isState(inst.State, Ready, Serving):
		next = Ready
	case inst.State == Draining.String():
		next = Draining
	default: // Destroying, or a state this binary does not know: tearing down is the cost-safe reading
		next = Destroying
	}

	c.mu.Lock()
	c.generation, c.state = doc.Generation, next
	c.mu.Unlock()
	if next == Destroying {
		c.armTimer(timerDestroyRetry, now)
	} else {
		c.syncTimers(Idle, next, doc)
	}

	f := Facts{Now: now, Doc: doc, Cfg: c.deps.Config}
	switch {
	case next == Ready && (idleRuleFires(f) || lifetimeExpired(f) || doc.Desired == store.DesiredStopped):
		// T22 then T25: the same writes as a live idle stop, so desired ends STOPPED.
		c.handleEvent(ctx, Event{Kind: EvStop, Gen: doc.Generation})
		c.handleEvent(ctx, Event{Kind: EvDeadlineExpired, Gen: doc.Generation})
	case next == Ready:
		if id, ok := providerIDOf(doc); ok {
			go c.recoverDial(c.effCtx(), id, doc.Generation, inst.HostKeyFingerprint)
		}
	case next == Draining:
		// In-flight requests died with the previous process, so the drain is already complete.
		c.handleEvent(ctx, Event{Kind: EvDeadlineExpired, Gen: doc.Generation})
	}

	c.armTimer(timerReconcile, now)
	return nil
}

func (c *Controller) rewriteState(ctx context.Context, doc store.Document, to State) (store.Document, error) {
	gen, from := doc.Generation, doc.Instance.State
	return store.Transition(ctx, c.deps.Store, func(d *store.Document) error {
		if d.Instance == nil || d.Generation != gen || d.Instance.State != from {
			return errStale
		}
		d.Instance.State = to.String()
		return nil
	})
}

func isState(s string, candidates ...State) bool {
	for _, c := range candidates {
		if s == c.String() {
			return true
		}
	}
	return false
}

// recoverDial re-establishes the tunnel to a Ready instance at its pinned host
// key, retrying every 5s while the instance is still current, then hands off to
// the suspect probe: Ready re-adopts with no write, two misses raise T24.
func (c *Controller) recoverDial(ctx context.Context, id provider.InstanceID, gen int64, hostKey string) {
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			select {
			case <-c.deps.Clock.After(recoverRetry):
			case <-ctx.Done():
				return
			}
		}
		c.mu.Lock()
		live := gen == c.generation && inService(c.state)
		c.mu.Unlock()
		if !live {
			return
		}
		inst, err := c.deps.Provider.GetInstance(ctx, id)
		if err != nil || inst.SSHHost == "" {
			continue
		}
		conn, err := c.deps.Dialer.Dial(ctx, tunnel.Instance{Host: inst.SSHHost, Port: inst.SSHPort, HostKey: hostKey})
		if errors.Is(err, tunnel.ErrHostKeyMismatch) {
			c.postEvent(Event{Kind: EvWorkerFailed, Gen: gen, Err: err})
			return
		}
		if err != nil {
			continue
		}
		c.mu.Lock()
		live = gen == c.generation && inService(c.state) && c.conn == nil
		if live {
			c.conn = conn
		}
		c.mu.Unlock()
		if !live {
			_ = conn.Close()
			return
		}
		c.ReportUpstreamFailure(gen)
		return
	}
}
