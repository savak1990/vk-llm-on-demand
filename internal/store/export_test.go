package store

import (
	"context"
	"time"
)

// SetWaitForTest swaps Transition's retry-backoff wait for f; call the
// returned restore (e.g. via t.Cleanup) to put the original back.
func SetWaitForTest(f func(context.Context, time.Duration) error) (restore func()) {
	old := wait
	wait = f
	return func() { wait = old }
}
