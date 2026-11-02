package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/store/fake"
)

func withInstantWait(t *testing.T) {
	restore := store.SetWaitForTest(func(context.Context, time.Duration) error { return nil })
	t.Cleanup(restore)
}

func TestTransitionRetriesConflict(t *testing.T) {
	withInstantWait(t)
	f := fake.New("4f2c1a9b0e77")
	f.ConflictNext(3)
	saves := 0
	doc, err := store.Transition(context.Background(), f, func(d *store.Document) error {
		saves++
		d.Generation++
		return nil
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if doc.Generation != 1 {
		t.Fatalf("Generation = %d, want 1", doc.Generation)
	}
	if saves != 4 {
		t.Fatalf("fn called %d times, want 4 (3 conflicts + 1 success)", saves)
	}
}

func TestTransitionExhausted(t *testing.T) {
	withInstantWait(t)
	f := fake.New("4f2c1a9b0e77")
	f.ConflictNext(8)
	_, err := store.Transition(context.Background(), f, func(d *store.Document) error {
		d.Generation++
		return nil
	})
	if !errors.Is(err, store.ErrRetriesExhausted) {
		t.Fatalf("Transition: want ErrRetriesExhausted, got %v", err)
	}
}

func TestTransitionFnErrorNoSave(t *testing.T) {
	f := fake.New("4f2c1a9b0e77")
	before := f.Raw()
	wantErr := errors.New("fn failed")
	_, err := store.Transition(context.Background(), f, func(*store.Document) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("Transition: want %v, got %v", wantErr, err)
	}
	if string(f.Raw()) != string(before) {
		t.Fatal("Save must not have been called")
	}
}

func TestTransitionNoopNoSave(t *testing.T) {
	f := fake.New("4f2c1a9b0e77")
	before := f.Raw()
	_, err := store.Transition(context.Background(), f, func(*store.Document) error { return nil })
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if string(f.Raw()) != string(before) {
		t.Fatal("Save must not have been called on a no-op fn")
	}
}

func TestTransitionCtxCancel(t *testing.T) {
	f := fake.New("4f2c1a9b0e77")
	f.ConflictNext(8)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.Transition(ctx, f, func(d *store.Document) error { d.Generation++; return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Transition: want context.Canceled, got %v", err)
	}
}

// TestTransitionStoreReset: fn must actually change the document (else the
// no-op short-circuit returns before Save is ever attempted). attempt 1's
// Save conflicts (fn's own Put bumped the version first); attempt 2 reloads
// a lower generation and resets before fn runs again.
func TestTransitionStoreReset(t *testing.T) {
	withInstantWait(t)
	f := fake.New("4f2c1a9b0e77")
	seeded, _ := store.NewDocument("4f2c1a9b0e77")
	seeded.Generation = 5
	f.Put(seeded)

	calls := 0
	_, err := store.Transition(context.Background(), f, func(d *store.Document) error {
		calls++
		d.Generation++
		fresh, _ := store.NewDocument("4f2c1a9b0e77")
		f.Put(fresh)
		return nil
	})
	if !errors.Is(err, store.ErrStoreReset) {
		t.Fatalf("Transition: want ErrStoreReset, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
}
