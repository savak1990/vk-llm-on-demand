package main

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthBecomesReadyAfterDelay(t *testing.T) {
	now := time.Now()
	h := newHandler(100*time.Millisecond, "qwen3.6-35b-a3b", func() time.Time { return now })
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("got %d before READY_AFTER, want 503", resp.StatusCode)
	}

	now = now.Add(150 * time.Millisecond)
	resp2, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("got %d after READY_AFTER, want 200", resp2.StatusCode)
	}
}

func TestModelsListsConfiguredAlias(t *testing.T) {
	h := newHandler(0, "qwen3.6-35b-a3b", time.Now)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "qwen3.6-35b-a3b") {
		t.Fatalf("response %q does not contain configured alias", buf[:n])
	}
}

func TestChatCompletionsStreamsAndEnds(t *testing.T) {
	h := newHandler(0, "qwen3.6-35b-a3b", time.Now)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)
	var lastLine string
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			lastLine = line
		}
	}
	if lastLine != "data: [DONE]" {
		t.Fatalf("stream ended with %q, want %q", lastLine, "data: [DONE]")
	}
}
