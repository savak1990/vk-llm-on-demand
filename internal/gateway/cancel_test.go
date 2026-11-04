package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func TestDisconnectCancelsUpstream(t *testing.T) {
	ctxDone := make(chan struct{}, 1)
	rt := fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       &watchedBody{ctx: r.Context(), done: ctxDone},
		}, nil
	})

	d := testDeps(t)
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 5}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`).WithContext(ctx)

	rec := httptest.NewRecorder()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	g.Handler().ServeHTTP(rec, req)

	select {
	case <-ctxDone:
	case <-time.After(time.Second):
		t.Fatal("upstream request context was never cancelled within 1s of client disconnect")
	}
	if _, endCalls := fc.snapshot(); len(endCalls) != 1 || endCalls[0] != 5 {
		t.Fatalf("RequestEnd calls = %v, want exactly [5]", func() []int64 { _, e := fc.snapshot(); return e }())
	}
}

// watchedBody blocks Read until its context is cancelled, then reports
// the cancellation on done and returns an error, simulating a real
// upstream connection that notices the client went away.
type watchedBody struct {
	ctx  context.Context
	done chan struct{}
}

func (b *watchedBody) Read(p []byte) (int, error) {
	<-b.ctx.Done()
	select {
	case b.done <- struct{}{}:
	default:
	}
	return 0, errors.New("context canceled")
}
func (b *watchedBody) Close() error { return nil }
