package tunnel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestSSHRoundTrip(t *testing.T) {
	backend := startEchoHTTPServer(t)
	addr, fp := startTestSSHServer(t, backend, true)
	host, port, _ := splitHostPortForTest(t, addr)

	conn, err := NewSSH(testSSHKey(t)).Dial(context.Background(), Instance{Host: host, Port: port})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if conn.HostKey() != fp {
		t.Fatalf("HostKey() = %q, want %q", conn.HostKey(), fp)
	}

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

func TestHostKeyMismatch(t *testing.T) {
	backend := startEchoHTTPServer(t)
	addr, _ := startTestSSHServer(t, backend, true)
	host, port, _ := splitHostPortForTest(t, addr)

	_, err := NewSSH(testSSHKey(t)).Dial(context.Background(), Instance{Host: host, Port: port, HostKey: "sha256:not-the-real-fingerprint"})
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("Dial: want ErrHostKeyMismatch, got %v", err)
	}
}

func TestHostKeyTrustOnFirstUse(t *testing.T) {
	backend := startEchoHTTPServer(t)
	addr, fp := startTestSSHServer(t, backend, true)
	host, port, _ := splitHostPortForTest(t, addr)

	conn, err := NewSSH(testSSHKey(t)).Dial(context.Background(), Instance{Host: host, Port: port})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if conn.HostKey() != fp {
		t.Fatalf("HostKey() = %q, want %q", conn.HostKey(), fp)
	}
}

func TestKeepaliveClose(t *testing.T) {
	restore := SetKeepaliveForTest(50*time.Millisecond, 3)
	defer restore()

	backend := startEchoHTTPServer(t)
	addr, _ := startTestSSHServer(t, backend, false) // never acks keepalives
	host, port, _ := splitHostPortForTest(t, addr)

	conn, err := NewSSH(testSSHKey(t)).Dial(context.Background(), Instance{Host: host, Port: port})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	client := &http.Client{Transport: conn.RoundTripper()}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := client.Get("http://worker/"); err != nil {
			return // the tunnel closed itself, as expected
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("keepalive misses never closed the Conn within 2s")
}

func TestSSHCloseIsSafeConcurrentlyAndTwice(t *testing.T) {
	backend := startEchoHTTPServer(t)
	addr, _ := startTestSSHServer(t, backend, true)
	host, port, _ := splitHostPortForTest(t, addr)

	conn, err := NewSSH(testSSHKey(t)).Dial(context.Background(), Instance{Host: host, Port: port})
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
