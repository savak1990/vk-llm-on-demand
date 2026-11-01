package clock

import "time"

// Ticker wraps time.Ticker so callers can swap in a fake without depending on *time.Ticker directly.
type Ticker struct {
	C    <-chan time.Time
	stop func()
}

func (t Ticker) Stop() { t.stop() }

// NewTickerFrom builds a Ticker for implementations outside this package, such as a fake clock.
func NewTickerFrom(c <-chan time.Time, stop func()) Ticker {
	return Ticker{C: c, stop: stop}
}

// Clock is the one interface this package defines; every reconcile/idle/backoff timer takes one instead of calling time directly.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	NewTicker(d time.Duration) Ticker
}

// Real wraps the real time package.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (Real) NewTicker(d time.Duration) Ticker {
	t := time.NewTicker(d)
	return Ticker{C: t.C, stop: t.Stop}
}
