package gateway

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

type chunkedBody struct {
	chunks [][]byte
	i      int
}

func (c *chunkedBody) Read(p []byte) (int, error) {
	if c.i >= len(c.chunks) {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[c.i])
	c.i++
	return n, nil
}
func (c *chunkedBody) Close() error { return nil }

func TestSSEPassThrough(t *testing.T) {
	chunks := [][]byte{
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"),
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n"),
		[]byte("data: [DONE]\n\n"),
	}
	rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       &chunkedBody{chunks: chunks},
		}, nil
	})

	d := testDeps(t)
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 3}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	var lines []string
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != 3 {
		t.Fatalf("got %d non-empty lines, want 3: %v", len(lines), lines)
	}
	if lines[2] != "data: [DONE]" {
		t.Fatalf("last line = %q, want %q", lines[2], "data: [DONE]")
	}

	fc.mu.Lock()
	ft := append([]firstTokenCall{}, fc.firstToken...)
	fc.mu.Unlock()
	if len(ft) != 1 || ft[0].gen != 3 {
		t.Fatalf("ObserveFirstToken calls = %v, want exactly one with gen=3", ft)
	}
}
