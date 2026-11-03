package tunnel

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	keepaliveInterval  = 15 * time.Second
	keepaliveMaxMisses = 3
)

type sshDialer struct{ key func() ([]byte, error) }

// NewSSH builds a Dialer that opens one SSH connection per Dial, over
// which every HTTP request reaches 127.0.0.1:8080 on the worker through
// a direct-tcpip channel. key is read fresh on every Dial.
func NewSSH(key func() ([]byte, error)) Dialer { return &sshDialer{key: key} }

func (d *sshDialer) Dial(ctx context.Context, in Instance) (*Conn, error) {
	keyPEM, err := d.key()
	if err != nil {
		return nil, fmt.Errorf("tunnel: read ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tunnel: parse ssh key: %w", err)
	}

	observed := ""
	mismatch := false
	config := &ssh.ClientConfig{
		User:    "root",
		Auth:    []ssh.AuthMethod{ssh.PublicKeys(signer)},
		Timeout: 15 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			observed = ssh.FingerprintSHA256(key)
			if in.HostKey != "" && observed != in.HostKey {
				mismatch = true
				return ErrHostKeyMismatch
			}
			return nil
		},
	}

	addr := net.JoinHostPort(in.Host, strconv.Itoa(in.Port))

	// ClientConfig.Timeout only bounds the TCP connect; NewClientConn's
	// handshake has no deadline of its own, so we set one on the raw
	// conn and let a cancelled ctx force-close it rather than abandon it.
	rawConn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tunnel: ssh dial: %w", err)
	}
	if err := rawConn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("tunnel: set deadline: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	sshConn, chans, reqs, err := ssh.NewClientConn(rawConn, addr, config)
	stop()
	if err != nil {
		_ = rawConn.Close()
		if mismatch {
			return nil, ErrHostKeyMismatch
		}
		return nil, fmt.Errorf("tunnel: ssh dial: %w", err)
	}
	_ = rawConn.SetDeadline(time.Time{}) // keepalive owns liveness from here
	client := ssh.NewClient(sshConn, chans, reqs)
	// Snapshot tunables so the loop never reads vars a test may rewrite.
	return wrapSSHClient(client, observed, keepaliveInterval, keepaliveMaxMisses), nil
}

func wrapSSHClient(client *ssh.Client, hostKey string, interval time.Duration, maxMisses int) *Conn {
	closed := make(chan struct{})
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			select {
			case <-closed:
				return nil, net.ErrClosed
			default:
			}
			return client.Dial("tcp", "127.0.0.1:8080")
		},
	}
	var once sync.Once
	var closeErr error
	closer := func() error {
		once.Do(func() {
			close(closed)
			transport.CloseIdleConnections()
			closeErr = client.Close()
		})
		return closeErr
	}
	go keepaliveLoop(client, closer, closed, interval, maxMisses)
	return &Conn{rt: transport, hostKey: hostKey, closer: closer}
}

func keepaliveLoop(client *ssh.Client, closeFn func() error, closed <-chan struct{}, interval time.Duration, maxMisses int) {
	misses := 0
	for {
		select {
		case <-closed:
			return
		case <-time.After(interval):
		}
		replyErr := make(chan error, 1)
		go func() {
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			replyErr <- err
		}()
		select {
		case err := <-replyErr:
			if err != nil {
				misses++
			} else {
				misses = 0
			}
		case <-time.After(interval):
			misses++
		}
		if misses >= maxMisses {
			_ = closeFn()
			return
		}
	}
}
