package vastai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

// instanceEntry is one instance as the fake Vast.ai list endpoint reports it.
type instanceEntry struct {
	ID    int64
	Label string
}

// createTestServer fakes /api/v1/instances (GET) and /api/v0/asks/{id} (PUT):
// PUT always stores the instance server-side then returns 504, so tests can
// assert whether CreateInstance's follow-up list sees it. List responses
// are empty while listCount <= showAfter, and show every stored instance
// once listCount exceeds it — simulating list lag after an ambiguous PUT.
type createTestServer struct {
	srv *httptest.Server

	mu        sync.Mutex
	nextID    int64
	instances []instanceEntry

	putCount  int32
	listCount int32
	showAfter int32
}

func newCreateTestServer(showAfter int32) *createTestServer {
	s := &createTestServer{nextID: 1000, showAfter: showAfter}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *createTestServer) handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		atomic.AddInt32(&s.putCount, 1)
		var body struct {
			Label string `json:"label"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)

		s.mu.Lock()
		s.nextID++
		s.instances = append(s.instances, instanceEntry{ID: s.nextID, Label: body.Label})
		s.mu.Unlock()

		w.WriteHeader(http.StatusGatewayTimeout)
	case http.MethodGet:
		n := atomic.AddInt32(&s.listCount, 1)

		s.mu.Lock()
		var out []map[string]any
		if n > s.showAfter {
			for _, inst := range s.instances {
				out = append(out, map[string]any{
					"id": inst.ID, "label": inst.Label, "actual_status": "running",
				})
			}
		}
		s.mu.Unlock()

		resp := map[string]any{"instances": out, "next_cursor": ""}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestCreateAdoptBeforePut(t *testing.T) { // AC5
	s := newCreateTestServer(0) // every list shows what's there, which is nothing until seeded below
	defer s.srv.Close()
	s.mu.Lock()
	s.instances = append(s.instances, instanceEntry{ID: 777, Label: "vkllm:test:adopt"})
	s.mu.Unlock()

	c := fakeclock.New(time.Unix(0, 0))
	cl, reg := newTestClient(t, s.srv, c)

	id, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
		OfferID: 1, Image: "img", Label: "vkllm:test:adopt",
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if id != 777 {
		t.Fatalf("id = %d, want 777", id)
	}
	if got := atomic.LoadInt32(&s.putCount); got != 0 {
		t.Fatalf("putCount = %d, want 0", got)
	}
	if got := counterValue(t, reg, "create", "adopted"); got != 1 {
		t.Fatalf("create/adopted = %v, want 1", got)
	}
}

func TestCreateAdoptAfterAmbiguous(t *testing.T) { // AC6
	s := newCreateTestServer(1) // pre-PUT list (1) empty, post-504 list (2) shows it
	defer s.srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, reg := newTestClient(t, s.srv, c)

	id, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
		OfferID: 1, Image: "img", Label: "vkllm:test:ambiguous",
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if id == 0 {
		t.Fatalf("id = 0, want the adopted instance's id")
	}
	if got := atomic.LoadInt32(&s.putCount); got != 1 {
		t.Fatalf("putCount = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&s.listCount); got != 2 {
		t.Fatalf("listCount = %d, want 2", got)
	}
	if got := counterValue(t, reg, "create", "adopted"); got != 1 {
		t.Fatalf("create/adopted = %v, want 1", got)
	}
	// do()'s own 504 handling counts one retryable attempt on top of the
	// final adopted outcome; that is existing behavior, not double counting
	// the same outcome.
	if got := counterValue(t, reg, "create", "retryable"); got != 1 {
		t.Fatalf("create/retryable = %v, want 1 (from do()'s 504 handling)", got)
	}
}

func TestCreateOnePerLabel(t *testing.T) { // AC7
	s := newCreateTestServer(2) // lists 1 and 2 (call 1's pre-PUT and post-504) are empty; list 3+ shows it
	defer s.srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, reg := newTestClient(t, s.srv, c)

	req := provider.CreateRequest{OfferID: 1, Image: "img", Label: "vkllm:test:once"}

	id1, err1 := cl.CreateInstance(context.Background(), req)
	if !errors.Is(err1, provider.ErrOutcomeUnknown) {
		t.Fatalf("call 1 err = %v, want ErrOutcomeUnknown", err1)
	}
	if id1 != 0 {
		t.Fatalf("call 1 id = %d, want 0", id1)
	}
	if got := counterValue(t, reg, "create", "retryable"); got != 2 {
		t.Fatalf("create/retryable after call 1 = %v, want 2 (one from do()'s 504, one from the still-ambiguous outcome)", got)
	}

	var adoptedID provider.InstanceID
	for i := 2; i <= 10; i++ {
		id, err := cl.CreateInstance(context.Background(), req)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if adoptedID == 0 {
			adoptedID = id
		} else if id != adoptedID {
			t.Fatalf("call %d id = %d, want %d (same instance every time)", i, id, adoptedID)
		}
	}

	if got := atomic.LoadInt32(&s.putCount); got != 1 {
		t.Fatalf("putCount = %d, want 1 (only call 1 ever PUTs)", got)
	}
	s.mu.Lock()
	n := len(s.instances)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("server holds %d instances, want exactly 1", n)
	}
}

func TestCreateDuplicateLabel(t *testing.T) {
	s := newCreateTestServer(0)
	defer s.srv.Close()
	s.mu.Lock()
	s.instances = append(s.instances,
		instanceEntry{ID: 1, Label: "vkllm:test:dup"},
		instanceEntry{ID: 2, Label: "vkllm:test:dup"},
	)
	s.mu.Unlock()

	c := fakeclock.New(time.Unix(0, 0))
	cl, reg := newTestClient(t, s.srv, c)

	_, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
		OfferID: 1, Image: "img", Label: "vkllm:test:dup",
	})
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != provider.Terminal {
		t.Fatalf("err = %v, want *provider.Error{Class: Terminal}", err)
	}
	if !errors.Is(err, provider.ErrDuplicateLabel) {
		t.Fatalf("err = %v, want it to wrap ErrDuplicateLabel", err)
	}
	if got := atomic.LoadInt32(&s.putCount); got != 0 {
		t.Fatalf("putCount = %d, want 0", got)
	}
	if got := counterValue(t, reg, "create", "terminal"); got != 1 {
		t.Fatalf("create/terminal = %v, want 1", got)
	}
}

func TestCreateTerminalFailure(t *testing.T) { // AC8
	var putCount, listCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			atomic.AddInt32(&listCount, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"instances":[],"next_cursor":""}`))
		case http.MethodPut:
			atomic.AddInt32(&putCount, 1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"success":false}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, reg := newTestClient(t, srv, c)

	_, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
		OfferID: 1, Image: "img", Label: "vkllm:test:terminal",
	})
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != provider.Terminal {
		t.Fatalf("err = %v, want *provider.Error{Class: Terminal}", err)
	}
	if got := atomic.LoadInt32(&putCount); got != 1 {
		t.Fatalf("putCount = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&listCount); got != 1 {
		t.Fatalf("listCount = %d, want 1", got)
	}
	if got := counterValue(t, reg, "create", "terminal"); got != 1 {
		t.Fatalf("create/terminal = %v, want 1", got)
	}
}

func TestCreateSuccessZeroContractIsTerminal(t *testing.T) { // malformed 2xx: success:true but no real id
	var putCount, listCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			atomic.AddInt32(&listCount, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"instances":[],"next_cursor":""}`))
		case http.MethodPut:
			atomic.AddInt32(&putCount, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"new_contract":0}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, reg := newTestClient(t, srv, c)

	id, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
		OfferID: 1, Image: "img", Label: "vkllm:test:zerocontract",
	})
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != provider.Terminal {
		t.Fatalf("err = %v (id=%d), want *provider.Error{Class: Terminal}", err, id)
	}
	if got := counterValue(t, reg, "create", "terminal"); got != 1 {
		t.Fatalf("create/terminal = %v, want 1", got)
	}
}

func TestCreateOnstartTooLong(t *testing.T) { // AC9
	var reqCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&reqCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, srv, c)

	_, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
		OfferID: 1, Image: "img", Label: "vkllm:test:onstart",
		Onstart: strings.Repeat("x", 4049),
	})
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != provider.Terminal {
		t.Fatalf("err = %v, want *provider.Error{Class: Terminal}", err)
	}
	if got := atomic.LoadInt32(&reqCount); got != 0 {
		t.Fatalf("reqCount = %d, want 0 (the length check must short-circuit before any HTTP call)", got)
	}
}

func TestMetricsCreateOk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"instances":[],"next_cursor":""}`))
		case http.MethodPut:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"new_contract":4242}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, reg := newTestClient(t, srv, c)

	id, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
		OfferID: 1, Image: "img", Label: "vkllm:test:metrics",
	})
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	if id != 4242 {
		t.Fatalf("id = %d, want 4242", id)
	}
	if got := counterValue(t, reg, "create", "ok"); got != 1 {
		t.Fatalf("create/ok metric = %v, want 1", got)
	}
}
