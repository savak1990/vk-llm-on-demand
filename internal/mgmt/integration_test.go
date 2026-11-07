package mgmt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/obs"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	fakeprovider "github.com/savak1990/vk-llm-on-demand/internal/provider/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	fakestore "github.com/savak1990/vk-llm-on-demand/internal/store/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

// allowAllBudget and readyWorker are minimal fakes built only against
// the exported controller.Budget/controller.Worker interfaces, not a
// copy of internal/controller's own test fakes.
type allowAllBudget struct{}

func (allowAllBudget) Allow(store.Document, provider.Money, bool) string    { return "" }
func (allowAllBudget) AssertPrice(float64, float64) error                   { return nil }
func (allowAllBudget) OpenEntry(*store.Document, time.Time, int64, float64) {}
func (allowAllBudget) CloseEntry(*store.Document, time.Time, int64)         {}
func (allowAllBudget) Rollover(*store.Document, time.Time)                  {}
func (allowAllBudget) RefreshOpenEntry(*store.Document, time.Time)          {}
func (allowAllBudget) UsedRatio(store.Document) float64                     { return 0 }
func (allowAllBudget) EstimatedMTD(store.Document) float64                  { return 0 }

var _ controller.Budget = allowAllBudget{}

// readyWorker reports ProbeReady from the first probe on, so the saga
// reaches READY deterministically without a multi-step status sequence.
type readyWorker struct{}

func (readyWorker) WorkerKey([]byte, string, int64) string { return "ac6-key" }
func (readyWorker) BuildCreate(offer provider.Offer, label, workerKey string) provider.CreateRequest {
	return provider.CreateRequest{OfferID: offer.ID, Label: label, Env: map[string]string{"WORKER_KEY": workerKey}}
}
func (readyWorker) Probe(context.Context, http.RoundTripper, string, string) (controller.ProbeStatus, error) {
	return controller.ProbeReady, nil
}
func (readyWorker) Scrape(context.Context, http.RoundTripper) error { return nil }

var _ controller.Worker = readyWorker{}

type fakeDialer struct{}

func (fakeDialer) Dial(context.Context, tunnel.Instance) (*tunnel.Conn, error) {
	return tunnel.NewConn(nil, func() error { return nil }, "sha256:fake"), nil
}

// waitForReady advances the fake clock in small steps, giving the
// saga's own background goroutines real wall-clock turns between each
// step, and polls Status until it reports READY.
func waitForReady(t *testing.T, ctl *controller.Controller, clk *fakeclock.Clock) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ctl.Status().Public == "READY" {
			return
		}
		clk.Advance(time.Second)
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("controller never reached READY, last status: %+v", ctl.Status())
}

func TestAC6_RealControllerThroughMgmt(t *testing.T) {
	clk := fakeclock.New(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	prov := fakeprovider.New(clk)
	prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 710000, GPUName: "L40S"})

	ctl, err := controller.New(controller.Deps{
		Provider: prov, Store: fakestore.New("iid"), Dialer: fakeDialer{}, Clock: clk,
		Budget: allowAllBudget{}, Worker: readyWorker{},
		WorkerSeed:     func() ([]byte, error) { return []byte("seed"), nil },
		InstallationID: "iid", ModelAlias: "qwen3.6-35b-a3b",
		Config: config.Controller{
			ProvisioningMode: "explicit", MaxHourlyPriceUSD: 0.80,
			IdleShutdownEnabled: true, IdleShutdownTimeout: 1800 * time.Second,
			ReconcileInterval: 60 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("controller.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	keys := authKeys(t, "sk-vkllm-clientkey1", "sk-vkllm-mgmttoken1")
	reg := obs.NewRegistry()
	h := New(Deps{Controller: ctl, Keys: keys, Limiter: auth.NewLimiter(10000, clk), Clock: clk, Config: config.Mgmt{}, Reg: reg, Log: noopLogger()})
	go func() { _ = ctl.Run(ctx) }()

	doAuthed := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); doAuthed("POST", "/api/v1/gpu/start") }()
	}
	wg.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for prov.Calls("create") != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := prov.Calls("create"); got != 1 {
		t.Fatalf("Calls(create) = %d, want 1 (50 starts collapse to one controller.Start, which itself collapses to one create)", got)
	}

	waitForReady(t, ctl, clk)
	before := ctl.Status().IdleDeadline

	for i := 0; i < 10; i++ {
		doAuthed("POST", "/api/v1/gpu/start")
	}
	for i := 0; i < 100; i++ {
		doAuthed("GET", "/api/v1/gpu/status")
	}
	if after := ctl.Status().IdleDeadline; before == nil || after == nil || !before.Equal(*after) {
		t.Fatalf("idleDeadline moved across 110 calls: before=%v after=%v", before, after)
	}

	rec := doAuthed("POST", "/api/v1/gpu/stop")
	if rec.Code != 202 {
		t.Fatalf("stop: code = %d, want 202", rec.Code)
	}
}
