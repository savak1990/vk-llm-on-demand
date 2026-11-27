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

	mu          sync.Mutex
	nextID      int64
	instances   []instanceEntry
	lastPutBody map[string]any

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
		var full map[string]any
		_ = json.Unmarshal(b, &full)

		s.mu.Lock()
		s.nextID++
		s.instances = append(s.instances, instanceEntry{ID: s.nextID, Label: body.Label})
		s.lastPutBody = full
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

		resp := map[string]any{"instances": out, "next_token": ""}
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

	done := make(chan struct {
		id  provider.InstanceID
		err error
	}, 1)
	go func() {
		id, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
			OfferID: 1, Image: "img", Label: "vkllm:test:ambiguous",
		})
		done <- struct {
			id  provider.InstanceID
			err error
		}{id, err}
	}()
	c.BlockUntil(1)
	c.Advance(2 * time.Second)
	res := <-done
	id, err := res.id, res.err
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

	done := make(chan struct {
		id  provider.InstanceID
		err error
	}, 1)
	go func() {
		id, err := cl.CreateInstance(context.Background(), req)
		done <- struct {
			id  provider.InstanceID
			err error
		}{id, err}
	}()
	c.BlockUntil(1)
	c.Advance(2 * time.Second)
	res := <-done
	id1, err1 := res.id, res.err
	if !errors.Is(err1, provider.ErrOutcomeUnknown) {
		t.Fatalf("call 1 err = %v, want ErrOutcomeUnknown", err1)
	}
	if id1 != 0 {
		t.Fatalf("call 1 id = %d, want 0", id1)
	}
	// Only do()'s own 504 handling increments retryable; CreateInstance's
	// still-ambiguous give-up path must not double-count the same attempt.
	if got := counterValue(t, reg, "create", "retryable"); got != 1 {
		t.Fatalf("create/retryable after call 1 = %v, want 1 (from do()'s 504 handling only)", got)
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

// TestCreateDuplicateLabelAfterAmbiguous exercises adoptByLabel's second
// call, the one inside the post-504 ambiguous branch: a pre-PUT list finds
// nothing, the PUT itself is ambiguous (504), and the follow-up list finds
// two instances already sharing the label — the race this task guards
// against, not just the pre-PUT duplicate TestCreateDuplicateLabel covers.
func TestCreateDuplicateLabelAfterAmbiguous(t *testing.T) {
	var putCount, listCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			n := atomic.AddInt32(&listCount, 1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				_, _ = w.Write([]byte(`{"instances":[],"next_token":""}`))
				return
			}
			_, _ = w.Write([]byte(`{"instances":[` +
				`{"id":1,"label":"vkllm:test:dup-ambiguous","actual_status":"running"},` +
				`{"id":2,"label":"vkllm:test:dup-ambiguous","actual_status":"running"}` +
				`],"next_token":""}`))
		case http.MethodPut:
			atomic.AddInt32(&putCount, 1)
			w.WriteHeader(http.StatusGatewayTimeout)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, reg := newTestClient(t, srv, c)

	done := make(chan error, 1)
	go func() {
		_, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
			OfferID: 1, Image: "img", Label: "vkllm:test:dup-ambiguous",
		})
		done <- err
	}()
	c.BlockUntil(1)
	c.Advance(2 * time.Second)
	err := <-done
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != provider.Terminal {
		t.Fatalf("err = %v, want *provider.Error{Class: Terminal}", err)
	}
	if !errors.Is(err, provider.ErrDuplicateLabel) {
		t.Fatalf("err = %v, want it to wrap ErrDuplicateLabel", err)
	}
	if got := atomic.LoadInt32(&putCount); got != 1 {
		t.Fatalf("putCount = %d, want 1 (only one PUT ever sent)", got)
	}
	if got := counterValue(t, reg, "create", "terminal"); got != 1 {
		t.Fatalf("create/terminal = %v, want 1", got)
	}
	if got := counterValue(t, reg, "create", "adopted"); got != 0 {
		t.Fatalf("create/adopted = %v, want 0", got)
	}
	if got := counterValue(t, reg, "create", "retryable"); got != 1 {
		t.Fatalf("create/retryable = %v, want 1 (from do()'s 504 handling, not from the duplicate outcome)", got)
	}
}

// TestCreateListFailureAfterAmbiguousIsOutcomeUnknown covers R7a: when the
// follow-up list after an ambiguous PUT itself fails (as opposed to finding
// 0 or 2+ instances), the caller must see ErrOutcomeUnknown to know retrying
// with the same label is the right move.
func TestCreateListFailureAfterAmbiguousIsOutcomeUnknown(t *testing.T) {
	var putCount, listCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			n := atomic.AddInt32(&listCount, 1)
			if n == 1 {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"instances":[],"next_token":""}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
		case http.MethodPut:
			atomic.AddInt32(&putCount, 1)
			w.WriteHeader(http.StatusGatewayTimeout)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, srv, c)

	done := make(chan error, 1)
	go func() {
		_, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
			OfferID: 1, Image: "img", Label: "vkllm:test:list-fails-after-ambiguous",
		})
		done <- err
	}()
	c.BlockUntil(1)
	c.Advance(2 * time.Second)
	err := <-done

	if !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("err = %v, want it to wrap ErrOutcomeUnknown", err)
	}
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != provider.Retryable {
		t.Fatalf("err = %v, want *provider.Error{Class: Retryable}", err)
	}
	if got := atomic.LoadInt32(&putCount); got != 1 {
		t.Fatalf("putCount = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&listCount); got != 2 {
		t.Fatalf("listCount = %d, want 2 (pre-PUT list, then the failed follow-up list)", got)
	}
}

func TestCreateTerminalFailure(t *testing.T) { // AC8
	var putCount, listCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			atomic.AddInt32(&listCount, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"instances":[],"next_token":""}`))
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
			_, _ = w.Write([]byte(`{"instances":[],"next_token":""}`))
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
			_, _ = w.Write([]byte(`{"instances":[],"next_token":""}`))
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

func TestCreateVolumeInfo(t *testing.T) {
	s := newCreateTestServer(0)
	defer s.srv.Close()
	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, s.srv, c)

	done := make(chan error, 1)
	go func() {
		_, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
			OfferID: 1, Image: "img", Label: "vkllm:test:volume-create",
			Volume: &provider.VolumeAttach{CreateNew: true, ID: 55800361, SizeGB: 34, MountPath: "/models", Name: "vkllm_abc_v11"},
		})
		done <- err
	}()
	c.BlockUntil(1)
	c.Advance(2 * time.Second)
	if err := <-done; err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	s.mu.Lock()
	body := s.lastPutBody
	s.mu.Unlock()
	vi, _ := body["volume_info"].(map[string]any)
	if vi["create_new"] != true || vi["volume_id"] != float64(55800361) || vi["size"] != float64(34) ||
		vi["mount_path"] != "/models" || vi["name"] != "vkllm_abc_v11" {
		t.Fatalf("volume_info = %v", vi)
	}
}

func TestCreateVolumeInfoLinkExisting(t *testing.T) {
	s := newCreateTestServer(0)
	defer s.srv.Close()
	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, s.srv, c)

	done := make(chan error, 1)
	go func() {
		_, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
			OfferID: 1, Image: "img", Label: "vkllm:test:volume-link",
			Volume: &provider.VolumeAttach{CreateNew: false, ID: 62762324, MountPath: "/models"},
		})
		done <- err
	}()
	c.BlockUntil(1)
	c.Advance(2 * time.Second)
	if err := <-done; err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	s.mu.Lock()
	body := s.lastPutBody
	s.mu.Unlock()
	vi, _ := body["volume_info"].(map[string]any)
	if _, hasSize := vi["size"]; hasSize {
		t.Fatalf("volume_info = %v, want no size key for a link attach", vi)
	}
	if vi["create_new"] != false || vi["volume_id"] != float64(62762324) {
		t.Fatalf("volume_info = %v", vi)
	}
}

func TestCreateNoVolumeInfo(t *testing.T) {
	s := newCreateTestServer(0)
	defer s.srv.Close()
	c := fakeclock.New(time.Unix(0, 0))
	cl, _ := newTestClient(t, s.srv, c)

	done := make(chan error, 1)
	go func() {
		_, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
			OfferID: 1, Image: "img", Label: "vkllm:test:no-volume",
		})
		done <- err
	}()
	c.BlockUntil(1)
	c.Advance(2 * time.Second)
	if err := <-done; err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	s.mu.Lock()
	body := s.lastPutBody
	s.mu.Unlock()
	if _, ok := body["volume_info"]; ok {
		t.Fatalf("volume_info = %v, want no key when Volume is nil", body["volume_info"])
	}
}

func TestCreateRefusalCarriesVastReason(t *testing.T) {
	const body = `{"success":false,"error":"invalid_args","msg":"error 404/3603: no_such_ask  Instance type by id 60356224 is not available.","ask_id":60356224}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"instances":[],"next_token":""}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	buf := captureLogs(t)
	cl, _ := newTestClient(t, srv, fakeclock.New(time.Unix(0, 0)))
	cl.logger = buf.logger
	_, err := cl.CreateInstance(context.Background(), provider.CreateRequest{
		OfferID: 60356224, Image: "img", Label: "vkllm:test:refused",
		Env: map[string]string{"WORKER_API_KEY": "sk-vkllm-testworkerkey123"},
	})
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != provider.Terminal || pe.Status != 400 {
		t.Fatalf("err = %v, want a Terminal 400", err)
	}
	if !contains(err.Error(), "no_such_ask") || !contains(err.Error(), "not available") {
		t.Fatalf("err = %q, want the Vast reason", err)
	}
	out := buf.String()
	if !contains(out, "vast_error=invalid_args") || !contains(out, "vast_msg=") || !contains(out, "no_such_ask") {
		t.Fatalf("log lacks vast_error/vast_msg: %s", out)
	}
	for _, secret := range []string{"test-key-ABCDEF", "sk-vkllm-testworkerkey123", "Bearer"} {
		if contains(out, secret) || contains(err.Error(), secret) {
			t.Fatalf("%q leaked: err=%v log=%s", secret, err, out)
		}
	}
}

func TestCreateVastReasonTruncated(t *testing.T) {
	long := strings.Repeat("é", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"instances":[],"next_token":""}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"error":"e","msg":"` + long + `"}`))
	}))
	defer srv.Close()

	cl, _ := newTestClient(t, srv, fakeclock.New(time.Unix(0, 0)))
	_, err := cl.CreateInstance(context.Background(), provider.CreateRequest{OfferID: 1, Image: "img", Label: "vkllm:test:long"})
	if err == nil || !contains(err.Error(), strings.Repeat("é", 300)) || contains(err.Error(), strings.Repeat("é", 301)) {
		t.Fatalf("err = %v, want the msg cut to 300 characters", err)
	}
}
