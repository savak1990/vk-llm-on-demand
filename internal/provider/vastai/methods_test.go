package vastai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

func serveFile(t *testing.T, path string) http.HandlerFunc {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

func TestSearchFixture(t *testing.T) { // AC1-style: price cap and denylist applied locally
	srv := httptest.NewServer(serveFile(t, "testdata/offer_search.json"))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, srv, c)

	f := provider.OfferFilter{MaxHourly: 800000, ExcludeMachineIDs: []int64{300}}
	offers, err := cl.SearchOffers(context.Background(), f)
	if err != nil {
		t.Fatalf("SearchOffers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("len(offers) = %d, want 1 (price cap and denylist applied): %+v", len(offers), offers)
	}
	if offers[0].ID != 1 || offers[0].MachineID != 100 {
		t.Fatalf("unexpected offer: %+v", offers[0])
	}
}

func TestGetFixture(t *testing.T) { // AC14
	srv := httptest.NewServer(serveFile(t, "testdata/instance_get.json"))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, srv, c)

	inst, err := cl.GetInstance(context.Background(), 555)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if inst.Status != provider.StatusRunning {
		t.Fatalf("Status = %v, want StatusRunning", inst.Status)
	}
	if inst.SSHPort != 41022 {
		t.Fatalf("SSHPort = %d, want 41022", inst.SSHPort)
	}
	want := time.Unix(1700000000, 0)
	if !inst.StartedAt.Equal(want) {
		t.Fatalf("StartedAt = %v, want %v", inst.StartedAt, want)
	}
}

func TestListPagination(t *testing.T) { // AC10, no failure: 12 instances match the prefix
	pages := map[string]string{
		"":      "testdata/instances_page1.json",
		"page2": "testdata/instances_page2.json",
		"page3": "testdata/instances_page3.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		body, err := os.ReadFile(pages[cursor])
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, srv, c)

	instances, err := cl.ListInstances(context.Background(), "vkllm:test:")
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(instances) != 12 {
		t.Fatalf("len(instances) = %d, want 12", len(instances))
	}
}

func TestListPageFailure(t *testing.T) { // AC10, with failure: error, nil slice
	page1, err := os.ReadFile("testdata/instances_page1.json")
	if err != nil {
		t.Fatal(err)
	}
	page2Attempts := 0
	reqs := make(chan struct{}, 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		if cursor == "" {
			reqs <- struct{}{}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(page1)
			return
		}
		page2Attempts++
		reqs <- struct{}{}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, srv, c)

	done := make(chan error, 1)
	var result []provider.Instance
	go func() {
		var err error
		result, err = cl.ListInstances(context.Background(), "vkllm:test:")
		done <- err
	}()

	<-reqs // page 1
	for i := 0; i < 5; i++ {
		<-reqs // page 2 attempt
		c.BlockUntil(1)
		c.Advance(60 * time.Second)
	}
	<-reqs // page 2, 6th and final attempt

	err = <-done
	if err == nil {
		t.Fatal("expected error from exhausted page-2 retries")
	}
	if result != nil {
		t.Fatalf("result = %v, want nil on failure", result)
	}
	if page2Attempts != 6 {
		t.Fatalf("page2Attempts = %d, want 6", page2Attempts)
	}
}

func TestDestroyNotFound(t *testing.T) { // AC11
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, srv, c)

	if err := cl.DestroyInstance(context.Background(), 1); err != nil {
		t.Fatalf("DestroyInstance: %v", err)
	}
	_, err := cl.GetInstance(context.Background(), 1)
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("GetInstance err = %v, want ErrNotFound", err)
	}
}

func TestBalanceFixture(t *testing.T) {
	srv := httptest.NewServer(serveFile(t, "testdata/balance.json"))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, srv, c)

	bal, err := cl.Balance(context.Background())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 42500000 {
		t.Fatalf("Balance = %d, want 42500000 (42.5 USD in micro-USD)", bal)
	}
}
