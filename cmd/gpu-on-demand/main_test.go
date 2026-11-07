package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestWaitAndShutdownOnCancel drives the shutdown-coordination logic with fake
// shutdown funcs instead of real listeners, so it needs no sleep or network.
func TestWaitAndShutdownOnCancel(t *testing.T) {
	var aCalled, bCalled bool
	shutdownA := func(context.Context) error { aCalled = true; return nil }
	shutdownB := func(context.Context) error { bCalled = true; return nil }

	errs := make(chan error)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- waitAndShutdown(ctx, errs, shutdownA, shutdownB) }()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitAndShutdown returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitAndShutdown did not return after cancel")
	}

	if !aCalled || !bCalled {
		t.Fatalf("expected both shutdown funcs called, got a=%v b=%v", aCalled, bCalled)
	}
}

// TestWaitAndShutdownReturnsListenError proves a listen error short-circuits
// the wait instead of requiring ctx cancellation.
func TestWaitAndShutdownReturnsListenError(t *testing.T) {
	wantErr := errors.New("listen failed")
	shutdownA := func(context.Context) error { return nil }
	shutdownB := func(context.Context) error { return nil }

	errs := make(chan error, 1)
	errs <- wantErr
	ctx := context.Background()

	done := make(chan error, 1)
	go func() { done <- waitAndShutdown(ctx, errs, shutdownA, shutdownB) }()

	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("got error %v, want %v", err, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitAndShutdown did not return on listen error")
	}
}

// TestWaitAndShutdownWaitsForInFlightWork proves waitAndShutdown waits for a
// blocked shutdown func (standing in for Shutdown draining a request) before
// returning, and that its context has a live deadline, not the cancelled parent.
func TestWaitAndShutdownWaitsForInFlightWork(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	shutdownA := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			t.Errorf("shutdown context already done at call time: %v", err)
		}
		close(started)
		<-release
		return nil
	}
	shutdownB := func(context.Context) error { return nil }

	errs := make(chan error)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- waitAndShutdown(ctx, errs, shutdownA, shutdownB) }()

	cancel()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdownA was never invoked")
	}

	// shutdownA is blocked on release, so wg.Wait() cannot finish yet: this is
	// a structural guarantee, not a scheduling race, since the code cannot
	// reach "return nil" while shutdownA has not returned.
	select {
	case <-done:
		t.Fatal("waitAndShutdown returned before in-flight shutdown finished")
	default:
	}

	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitAndShutdown returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitAndShutdown did not return after release")
	}
}
