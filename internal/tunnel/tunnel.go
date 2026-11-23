// Package tunnel carries HTTP traffic between the controller and the
// worker, over an SSH connection or, for the kind job only, plain TCP.
package tunnel

import (
	"context"
	"errors"
	"net/http"
)

// Instance is the worker address to dial. HostKey is the pinned SHA256
// fingerprint from the document; empty means trust-on-first-use.
type Instance struct {
	Host    string
	Port    int
	HostKey string
}

// Conn is one tunnel to a worker: an HTTP transport, a way to tear it
// down, and the host key fingerprint observed (or pinned) at dial time.
type Conn struct {
	rt      http.RoundTripper
	closer  func() error
	hostKey string
	exec    func(context.Context, string) ([]byte, error)
}

// NewConn builds a Conn from its parts; used by Dialer implementations
// and by test fakes outside this package.
func NewConn(rt http.RoundTripper, closer func() error, hostKey string) *Conn {
	return &Conn{rt: rt, closer: closer, hostKey: hostKey}
}

// RoundTripper sends HTTP requests over this tunnel; requests reach
// 127.0.0.1:8080 on the worker regardless of the request's own URL host.
func (c *Conn) RoundTripper() http.RoundTripper { return c.rt }

// Close tears the tunnel down; requests in flight on it fail.
func (c *Conn) Close() error { return c.closer() }

// HostKey is the fingerprint seen at dial; the caller persists it.
func (c *Conn) HostKey() string { return c.hostKey }

// NewConnWithExec is NewConn plus a command runner on the worker.
func NewConnWithExec(rt http.RoundTripper, closer func() error, hostKey string, exec func(context.Context, string) ([]byte, error)) *Conn {
	return &Conn{rt: rt, closer: closer, hostKey: hostKey, exec: exec}
}

// Exec runs cmd on the worker and returns its stdout.
func (c *Conn) Exec(ctx context.Context, cmd string) ([]byte, error) {
	if c.exec == nil {
		return nil, ErrExecUnsupported
	}
	return c.exec(ctx, cmd)
}

// Dialer opens one Conn to a worker instance.
type Dialer interface {
	Dial(ctx context.Context, in Instance) (*Conn, error)
}

// ErrHostKeyMismatch means the worker's host key changed since it was
// pinned; the caller must treat this as a destroy-worthy failure.
var ErrHostKeyMismatch = errors.New("tunnel: host key changed")

// ErrExecUnsupported is returned by Exec on a tunnel that cannot run commands, such as the direct TCP dialer.
var ErrExecUnsupported = errors.New("tunnel: exec not supported by this dialer")
