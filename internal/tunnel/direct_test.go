package tunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
)

func startEchoHTTPServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})}
		_ = srv.Serve(ln)
	}()
	return ln.Addr().String()
}

func TestDirectRoundTrip(t *testing.T) {
	addr := startEchoHTTPServer(t)
	conn, err := NewDirect(addr).Dial(context.Background(), Instance{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	client := &http.Client{Transport: conn.RoundTripper()}
	resp, err := client.Get("http://worker/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

func TestDirectDialFailure(t *testing.T) {
	// Nothing listens here; Dial must fail fast, not hang.
	if _, err := NewDirect("127.0.0.1:1").Dial(context.Background(), Instance{}); err == nil {
		t.Fatal("Dial: want error, got nil")
	}
}

func TestDirectCloseStopsNewConnections(t *testing.T) {
	addr := startEchoHTTPServer(t)
	conn, err := NewDirect(addr).Dial(context.Background(), Instance{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	client := &http.Client{Transport: conn.RoundTripper()}
	resp, err := client.Get("http://worker/")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("Get after Close: want error, got nil")
	}
}

func TestDirectCloseIsSafeConcurrentlyAndTwice(t *testing.T) {
	addr := startEchoHTTPServer(t)
	conn, err := NewDirect(addr).Dial(context.Background(), Instance{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = conn.Close()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Close() goroutine %d: %v, want nil", i, err)
		}
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("Close() again: %v, want nil", err)
	}
}
