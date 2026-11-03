package store

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// SetWaitForTest swaps Transition's retry-backoff wait for f; call the
// returned restore (e.g. via t.Cleanup) to put the original back.
func SetWaitForTest(f func(context.Context, time.Duration) error) (restore func()) {
	old := wait
	wait = f
	return func() { wait = old }
}

// ConflictsTotalForTest reads the process-global counter's current value;
// callers must diff before/after since other tests share it.
func ConflictsTotalForTest() float64 { return testutil.ToFloat64(conflictsTotal) }

// OpsTotalForTest reads one op/outcome label pair's current value; callers
// must diff before/after since other tests share the counter.
func OpsTotalForTest(op, outcome string) float64 {
	return testutil.ToFloat64(opsTotal.WithLabelValues(op, outcome))
}
