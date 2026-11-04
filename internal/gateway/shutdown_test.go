package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func TestShutdownCutoffDelay(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		deadline time.Time
		want     time.Duration
	}{
		{"30s out, 5s margin", now.Add(30 * time.Second), 25 * time.Second},
		{"3s out, already inside the margin", now.Add(3 * time.Second), 0},
		{"exactly 5s out", now.Add(5 * time.Second), 0},
	}
	for _, c := range cases {
		if got := shutdownCutoffDelay(now, c.deadline); got != c.want {
			t.Errorf("%s: shutdownCutoffDelay = %v, want %v", c.name, got, c.want)
		}
	}
}

// reachedBody signals once reading starts, then blocks on the request
// context like a real upstream connection that notices cancellation,
// letting the test wait for the handler to block without sleeping.
type reachedBody struct {
	ctx     context.Context
	reached chan struct{}
}

func (b *reachedBody) Read(_ []byte) (int, error) {
	select {
	case b.reached <- struct{}{}:
	default:
	}
	<-b.ctx.Done()
	return 0, errors.New("context canceled")
}
func (b *reachedBody) Close() error { return nil }

func TestShutdownDrains(t *testing.T) {
	reached := make(chan struct{}, 1)
	rt := fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: &reachedBody{ctx: r.Context(), reached: reached}}, nil
	})

	d := testDeps(t)
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})
	rec := httptest.NewRecorder()
	go func() {
		g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
		close(done)
	}()
	<-reached // the handler is now blocked on the upstream read; no sleep needed

	// A context with no deadline skips the cutoff-wait branch, isolating
	// this test to cancelActive + WaitGroup draining; the cutoff math
	// itself is covered separately by TestShutdownCutoffDelay.
	if err := g.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// Shutdown only returns once activeWG has drained, which happens
	// after RequestEnd's defer runs; asserting here (not after <-done)
	// is what proves Shutdown actually waited rather than raced it.
	if _, endCalls := fc.snapshot(); len(endCalls) != 1 {
		t.Fatalf("RequestEnd calls = %d, want 1", len(endCalls))
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler never returned after Shutdown")
	}
}

// TestShutdownDrainsPlain covers the plain (non-stream) half of a
// Shutdown cutoff: a plain request blocks inside the round trip
// itself, not inside a body read, so it needs its own fake round
// tripper rather than reachedBody.
func TestShutdownDrainsPlain(t *testing.T) {
	reached := make(chan struct{}, 1)
	rt := fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	d := testDeps(t)
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})
	rec := httptest.NewRecorder()
	go func() {
		g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
		close(done)
	}()
	<-reached

	if err := g.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler never returned after Shutdown")
	}

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("Retry-After = %q, want %q", got, "30")
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body.Error.Code != "shutting_down" {
		t.Fatalf("body code = %q, want %q", body.Error.Code, "shutting_down")
	}
	if _, endCalls := fc.snapshot(); len(endCalls) != 1 {
		t.Fatalf("RequestEnd calls = %d, want 1", len(endCalls))
	}
	fc.mu.Lock()
	gotFailure := len(fc.upstreamErr)
	fc.mu.Unlock()
	if gotFailure != 0 {
		t.Fatalf("ReportUpstreamFailure called %d times, want 0", gotFailure)
	}
}
