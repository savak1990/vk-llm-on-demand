package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

// holdRun drives one held request; the body is read only after ServeHTTP returns.
type holdRun struct {
	clk  *fakeclock.Clock
	g    *Gateway
	fc   *fakeController
	rec  *httptest.ResponseRecorder
	done chan struct{}
}

func startHold(t *testing.T, fc *fakeController) *holdRun {
	t.Helper()
	clk := fakeclock.New(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	d := onDemandDeps(t, fc)
	d.Clock = clk
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := &holdRun{clk: clk, g: g, fc: fc, rec: httptest.NewRecorder(), done: make(chan struct{})}
	go func() {
		g.Handler().ServeHTTP(h.rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`))
		close(h.done)
	}()
	return h
}

// settle waits until cond holds or the handler returns; it reports whether the handler is still running.
func (h *holdRun) settle(cond func() bool) bool {
	deadline := time.Now().Add(time.Second)
	for !cond() && time.Now().Before(deadline) {
		select {
		case <-h.done:
			return false
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-h.done:
		return false
	default:
		return true
	}
}

// run advances the clock one poll at a time; after each advance it waits
// for the handler's Status read, so no ticker fire is dropped.
func (h *holdRun) run(t *testing.T, limit time.Duration) string {
	t.Helper()
	if !h.settle(func() bool { _, ok := h.clk.NextDeadline(); return ok }) {
		return h.rec.Body.String()
	}
	for elapsed := time.Duration(0); elapsed <= limit; elapsed += 2 * time.Second {
		before := h.fc.statusCount()
		h.clk.Advance(2 * time.Second)
		if !h.settle(func() bool { return h.fc.statusCount() > before }) {
			return h.rec.Body.String()
		}
	}
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return")
	}
	return h.rec.Body.String()
}

func scripted(snaps ...controller.Snapshot) func() controller.Snapshot {
	var mu sync.Mutex
	i := 0
	return func() controller.Snapshot {
		mu.Lock()
		defer mu.Unlock()
		s := snaps[min(i, len(snaps)-1)]
		i++
		return s
	}
}

func TestHeldStreamProgressThenAnswer(t *testing.T) {
	worker := fakeRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: &chunkedBody{chunks: [][]byte{[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")}}}, nil
	})
	var admits atomic.Int32
	fc := &fakeController{
		demandSnap: controller.Snapshot{Public: "ALLOCATING", Desired: "RUNNING"},
		admitFn: func(int64) (http.RoundTripper, error) {
			if admits.Add(1) == 1 {
				return nil, controller.ErrBackendUnavailable // the handler's first Admit, before the hold
			}
			return worker, nil
		},
	}
	fc.statusFn = scripted(
		controller.Snapshot{Public: "ALLOCATING", Desired: "RUNNING"},
		controller.Snapshot{Public: "STARTING", Desired: "RUNNING", MachineID: 7, GPUName: "L40S"},
		controller.Snapshot{Public: "READY", Desired: "RUNNING", Ready: true, Generation: 4},
	)
	h := startHold(t, fc)
	body := h.run(t, 30*time.Second)
	if h.rec.Code != http.StatusOK {
		t.Fatalf("code = %d", h.rec.Code)
	}
	iAlloc := strings.Index(body, "Looking for a GPU offer")
	iOffer := strings.Index(body, "Renting an L40S")
	iReady := strings.Index(body, "[vk-llm] Ready.")
	iAnswer := strings.Index(body, `"content":"hi"`)
	if iAlloc < 0 || iOffer < iAlloc || iReady < iOffer || iAnswer < iReady {
		t.Fatalf("order wrong: %d %d %d %d\n%s", iAlloc, iOffer, iReady, iAnswer, body)
	}
	if strings.Count(body, "Looking for a GPU offer") != 1 {
		t.Fatalf("checkpoint repeated:\n%s", body)
	}
}

func TestHeldStreamHeartbeatAndTicker(t *testing.T) {
	fc := &fakeController{admitErr: controller.ErrBackendUnavailable, demandSnap: controller.Snapshot{Public: "ALLOCATING", Desired: "RUNNING"}}
	fc.statusFn = scripted(controller.Snapshot{Public: "ALLOCATING", Desired: "RUNNING"})
	h := startHold(t, fc)
	body := h.run(t, 610*time.Second)
	if n := strings.Count(body, ": keep-alive\n\n"); n < 30 {
		t.Fatalf("keep-alive count = %d, want ≥ 30 over 600 s", n)
	}
	var phrases []string
	for _, line := range strings.Split(body, "\n") {
		for _, p := range defaultTickerPhrases {
			if strings.Contains(line, p) {
				phrases = append(phrases, p)
			}
		}
	}
	if len(phrases) < 15 {
		t.Fatalf("ticker phrases = %d, want ≥ 15 over 600 s", len(phrases))
	}
	for i := 1; i < len(phrases); i++ {
		if phrases[i] == phrases[i-1] {
			t.Fatalf("phrase %q twice in a row", phrases[i])
		}
	}
	if !strings.Contains(body, "still starting") || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("maxWait tail missing:\n%s", body[max(0, len(body)-400):])
	}
	if len(fc.demandCalls) < 30 {
		t.Fatalf("Demand refreshes = %d, want ≥ 30", len(fc.demandCalls))
	}
	last := fc.demandCalls[len(fc.demandCalls)-1]
	if want := h.clk.Now().Add(longestColdStart); last.Before(want) {
		t.Fatalf("last demand deadline %v, want ≥ %v so the start survives the expiry", last, want)
	}
}

func TestHeldStreamEndsOnShutdown(t *testing.T) {
	fc := &fakeController{admitErr: controller.ErrBackendUnavailable, demandSnap: controller.Snapshot{Public: "ALLOCATING", Desired: "RUNNING"}}
	fc.statusFn = scripted(controller.Snapshot{Public: "ALLOCATING", Desired: "RUNNING"})
	h := startHold(t, fc)
	h.settle(func() bool { _, ok := h.clk.NextDeadline(); return ok })
	h.g.shuttingDown.Store(true)
	body := h.run(t, 10*time.Second)
	if !strings.Contains(body, `"code":"shutting_down"`) || !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("body:\n%s", body)
	}
}

func TestHeldStreamDenied(t *testing.T) {
	fc := &fakeController{admitErr: controller.ErrBackendUnavailable, demandErr: controller.ErrGPUUnavailable, demandSnap: controller.Snapshot{Unavailable: "attempts_exhausted"}}
	h := startHold(t, fc)
	body := h.run(t, 4*time.Second)
	if !strings.Contains(body, "[vk-llm] Too many GPU start attempts") || !strings.Contains(body, `"finish_reason":"stop"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("body:\n%s", body)
	}
}

func TestHeldStreamStartFails(t *testing.T) {
	fc := &fakeController{admitErr: controller.ErrBackendUnavailable, demandSnap: controller.Snapshot{Public: "ALLOCATING", Desired: "RUNNING"}}
	fc.statusFn = scripted(controller.Snapshot{Public: "STOPPED", Desired: "STOPPED", Unavailable: "no_offer"})
	h := startHold(t, fc)
	body := h.run(t, 10*time.Second)
	if !strings.Contains(body, "No GPU offer matches") || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("body:\n%s", body)
	}
}

func readyAfterHold(worker http.RoundTripper) *fakeController {
	var admits atomic.Int32
	fc := &fakeController{
		demandSnap: controller.Snapshot{Public: "ALLOCATING", Desired: "RUNNING"},
		admitFn: func(int64) (http.RoundTripper, error) {
			if admits.Add(1) == 1 {
				return nil, controller.ErrBackendUnavailable
			}
			return worker, nil
		},
	}
	fc.statusFn = scripted(controller.Snapshot{Public: "READY", Desired: "RUNNING", Ready: true, Generation: 4})
	return fc
}

func TestHeldStreamWorkerErrorEndsWithTail(t *testing.T) {
	fc := readyAfterHold(fakeRoundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("boom") }))
	h := startHold(t, fc)
	body := h.run(t, 10*time.Second)
	if !strings.Contains(body, "[vk-llm] connection to the worker was lost") || !strings.Contains(body, `"finish_reason":"stop"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("body:\n%s", body)
	}
}

func TestHeldStreamWorkerRejectsEndsWithTail(t *testing.T) {
	fc := readyAfterHold(fakeRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: &chunkedBody{}}, nil
	}))
	h := startHold(t, fc)
	body := h.run(t, 10*time.Second)
	if !strings.Contains(body, "[vk-llm] the worker rejected the request") || !strings.Contains(body, `"finish_reason":"stop"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("body:\n%s", body)
	}
}

func TestHeldSlotReleasedWhileAnswering(t *testing.T) {
	var h *holdRun
	var held, pending atomic.Int64
	held.Store(-1)
	fc := readyAfterHold(fakeRoundTripper(func(*http.Request) (*http.Response, error) {
		held.Store(h.g.held.Load())
		pending.Store(int64(testutil.ToFloat64(h.g.metrics.pendingRequests)))
		return &http.Response{StatusCode: 200, Body: &chunkedBody{chunks: [][]byte{[]byte("data: [DONE]\n\n")}}}, nil
	}))
	h = startHold(t, fc)
	h.run(t, 10*time.Second)
	if held.Load() != 0 || pending.Load() != 0 {
		t.Fatalf("while answering: held = %d, pending = %d, want 0 and 0", held.Load(), pending.Load())
	}
	if got := h.g.held.Load(); got != 0 {
		t.Fatalf("after return: held = %d, want 0 (released once)", got)
	}
	if got := testutil.ToFloat64(h.g.metrics.pendingRequests); got != 0 {
		t.Fatalf("after return: pending = %v, want 0", got)
	}
}

type failingWriter struct{ h http.Header }

func (f *failingWriter) Header() http.Header       { return f.h }
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("client gone") }
func (f *failingWriter) WriteHeader(int)           {}

func TestHeldStreamWriteFailureIsClientGone(t *testing.T) {
	fc := &fakeController{admitErr: controller.ErrBackendUnavailable, demandSnap: controller.Snapshot{Public: "ALLOCATING", Desired: "RUNNING"}}
	g, err := New(onDemandDeps(t, fc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	g.Handler().ServeHTTP(&failingWriter{h: http.Header{}}, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`))
	if got := testutil.ToFloat64(g.metrics.coldStartOutcomesTotal.WithLabelValues("client_gone")); got != 1 {
		t.Fatalf("client_gone outcomes = %v, want 1", got)
	}
}
