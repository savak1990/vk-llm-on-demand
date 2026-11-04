package gateway

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	// Fixture chunks carry reasoning_content and tool_calls deltas, not
	// just plain content, so a line-assembly bug that only handles the
	// simple shape would still show up here (AC8).
	chunks := [][]byte{
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"),
		[]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n"),
		[]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"f\",\"arguments\":\"{}\"}}]}}]}\n\n"),
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

	var want bytes.Buffer
	for _, c := range chunks {
		want.Write(c)
	}
	if rec.Body.String() != want.String() {
		t.Fatalf("body = %q, want the upstream chunks copied byte for byte: %q", rec.Body.String(), want.String())
	}

	var lines []string
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) != len(chunks) {
		t.Fatalf("got %d non-empty lines, want %d: %v", len(lines), len(chunks), lines)
	}
	if last := lines[len(lines)-1]; last != "data: [DONE]" {
		t.Fatalf("last line = %q, want %q", last, "data: [DONE]")
	}

	fc.mu.Lock()
	ft := append([]firstTokenCall{}, fc.firstToken...)
	fc.mu.Unlock()
	if len(ft) != 1 || ft[0].gen != 3 {
		t.Fatalf("ObserveFirstToken calls = %v, want exactly one with gen=3", ft)
	}
}

// channelChunkedBody delivers one chunk per receive on ch, letting a
// test control exactly when each SSE chunk becomes readable; closing ch
// ends the stream with io.EOF, like a real upstream finishing cleanly.
type channelChunkedBody struct {
	ch chan []byte
}

func (c *channelChunkedBody) Read(p []byte) (int, error) {
	chunk, ok := <-c.ch
	if !ok {
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}
func (c *channelChunkedBody) Close() error { return nil }

// TestSSELatencyPerChunk releases 20 SSE chunks one at a time over a
// channel the test controls and asserts each is observed by a real
// client within a tight bound of its release.
func TestSSELatencyPerChunk(t *testing.T) {
	const n = 20
	ch := make(chan []byte)
	body := &channelChunkedBody{ch: ch}
	rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       body,
		}, nil
	})

	d := testDeps(t)
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	req.Header.Set("Content-Type", "application/json")

	// releases carries each chunk's send time to the matching loop
	// below; advance gates the next send on this chunk's arrival being
	// confirmed, so a stuck flush blocks progress instead of racing it.
	releases := make(chan time.Time, 1)
	advance := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			releases <- time.Now()
			ch <- []byte(fmt.Sprintf("data: {\"i\":%d}\n\n", i))
			if i < n-1 {
				<-advance
			}
		}
		close(ch)
	}()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: {") {
				lines <- line
			}
		}
		close(lines)
	}()

	for i := 0; i < n; i++ {
		release := <-releases
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("chunk %d: stream ended early", i)
			}
			if want := fmt.Sprintf("data: {\"i\":%d}", i); line != want {
				t.Fatalf("chunk %d = %q, want %q", i, line, want)
			}
			if lag := time.Since(release); lag > 50*time.Millisecond {
				t.Errorf("chunk %d arrived %v after release, want under 50ms", i, lag)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("chunk %d: never arrived within 200ms of release", i)
		}
		if i < n-1 {
			advance <- struct{}{}
		}
	}
}

// failWriteResponseWriter's Write always fails, modeling a client that
// disconnected mid-stream after the 200/SSE headers already went out.
// Unwrap lets http.ResponseController still reach the recorder's Flush.
type failWriteResponseWriter struct {
	http.ResponseWriter
}

func (f *failWriteResponseWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func (f *failWriteResponseWriter) Unwrap() http.ResponseWriter { return f.ResponseWriter }

func TestWriteErrorMidStreamLabeled499(t *testing.T) {
	d := testDeps(t)
	fc := &fakeController{status: controller.Snapshot{Generation: 1}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sr := &statusRecorder{ResponseWriter: &failWriteResponseWriter{ResponseWriter: httptest.NewRecorder()}}
	resp := &http.Response{
		StatusCode: 200,
		Body:       &chunkedBody{chunks: [][]byte{[]byte("data: x\n\n")}},
	}

	g.streamUpstream(sr, resp, 1, chatReq(`{}`), g.deps.Clock.Now())

	if sr.codeOverride != "499" {
		t.Fatalf("codeOverride = %q, want %q (a write failure mid-stream is a disconnect, not a 200)", sr.codeOverride, "499")
	}
}
