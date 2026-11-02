package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"time"
)

const maxTransitionAttempts = 8

var wait = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Transition loads, applies fn, and saves, retrying only on ErrConflict.
func Transition(ctx context.Context, s Store, fn func(*Document) error) (Document, error) {
	firstGen := int64(-1)
	for attempt := 1; attempt <= maxTransitionAttempts; attempt++ {
		doc, v, err := s.Load(ctx)
		if err != nil {
			return Document{}, err
		}
		if firstGen == -1 {
			firstGen = doc.Generation
		} else if doc.Generation < firstGen {
			RecordOp("transition", "reset")
			return Document{}, ErrStoreReset
		}

		before, err := Encode(doc)
		if err != nil {
			return Document{}, err
		}
		if err := fn(&doc); err != nil {
			return Document{}, err
		}
		after, err := Encode(doc)
		if err != nil {
			return Document{}, err
		}
		if bytes.Equal(before, after) {
			return doc, nil
		}

		if _, err := s.Save(ctx, doc, v); err == nil {
			RecordOp("transition", "ok")
			return doc, nil
		} else if !errors.Is(err, ErrConflict) {
			return Document{}, err
		}

		conflictsTotal.Inc()
		if attempt == maxTransitionAttempts {
			RecordOp("transition", "exhausted")
			return Document{}, ErrRetriesExhausted
		}
		jitter, err := rand.Int(rand.Reader, big.NewInt(int64(90*time.Millisecond)))
		if err != nil {
			return Document{}, err
		}
		backoff := 10*time.Millisecond + time.Duration(jitter.Int64())
		if err := wait(ctx, backoff); err != nil {
			return Document{}, err
		}
	}
	return Document{}, ErrRetriesExhausted
}
