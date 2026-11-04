package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func (b *watchedBody) Read(_ []byte) (int, error) {
	<-b.ctx.Done()
	select {
	case b.done <- struct{}{}:
	default:
	}
	return 0, errors.New("context canceled")
}
func (b *watchedBody) Close() error { return nil }

// panicBody models a fake upstream reader that panics instead of
// returning an error, confirming the deferred RequestEnd in
// handleChatCompletions still runs as the panic unwinds through it.
type panicBody struct{}

func (panicBody) Read(_ []byte) (int, error) { panic("fake upstream body panicked") }
func (panicBody) Close() error               { return nil }

// TestRequestEndOnEveryPath covers every way a chat-completions
// request can end and asserts RequestEnd fires exactly once, for the
// same generation Admit returned, on each of them.
func TestRequestEndOnEveryPath(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) (admitCalls, endCalls []int64, gen int64)
	}{
		{
			name: "200 plain",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(101)
				rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader([]byte(`{}`)))}, nil
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
		{
			name: "200 stream",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(102)
				chunks := [][]byte{[]byte("data: [DONE]\n\n")}
				rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &chunkedBody{chunks: chunks}}, nil
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`))
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
		{
			name: "500",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(103)
				rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 500, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
		{
			name: "401",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(104)
				rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 401, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
		{
			name: "transport error before headers",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(105)
				rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
					return nil, errors.New("connection reset")
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen, State: controller.Ready}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
		{
			name: "mid-stream reset",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(106)
				failNow := make(chan struct{})
				body := &drainBody{failNow: failNow}
				rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen, State: controller.Ready}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				go close(failNow)
				g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`))
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
		{
			name: "panic in fake reader",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(107)
				rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: panicBody{}}, nil
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
		{
			name: "deadline",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(108)
				rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
					return nil, context.DeadlineExceeded
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
		{
			name: "client cancel",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(109)
				ctxDone := make(chan struct{}, 1)
				rt := fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &watchedBody{ctx: r.Context(), done: ctxDone}}, nil
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				req := chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`).WithContext(ctx)
				go func() {
					time.Sleep(20 * time.Millisecond)
					cancel()
				}()
				g.Handler().ServeHTTP(httptest.NewRecorder(), req)
				select {
				case <-ctxDone:
				case <-time.After(time.Second):
					t.Fatal("upstream request context was never cancelled within 1s of client disconnect")
				}
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
		{
			name: "drain cutoff",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(110)
				failNow := make(chan struct{})
				body := &drainBody{failNow: failNow}
				rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen, State: controller.Ready}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				go func() {
					fc.mu.Lock()
					fc.status.State = controller.Draining
					fc.mu.Unlock()
					close(failNow)
				}()
				g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`))
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
		{
			name: "shutdown",
			run: func(t *testing.T) ([]int64, []int64, int64) {
				gen := int64(111)
				reached := make(chan struct{}, 1)
				rt := fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: &reachedBody{ctx: r.Context(), reached: reached}}, nil
				})
				d := testDeps(t)
				fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: gen}}
				d.Controller = fc
				g, err := New(d)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				done := make(chan struct{})
				go func() {
					g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
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
				ac, ec := fc.snapshot()
				return ac, ec, gen
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			admitCalls, endCalls, gen := c.run(t)
			if len(admitCalls) != len(endCalls) {
				t.Fatalf("Admit calls = %v, RequestEnd calls = %v; counts must match", admitCalls, endCalls)
			}
			if len(admitCalls) != 1 || admitCalls[0] != gen {
				t.Fatalf("Admit calls = %v, want exactly [%d]", admitCalls, gen)
			}
			if len(endCalls) != 1 || endCalls[0] != gen {
				t.Fatalf("RequestEnd calls = %v, want exactly [%d]", endCalls, gen)
			}
		})
	}
}
