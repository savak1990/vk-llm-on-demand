package controller

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

func readyController(t *testing.T) *Controller {
	c, _, _, _, _ := newTestController(t)
	c.state = Ready
	c.generation = 3
	// http.DefaultTransport stands in for a dialed RoundTripper; Admit must
	// hand back whatever the conn holds, so the fixture needs a non-nil one.
	c.conn = tunnel.NewConn(http.DefaultTransport, func() error { return nil }, "fp")
	return c
}

func TestAdmitRequestEndBasic(t *testing.T) {
	c := readyController(t)
	rt, err := c.Admit(3)
	if err != nil || rt == nil {
		t.Fatalf("Admit: rt=%v err=%v", rt, err)
	}
	if c.inFlight != 1 || c.state != Serving {
		t.Fatalf("after Admit: inFlight=%d state=%v", c.inFlight, c.state)
	}
	c.RequestEnd(3)
	if c.inFlight != 0 || c.state != Ready {
		t.Fatalf("after RequestEnd: inFlight=%d state=%v", c.inFlight, c.state)
	}
	if at, pending := c.pendingActivity(); !pending || at.IsZero() {
		t.Fatalf("pendingActivity: at=%v pending=%v, want a recorded timestamp", at, pending)
	}
}

func TestAdmitStaleGenerationIsDraining(t *testing.T) {
	c := readyController(t)
	if _, err := c.Admit(99); !errors.Is(err, ErrDraining) {
		t.Fatalf("Admit(stale gen): want ErrDraining, got %v", err)
	}
}

func TestAdmitDuringDrainingIsDraining(t *testing.T) {
	c := readyController(t)
	c.state = Draining
	if _, err := c.Admit(3); !errors.Is(err, ErrDraining) {
		t.Fatalf("Admit(Draining): want ErrDraining, got %v", err)
	}
}

func TestAdmitWhenNotReadyIsBackendUnavailable(t *testing.T) {
	c := readyController(t)
	c.state = Idle
	if _, err := c.Admit(3); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Admit(Idle): want ErrBackendUnavailable, got %v", err)
	}
}

func TestRequestEndOnNonCurrentGenerationOnlyDecrements(t *testing.T) {
	c := readyController(t)
	c.inFlight = 1
	c.state = Serving
	c.RequestEnd(999) // stale generation
	if c.inFlight != 0 {
		t.Fatalf("inFlight = %d, want 0", c.inFlight)
	}
	if at, pending := c.pendingActivity(); pending || !at.IsZero() {
		t.Fatalf("stale RequestEnd must not touch activity: at=%v pending=%v", at, pending)
	}
	if c.state != Serving {
		t.Fatalf("stale RequestEnd must not change state: got %v", c.state)
	}
}

func TestAdmitVsDrainRace(t *testing.T) {
	c := readyController(t)
	var wg sync.WaitGroup
	var admitted, ended int64
	var mu sync.Mutex

	// Event-driven so the drain flip cannot miss every window inFlight>0.
	firstAdmit := make(chan struct{})
	var firstAdmitOnce sync.Once

	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rt, err := c.Admit(3); err == nil && rt != nil {
				firstAdmitOnce.Do(func() { close(firstAdmit) })
				mu.Lock()
				admitted++
				mu.Unlock()
				c.RequestEnd(3)
				mu.Lock()
				ended++
				mu.Unlock()
			}
		}()
	}
	var drainWG sync.WaitGroup
	drainWG.Add(1)
	go func() {
		defer drainWG.Done()
		<-firstAdmit
		c.mu.Lock()
		c.state = Draining
		c.mu.Unlock()
	}()
	wg.Wait()
	firstAdmitOnce.Do(func() { close(firstAdmit) })
	drainWG.Wait()
	if admitted != ended {
		t.Fatalf("admitted=%d ended=%d, every admitted request must end", admitted, ended)
	}

	c.mu.Lock()
	inFlight, state := c.inFlight, c.state
	c.mu.Unlock()
	if inFlight != 0 {
		t.Fatalf("inFlight = %d, want 0 once every admitted call has ended", inFlight)
	}
	if state != Draining {
		t.Fatalf("state = %v, want Draining", state)
	}
	if admitted <= 0 {
		t.Fatalf("admitted = %d, want at least one request admitted before the drain flip", admitted)
	}
	if admitted >= 1000 {
		t.Logf("admitted = %d (all 1000); the drain flip observed no rejections this run", admitted)
	}
}

func TestComputeIdleDeadline(t *testing.T) {
	timeout := 30 * time.Minute
	readyAt := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	later := readyAt.Add(10 * time.Minute)

	if got := computeIdleDeadline(nil, nil, timeout); !got.IsZero() {
		t.Fatalf("both nil: got %v, want zero", got)
	}
	if got := computeIdleDeadline(&readyAt, nil, timeout); !got.Equal(readyAt.Add(timeout)) {
		t.Fatalf("readyAt only: got %v, want %v", got, readyAt.Add(timeout))
	}
	if got := computeIdleDeadline(&readyAt, &later, timeout); !got.Equal(later.Add(timeout)) {
		t.Fatalf("later lastInferenceFinishedAt wins: got %v, want %v", got, later.Add(timeout))
	}
	if got := computeIdleDeadline(&later, &readyAt, timeout); !got.Equal(later.Add(timeout)) {
		t.Fatalf("earlier lastInferenceFinishedAt loses: got %v, want %v", got, later.Add(timeout))
	}
}

func TestObserveFirstTokenOnlyOncePerGeneration(t *testing.T) {
	c := readyController(t)
	c.ObserveFirstToken(3, 800*time.Millisecond)
	c.ObserveFirstToken(3, 50*time.Millisecond) // second call must not overwrite
	c.stagesMu.Lock()
	got := c.stages["first_token"]
	c.stagesMu.Unlock()
	if got != 800*time.Millisecond {
		t.Fatalf("first_token = %v, want 800ms (first call wins)", got)
	}
}

func TestObserveFirstTokenIgnoresStaleGeneration(t *testing.T) {
	c := readyController(t)
	c.ObserveFirstToken(99, 800*time.Millisecond)
	c.stagesMu.Lock()
	_, ok := c.stages["first_token"]
	c.stagesMu.Unlock()
	if ok {
		t.Fatal("stale generation must not record first_token")
	}
}

func TestReportUpstreamFailureTwoMissesPostsWorkerFailed(t *testing.T) {
	c := readyController(t)
	clk := fakeclock.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	c.deps.Clock = clk
	worker := c.deps.Worker.(*fakeWorker)
	worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable)

	c.ReportUpstreamFailure(3)
	clk.BlockUntil(1)
	clk.Advance(5 * time.Second)

	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvWorkerFailed || ev.Gen != 3 {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestReportUpstreamFailureIsSingleFlight(t *testing.T) {
	c := readyController(t)
	clk := fakeclock.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	c.deps.Clock = clk
	worker := c.deps.Worker.(*fakeWorker)
	worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable)

	c.ReportUpstreamFailure(3)
	clk.BlockUntil(1) // the first probe is now parked between its two attempts

	c.mu.Lock()
	suspect := c.suspect
	c.mu.Unlock()
	if !suspect {
		t.Fatal("suspect flag must be set while the first probe is in flight")
	}

	c.ReportUpstreamFailure(3) // must be a no-op: a probe already owns the flight

	clk.Advance(5 * time.Second)

	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvWorkerFailed || ev.Gen != 3 {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
	if _, ok := recvEvent(t, c); ok {
		t.Fatal("a second ReportUpstreamFailure call while one probe is in flight must not spawn a second probe")
	}
}

func TestReportUpstreamFailureRecoversPostsNothing(t *testing.T) {
	c := readyController(t)
	clk := fakeclock.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	c.deps.Clock = clk
	worker := c.deps.Worker.(*fakeWorker)
	worker.SetProbeSequence(ProbeReady)

	c.ReportUpstreamFailure(3)
	if _, ok := recvEvent(t, c); ok {
		t.Fatal("a recovered probe must not post WorkerFailed")
	}
}
