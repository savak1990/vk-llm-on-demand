//go:build integration

package store

import "github.com/prometheus/client_golang/prometheus/testutil"

// ConflictsTotalValue lets TestKindRace prove the conflict counter moved
// during a real two-writer race.
func ConflictsTotalValue() float64 { return testutil.ToFloat64(conflictsTotal) }
