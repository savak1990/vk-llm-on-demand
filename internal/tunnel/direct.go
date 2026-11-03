package tunnel

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

type directDialer struct{ addr string }

// NewDirect builds a Dialer that connects over plain TCP to addr,
// ignoring Instance entirely. Used only by the kind job's direct mode.
func NewDirect(addr string) Dialer { return &directDialer{addr: addr} }

func (d *directDialer) Dial(ctx context.Context, _ Instance) (*Conn, error) {
	nd := &net.Dialer{Timeout: 15 * time.Second}
	probe, err := nd.DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, err
	}
	_ = probe.Close()

	closed := make(chan struct{})
	var once sync.Once
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case <-closed:
				return nil, net.ErrClosed
			default:
			}
			return nd.DialContext(ctx, "tcp", d.addr)
		},
	}
	closer := func() error {
		once.Do(func() { close(closed) })
		transport.CloseIdleConnections()
		return nil
	}
	return NewConn(transport, closer, ""), nil
}
