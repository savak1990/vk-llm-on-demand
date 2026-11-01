package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthzAlwaysOK(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthz(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

func TestReadyzOKWithNoChecksRegistered(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyz(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

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

// TestWaitAndShutdownWaitsForInFlightWork proves waitAndShutdown does not
// return while a shutdown func (standing in for http.Server.Shutdown draining
// an in-flight request) is still blocked, and returns promptly once it is released.
func TestWaitAndShutdownWaitsForInFlightWork(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	shutdownA := func(context.Context) error {
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

func TestRunShutsDownWithinDeadline(t *testing.T) {
	api := &http.Server{Addr: "127.0.0.1:0", Handler: http.NewServeMux()}
	health := &http.Server{Addr: "127.0.0.1:0", Handler: http.NewServeMux()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // servers never need to accept a connection for this to exercise run's shutdown path

	done := make(chan error, 1)
	go func() { done <- run(ctx, api, health) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return within 2s of cancel")
	}
}
