package vastai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/obs/obstest"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type logCapture struct {
	buf    *bytes.Buffer
	logger *slog.Logger
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	buf := &bytes.Buffer{}
	return &logCapture{buf: buf, logger: slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
}

func (l *logCapture) String() string { return l.buf.String() }

func contains(s, substr string) bool { return strings.Contains(s, substr) }

func asProviderError(err error, target **provider.Error) bool {
	return errors.As(err, target)
}

func retryableClass() provider.Class { return provider.Retryable }

func newTestClient(t *testing.T, srv *httptest.Server, c *fakeclock.Clock) (*Client, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	cl := New(srv.URL, srv.Client(), func() (string, error) { return "test-key-ABCDEF", nil },
		c, rand.New(rand.NewSource(1)), noopLogger(), reg)
	return cl, reg
}

func getInstanceReq(srv *httptest.Server) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL+"/instances/1/", nil)
	}
}

func TestBackoff429(t *testing.T) { // AC3
	attempts := 0
	reqs := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		reqs <- struct{}{}
		if attempts <= 2 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte("API requests too frequent endpoint threshold=2"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instances":{"id":1,"actual_status":"running","ssh_port":41022,"start_date":1000}}`))
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, reg := newTestClient(t, srv, c)

	done := make(chan error, 1)
	go func() {
		_, err := cl.GetInstance(context.Background(), 1)
		done <- err
	}()
	// Each 429 triggers one sleep; wait for the request that produced it,
	// then wait for its retry sleep to register before advancing past it.
	for i := 0; i < 2; i++ {
		<-reqs
		c.BlockUntil(1)
		c.Advance(5 * time.Second)
	}
	<-reqs // the third, successful request
	if err := <-done; err != nil {
		t.Fatalf("GetInstance returned error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if got := counterValue(t, reg, "get", "ok"); got != 1 {
		t.Fatalf("get/ok = %v, want 1", got)
	}
	if got := rateLimitedValue(t, reg); got != 2 {
		t.Fatalf("rate_limited_total = %v, want 2", got)
	}
	// FR-5.1: the first retry's delay is in [1s, 2s), the second in [2s, 4s).
	if d := cl.backoff(0); d < time.Second || d >= 2*time.Second {
		t.Fatalf("backoff(0) = %v, want [1s, 2s)", d)
	}
	if d := cl.backoff(1); d < 2*time.Second || d >= 4*time.Second {
		t.Fatalf("backoff(1) = %v, want [2s, 4s)", d)
	}
}

func TestClassificationExhaustsRetries(t *testing.T) { // AC4
	attempts := 0
	reqs := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		reqs <- struct{}{}
		w.WriteHeader(429)
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, srv, c)

	done := make(chan error, 1)
	go func() {
		_, _, err := cl.do(context.Background(), "GetInstance", 5*time.Second, 5, getInstanceReq(srv))
		done <- err
	}()
	for i := 0; i < 5; i++ {
		<-reqs
		c.BlockUntil(1)
		c.Advance(60 * time.Second)
	}
	<-reqs // the sixth, final attempt that exhausts retries
	err := <-done
	if attempts != 6 {
		t.Fatalf("attempts = %d, want 6 (1 + 5 retries)", attempts)
	}
	var perr *provider.Error
	if !asProviderError(err, &perr) || perr.Class != retryableClass() {
		t.Fatalf("expected Retryable error, got %v", err)
	}
}

func TestKeyNotLogged(t *testing.T) { // AC12
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	buf := captureLogs(t)
	cl, _ := newTestClient(t, srv, c)
	cl.logger = buf.logger
	ctx := context.Background()

	calls := []struct {
		name string
		err  error
	}{
		{"search", firstErr(cl.SearchOffers(ctx, provider.OfferFilter{}))},
		{"create", firstErr(cl.CreateInstance(ctx, provider.CreateRequest{Image: "img", Label: "l"}))},
		{"get", firstErr(cl.GetInstance(ctx, 1))},
		{"list", firstErr(cl.ListInstances(ctx, ""))},
		{"destroy", cl.DestroyInstance(ctx, 1)},
		{"balance", firstErr(cl.Balance(ctx))},
	}
	for _, call := range calls {
		var pe *provider.Error
		if !errors.As(call.err, &pe) || pe.Class != provider.Fatal {
			t.Fatalf("%s: err = %v, want *provider.Error{Class: Fatal}", call.name, call.err)
		}
		if contains(call.err.Error(), "test-key-ABCDEF") {
			t.Fatalf("%s: error leaked the key: %v", call.name, call.err)
		}
	}

	out := buf.String()
	obstest.AssertNoSecret(t, []byte(out), "test-key-ABCDEF")
	if contains(out, "Bearer") {
		t.Fatalf("logs leaked the bearer header literal: %s", out)
	}
	if !contains(out, "level=DEBUG") {
		t.Fatalf("expected at least one debug-level attempt log, got: %s", out)
	}
	if !contains(out, "level=WARN") {
		t.Fatalf("expected at least one warn-level failure log, got: %s", out)
	}
}

func firstErr[T any](_ T, err error) error { return err }

func TestKeyReadPerRequest(t *testing.T) { // AC13
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"balance": 10.5}`))
	}))
	defer srv.Close()

	keys := []string{"k1", "k2"}
	i := 0
	reg := prometheus.NewRegistry()
	c := fakeclock.New(time.Unix(0, 0))
	cl := New(srv.URL, srv.Client(), func() (string, error) {
		k := keys[i]
		i++
		return k, nil
	}, c, rand.New(rand.NewSource(1)), noopLogger(), reg)

	balanceReq := func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, srv.URL+"/users/current/", nil)
	}
	if _, _, err := cl.do(context.Background(), "Balance", 5*time.Second, 0, balanceReq); err != nil {
		t.Fatalf("first do: %v", err)
	}
	if _, _, err := cl.do(context.Background(), "Balance", 5*time.Second, 0, balanceReq); err != nil {
		t.Fatalf("second do: %v", err)
	}
	if seen[0] != "Bearer k1" || seen[1] != "Bearer k2" {
		t.Fatalf("got %v, want [Bearer k1, Bearer k2]", seen)
	}
}

func TestSleepRemovesWaiterOnCancel(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := fakeclock.New(time.Unix(0, 0))
	cl := New("http://x", http.DefaultClient, func() (string, error) { return "k", nil },
		c, rand.New(rand.NewSource(1)), noopLogger(), reg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cl.sleep(ctx, time.Minute) }()
	c.BlockUntil(1)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected cancellation error")
	}
	// A second, independent sleep must see a fresh pending count, not a
	// leftover from the cancelled one above.
	done2 := make(chan error, 1)
	go func() { done2 <- cl.sleep(context.Background(), time.Minute) }()
	c.BlockUntil(1)
	c.Advance(time.Minute)
	if err := <-done2; err != nil {
		t.Fatalf("second sleep failed: %v", err)
	}
}
