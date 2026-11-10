package tunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/obs/obstest"
)

func TestSSHRoundTrip(t *testing.T) {
	backend := startEchoHTTPServer(t)
	addr, fp := startTestSSHServer(t, backend, globalRequestAck)
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

func TestDialRejectsEmptyInstance(t *testing.T) {
	keyRead := false
	key := func() ([]byte, error) {
		keyRead = true
		return nil, nil
	}

	start := time.Now()
	_, err := NewSSH(key).Dial(context.Background(), Instance{})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Dial took %v, want it to reject the empty instance instantly", elapsed)
	}
	if err == nil {
		t.Fatal("Dial: want error for zero-value Instance, got nil")
	}
	if !strings.Contains(err.Error(), "no ssh address") {
		t.Fatalf("Dial err = %q, want it to mention a missing ssh address", err)
	}
	if keyRead {
		t.Fatal("Dial read the ssh key before validating the address; the guard ran too late or a dial was attempted")
	}
}

func TestHostKeyMismatch(t *testing.T) {
	backend := startEchoHTTPServer(t)
	addr, _ := startTestSSHServer(t, backend, globalRequestAck)
	host, port, _ := splitHostPortForTest(t, addr)

	_, err := NewSSH(testSSHKey(t)).Dial(context.Background(), Instance{Host: host, Port: port, HostKey: "sha256:not-the-real-fingerprint"})
	if !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("Dial: want ErrHostKeyMismatch, got %v", err)
	}
}

func TestHostKeyTrustOnFirstUse(t *testing.T) {
	backend := startEchoHTTPServer(t)
	addr, fp := startTestSSHServer(t, backend, globalRequestAck)
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
	addr, _ := startTestSSHServer(t, backend, globalRequestSilent) // never acks keepalives
	host, port, _ := splitHostPortForTest(t, addr)

	conn, err := NewSSH(testSSHKey(t)).Dial(context.Background(), Instance{Host: host, Port: port})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	client := &http.Client{Transport: conn.RoundTripper()}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://worker/")
		if err != nil {
			return // the tunnel closed itself, as expected
		}
		_ = resp.Body.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("keepalive misses never closed the Conn within 2s")
}

func TestKeepaliveSurvivesRealOpenSSHFailureReply(t *testing.T) {
	restore := SetKeepaliveForTest(20*time.Millisecond, 3)
	defer restore()

	backend := startEchoHTTPServer(t)
	addr, _ := startTestSSHServer(t, backend, globalRequestNack) // replies ok=false, like real OpenSSH
	host, port, _ := splitHostPortForTest(t, addr)

	conn, err := NewSSH(testSSHKey(t)).Dial(context.Background(), Instance{Host: host, Port: port})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	client := &http.Client{Transport: conn.RoundTripper()}
	for i := 0; i < 25; i++ {
		resp, err := client.Get("http://worker/")
		if err != nil {
			t.Fatalf("Get #%d: %v, want the tunnel to survive a REQUEST_FAILURE keepalive reply", i, err)
		}
		_ = resp.Body.Close()
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSSHCloseIsSafeConcurrentlyAndTwice(t *testing.T) {
	backend := startEchoHTTPServer(t)
	addr, _ := startTestSSHServer(t, backend, globalRequestAck)
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

// TestSSHKeyNeverLeaksInDialErrors proves the private key text never
// ends up in an error Dial returns, across both error paths that
// touch the key: a host-key mismatch (after the key was read and
// used) and a parse failure (the key itself is malformed). The PEM
// is checked line by line, skipping the "-----BEGIN/END" markers,
// since a %q-quoted leak would escape the real newlines between them
// and defeat a whole-PEM substring match.
func TestSSHKeyNeverLeaksInDialErrors(t *testing.T) {
	keyFn := testSSHKey(t)
	keyPEM, err := keyFn()
	if err != nil {
		t.Fatalf("key func: %v", err)
	}

	backend := startEchoHTTPServer(t)
	addr, _ := startTestSSHServer(t, backend, globalRequestAck)
	host, port, _ := splitHostPortForTest(t, addr)

	_, mismatchErr := NewSSH(keyFn).Dial(context.Background(), Instance{Host: host, Port: port, HostKey: "sha256:not-the-real-fingerprint"})
	if !errors.Is(mismatchErr, ErrHostKeyMismatch) {
		t.Fatalf("Dial: want ErrHostKeyMismatch, got %v", mismatchErr)
	}

	_, parseErr := NewSSH(func() ([]byte, error) { return []byte("not a valid key"), nil }).
		Dial(context.Background(), Instance{Host: host, Port: port})
	if parseErr == nil {
		t.Fatal("Dial: want error for a malformed key, got nil")
	}

	var buf bytes.Buffer
	buf.WriteString(mismatchErr.Error())
	buf.WriteString(parseErr.Error())

	for _, line := range strings.Split(string(keyPEM), "\n") {
		if line == "" || strings.HasPrefix(line, "-----") {
			continue
		}
		obstest.AssertNoSecret(t, buf.Bytes(), line)
	}
}

func TestSSHDialHandshakeStallDoesNotHangOrLeak(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- conn // accept, then never speak SSH
	}()

	host, port, _ := splitHostPortForTest(t, ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, dialErr := NewSSH(testSSHKey(t)).Dial(ctx, Instance{Host: host, Port: port})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Dial took %v, want it to return promptly on ctx cancellation", elapsed)
	}
	if dialErr == nil {
		t.Fatal("Dial: want error, got nil")
	}

	select {
	case conn := <-accepted:
		defer func() { _ = conn.Close() }()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			if _, err := conn.Read(make([]byte, 1)); err != nil {
				if errors.Is(err, io.EOF) {
					return // our side closed the stalled connection, as expected
				}
			}
		}
		t.Fatal("stalled server-side connection was never closed by Dial")
	case <-time.After(2 * time.Second):
		t.Fatal("listener never accepted a connection")
	}
}
