package store

import (
	"context"
	"time"
)

// Ready returns a readiness check that fails if Load cannot reach the store within 2s.
func Ready(s Store) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _, err := s.Load(ctx)
		return err
	}
}
