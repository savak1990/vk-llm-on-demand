package gateway

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

// drainBody delivers one chunk, then blocks until told to fail with a
// connection error — simulating the controller closing the tunnel Conn
// drainTimeout after a DRAINING transition.
type drainBody struct {
	mu      sync.Mutex
	sent    bool
	failNow chan struct{}
}

func (b *drainBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if !b.sent {
		b.sent = true
		b.mu.Unlock()
		return copy(p, []byte("data: {\"delta\":\"x\"}\n\n")), nil
	}
	b.mu.Unlock()
	<-b.failNow
	return 0, errors.New("connection reset by peer")
}
func (b *drainBody) Close() error { return nil }

// Covers the drain-cutoff behavior for correctness, driven by channel
// synchronization rather than a fake clock's t=0s/t=60s shape; the
// gateway owns no drain timer, so that exact timing is not this test's job.
func TestDrainCutoffMidStream(t *testing.T) {
	failNow := make(chan struct{})
	body := &drainBody{failNow: failNow}
	rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
	})

	d := testDeps(t)
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 9, State: controller.Ready}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	go func() {
		fc.mu.Lock()
		fc.status.State = controller.Draining
		fc.mu.Unlock()
		close(failNow)
	}()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`))

	var lines []string
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 2 {
		t.Fatalf("got %d non-empty lines, want at least 2 (one data chunk + the cutoff error)", len(lines))
	}
	last := lines[len(lines)-1]
	if last != "data: [DONE]" {
		t.Fatalf("last line = %q, want %q", last, "data: [DONE]")
	}
	if !strings.Contains(rec.Body.String(), "backend_unavailable") {
		t.Fatalf("body does not contain backend_unavailable in-stream error: %q", rec.Body.String())
	}
	fc.mu.Lock()
	gotFailure := len(fc.upstreamErr)
	fc.mu.Unlock()
	if gotFailure != 0 {
		t.Fatalf("ReportUpstreamFailure called %d times, want 0 (drain cutoff is not an upstream failure)", gotFailure)
	}
	if _, endCalls := fc.snapshot(); len(endCalls) != 1 {
		t.Fatalf("RequestEnd calls = %d, want 1", len(endCalls))
	}
}

var _ = io.EOF
