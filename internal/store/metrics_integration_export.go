//go:build integration

package store

import "github.com/prometheus/client_golang/prometheus/testutil"

// ConflictsTotalValue exposes the conflicts counter for TestKindRace (AC12).
func ConflictsTotalValue() float64 { return testutil.ToFloat64(conflictsTotal) }
