package tunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"
)

// startTestSSHServer runs a minimal in-process SSH server that proxies
// every direct-tcpip channel to backendAddr, and returns its listen
// address and host key fingerprint. ackGlobalRequests controls whether
// it replies to keepalive requests (TestKeepaliveClose sets it false).
func startTestSSHServer(t *testing.T, backendAddr string, ackGlobalRequests bool) (addr, hostKeyFingerprint string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleTestSSHConn(conn, config, backendAddr, ackGlobalRequests)
		}
	}()

	return ln.Addr().String(), ssh.FingerprintSHA256(signer.PublicKey())
}

func handleTestSSHConn(conn net.Conn, config *ssh.ServerConfig, backendAddr string, ack bool) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer func() { _ = sc.Close() }()

	go func() {
		for req := range reqs {
			if ack && req.WantReply {
				_ = req.Reply(true, nil)
			}
		}
	}()

	for newChan := range chans {
		if newChan.ChannelType() != "direct-tcpip" {
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(chReqs)
		go proxyToBackend(ch, backendAddr)
	}
}

func proxyToBackend(ch ssh.Channel, backendAddr string) {
	defer func() { _ = ch.Close() }()
	backend, err := net.Dial("tcp", backendAddr)
	if err != nil {
		return
	}
	defer func() { _ = backend.Close() }()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(backend, ch); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ch, backend); done <- struct{}{} }()
	<-done
}

func testSSHKey(t *testing.T) func() ([]byte, error) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	keyPEM := pem.EncodeToMemory(pemBlock)
	return func() ([]byte, error) { return keyPEM, nil }
}

func splitHostPortForTest(t *testing.T, addr string) (host string, port int, err error) {
	t.Helper()
	h, p, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		t.Fatalf("SplitHostPort: %v", splitErr)
	}
	portNum, convErr := strconv.Atoi(p)
	if convErr != nil {
		t.Fatalf("Atoi: %v", convErr)
	}
	return h, portNum, nil
}
