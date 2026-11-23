package controller

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/budget"
	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/modelsize"
	"github.com/savak1990/vk-llm-on-demand/internal/obs"
	"github.com/savak1990/vk-llm-on-demand/internal/offercost"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	fakeprovider "github.com/savak1990/vk-llm-on-demand/internal/provider/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	fakestore "github.com/savak1990/vk-llm-on-demand/internal/store/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

func newTestController(t *testing.T) (*Controller, *fakeprovider.Fake, *fakeclock.Clock, Budget, *fakeWorker) {
	t.Helper()
	clk := fakeclock.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	prov := fakeprovider.New(clk)
	bgt := budget.New(budget.Config{MonthlyUSD: 30, MaxHourlyPriceUSD: 0.80})
	worker := newFakeWorker()
	c, err := New(Deps{
		Provider: prov, Store: fakestore.New("iid"), Dialer: &recordDialer{}, Clock: clk, Budget: bgt, Worker: worker,
		WorkerSeed:     func() ([]byte, error) { return []byte("seed"), nil },
		InstallationID: "iid", ModelAlias: "qwen3.6-35b-a3b",
		ModelSize: modelsize.Fixed(1_000_000_000), ModelURL: "https://example.invalid/model.gguf",
		Config: config.Controller{ProvisioningMode: "explicit", MaxHourlyPriceUSD: 0.80, MaxProvisionAttemptsPerHour: 3, IdleShutdownEnabled: true, IdleShutdownTimeout: 30 * time.Minute, ReconcileInterval: 60 * time.Second, MaxDiskGB: 200},
		Reg:    obs.NewRegistry(), // publishCostMetrics calls obs.Set*, which need obs's singleton registered, same as production wiring
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.lastBalance, c.lastBalanceKnown = provider.Money(100_000_000), true
	return c, prov, clk, bgt, worker
}

func recvEvent(t *testing.T, c *Controller) (Event, bool) {
	t.Helper()
	select {
	case e := <-c.events:
		return e, true
	case <-time.After(150 * time.Millisecond):
		return Event{}, false
	}
}

func TestDispatchSearchFindsOffer(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	dispatchSearch(context.Background(), c, effSearch{filter: provider.OfferFilter{MaxHourly: 800000}})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvOfferChosen || ev.Offer.ID != 11 {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestDispatchSearchNoOffer(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	dispatchSearch(context.Background(), c, effSearch{})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvNoOffer || ev.Reason != "no_offer" {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestDispatchSearchProviderErrorSetsErr(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.FailNext("search", 1, context.DeadlineExceeded)
	dispatchSearch(context.Background(), c, effSearch{})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvNoOffer || ev.Err == nil {
		t.Fatalf("event = %+v, ok=%v, want EvNoOffer with Err set", ev, ok)
	}
}

func TestDispatchCreateSuccess(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	dispatchCreate(context.Background(), c, effCreate{
		offer: provider.Offer{ID: 11, Hourly: 600000}, breakdown: offercost.Breakdown{DiskGB: 60},
		filter: provider.OfferFilter{MaxHourly: 800000}, gen: 1, label: "vkllm:iid:1",
	})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvCreated || ev.Gen != 1 {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestDispatchCreateRefusesDiskBelowModel(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	dispatchCreate(context.Background(), c, effCreate{
		offer: provider.Offer{ID: 11, Hourly: 600000}, breakdown: offercost.Breakdown{DiskGB: 0},
		filter: provider.OfferFilter{MaxHourly: 800000}, gen: 1, label: "vkllm:iid:1",
	})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvCreateFailed || ev.Class != provider.Fatal {
		t.Fatalf("event = %+v, ok=%v; want fatal EvCreateFailed", ev, ok)
	}
	if prov.Calls("create") != 0 {
		t.Fatalf("Calls(create) = %d, want 0", prov.Calls("create"))
	}
}

func TestDispatchCreatePriceOverCapNeverCallsProvider(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	dispatchCreate(context.Background(), c, effCreate{offer: provider.Offer{ID: 11, Hourly: 900000}, filter: provider.OfferFilter{MaxHourly: 800000}, gen: 1, label: "vkllm:iid:1"}) // $0.90 > $0.80 cap
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvCreateFailed || ev.Err == nil {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
	if prov.Calls("create") != 0 {
		t.Fatalf("Calls(create) = %d, want 0", prov.Calls("create"))
	}
}

func TestDispatchGetObservesRunningWithSSH(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:1", Status: provider.StatusOther})
	// provider/fake promotes StatusOther -> StatusRunning with a fake ssh host on the first Get.
	dispatchGet(context.Background(), c, effGet{id: 9, gen: 1})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvProviderObserved || ev.Status != provider.StatusRunning || ev.SSHHost == "" {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestDispatchGetFailureReadPostsNothing(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.FailNext("get", 1, context.DeadlineExceeded)
	dispatchGet(context.Background(), c, effGet{id: 9, gen: 1})
	if _, ok := recvEvent(t, c); ok {
		t.Fatal("want no event on a failed read")
	}
}

func TestDispatchDialPlainFailurePostsNothing(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	// Nothing listens on :1, so this only proves a plain dial failure posts
	// no event; the success path needs a live listener and is covered
	// elsewhere.
	c.deps.Dialer = tunnel.NewDirect("127.0.0.1:1")
	dispatchDial(context.Background(), c, effDial{inst: tunnel.Instance{Host: "127.0.0.1", Port: 1}, gen: 1})
	if _, ok := recvEvent(t, c); ok {
		t.Fatal("want no event on a plain dial failure (retries on next poll)")
	}
}

type dialerFunc func(context.Context, tunnel.Instance) (*tunnel.Conn, error)

func (f dialerFunc) Dial(ctx context.Context, in tunnel.Instance) (*tunnel.Conn, error) {
	return f(ctx, in)
}

func closingDialer(closed chan struct{}) tunnel.Dialer {
	return dialerFunc(func(context.Context, tunnel.Instance) (*tunnel.Conn, error) {
		return tunnelConnForTest(func() error { closed <- struct{}{}; return nil }), nil
	})
}

func TestDispatchDialDiscardsStaleConn(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	closed := make(chan struct{}, 1)
	c.deps.Dialer = closingDialer(closed)
	c.generation = 3
	c.state = Destroying // the state already moved on by the time the dial returns
	dispatchDial(context.Background(), c, effDial{gen: 3})
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("a dial that landed after the state moved on was never closed")
	}
	if c.conn != nil {
		t.Fatalf("conn = %v, want nil", c.conn)
	}
	if _, ok := recvEvent(t, c); ok {
		t.Fatal("want no EvLinkUp for a stale dial")
	}
}

func TestDispatchDialDiscardsDuplicateConn(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	first := tunnelConnForTest(func() error { return nil })
	c.conn = first
	closed := make(chan struct{}, 1)
	c.deps.Dialer = closingDialer(closed)
	c.generation = 3
	c.state = Booting
	dispatchDial(context.Background(), c, effDial{gen: 3})
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("a duplicate dial's Conn was never closed")
	}
	if c.conn != first {
		t.Fatal("dispatchDial overwrote the already-installed Conn")
	}
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvLinkUp || ev.HostKey != first.HostKey() {
		t.Fatalf("event = %+v, ok=%v, want EvLinkUp against the already-installed Conn (so a dropped first EvLinkUp self-heals here)", ev, ok)
	}
}

func TestDispatchProbeReadyPostsHealthOK(t *testing.T) {
	c, _, _, _, worker := newTestController(t)
	c.conn = tunnel.NewConn(nil, func() error { return nil }, "fp")
	worker.SetProbeSequence(ProbeReady)
	dispatchProbe(context.Background(), c, effProbe{gen: 1})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvHealthOK || ev.Gen != 1 {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestDispatchProbeLoadingPostsNothing(t *testing.T) {
	c, _, _, _, worker := newTestController(t)
	c.conn = tunnel.NewConn(nil, func() error { return nil }, "fp")
	worker.SetProbeSequence(ProbeLoading)
	dispatchProbe(context.Background(), c, effProbe{gen: 1})
	if _, ok := recvEvent(t, c); ok {
		t.Fatal("want no event while still Loading")
	}
}

func TestNextCreateAllowedAt(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if got := nextCreateAllowedAt(nil); !got.IsZero() {
		t.Fatalf("no ops: got %v, want zero", got)
	}
	f1 := now
	ops := []store.Op{{Key: "create-1", FinishedAt: &f1, Outcome: "provider_error"}}
	if got := nextCreateAllowedAt(ops); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("1 failed op: got %v, want %v", got, now.Add(time.Minute))
	}
	f2 := now.Add(time.Hour)
	ops = append(ops, store.Op{Key: "create-2", FinishedAt: &f2, Outcome: "provider_error"})
	ops = append(ops, store.Op{Key: "create-3", FinishedAt: &f2, Outcome: "provider_error"})
	if got := nextCreateAllowedAt(ops); !got.Equal(f2.Add(4 * time.Minute)) {
		t.Fatalf("3 failed ops: got %v, want %v", got, f2.Add(4*time.Minute))
	}
	f3 := now.Add(2 * time.Hour)
	ops = append(ops, store.Op{Key: "create-4", FinishedAt: &f3, Outcome: "ready"})
	if got := nextCreateAllowedAt(ops); !got.IsZero() {
		t.Fatalf("ready resets: got %v, want zero", got)
	}
}

type errSize struct{}

func (errSize) Bytes(context.Context, string) (int64, error) { return 0, errors.New("no size") }

func TestDispatchSearchModelSizeUnknownRetriesWithoutSearch(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.deps.ModelSize = errSize{}
	var buf bytes.Buffer
	c.deps.Log = slog.New(slog.NewTextHandler(&buf, nil))
	prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	dispatchSearch(context.Background(), c, effSearch{filter: provider.OfferFilter{MaxHourly: 800000}})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvNoOffer || ev.Err == nil || ev.Reason != "provider_error" {
		t.Fatalf("event = %+v, ok=%v; want EvNoOffer provider_error with Err", ev, ok)
	}
	if prov.Calls("search") != 0 {
		t.Fatalf("Calls(search) = %d, want 0", prov.Calls("search"))
	}
	if !strings.Contains(buf.String(), "reason=model_size_unknown") {
		t.Fatalf("log = %q, want reason=model_size_unknown", buf.String())
	}
}

func TestDispatchSearchModelTooLargeStopsWithoutSearch(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.deps.ModelSize = modelsize.Fixed(500_000_000_000) // needs 590 GB > 200
	var buf bytes.Buffer
	c.deps.Log = slog.New(slog.NewTextHandler(&buf, nil))
	dispatchSearch(context.Background(), c, effSearch{filter: provider.OfferFilter{MaxHourly: 800000}})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvNoOffer || ev.Err != nil || ev.Reason != "no_offer" {
		t.Fatalf("event = %+v, ok=%v; want EvNoOffer no_offer without Err", ev, ok)
	}
	if prov.Calls("search") != 0 {
		t.Fatalf("Calls(search) = %d, want 0", prov.Calls("search"))
	}
	if !strings.Contains(buf.String(), "reason=model_too_large") {
		t.Fatalf("log = %q, want reason=model_too_large", buf.String())
	}
}

func TestDispatchSearchRanksBySessionCostAndSizesDisk(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.deps.ModelSize = modelsize.Fixed(29_200_000_000)
	prov.SetOffers(
		provider.Offer{ID: 1, MachineID: 40508, Hourly: 615556, DphBase: 560000, DownloadPerGB: 39063, InetDownMbps: 889.4},
		provider.Offer{ID: 2, MachineID: 117871, Hourly: 650000, DphBase: 630000, DownloadPerGB: 10000, InetDownMbps: 1000},
	)
	f := provider.OfferFilter{MaxHourly: 800000, MaxDownloadPerGB: 100000, MaxStoragePerGBMonth: 1000000}
	dispatchSearch(context.Background(), c, effSearch{filter: f})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvOfferChosen || ev.Offer.MachineID != 117871 {
		t.Fatalf("event = %+v, ok=%v; want machine 117871 (cheaper session, dearer GPU)", ev, ok)
	}
	if ev.Breakdown.DiskGB != 49 || ev.Filter.MinDiskGB != 49 || ev.Filter.AllocatedStorageGB != 49 {
		t.Fatalf("disk = %d / filter %d / allocated %d, want 49 (minDiskGB 0 in this config)", ev.Breakdown.DiskGB, ev.Filter.MinDiskGB, ev.Filter.AllocatedStorageGB)
	}
	if ev.Breakdown.HorizonSeconds != 1800 {
		t.Fatalf("horizon = %d, want the 30m idle timeout", ev.Breakdown.HorizonSeconds)
	}
}

func TestDispatchSearchFiltersOfferAboveInstanceSpendCap(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.deps.Config.MaxInstanceSpendUSD = 0.10
	prov.SetOffers(
		provider.Offer{ID: 1, Hourly: 100000, DownloadPerGB: 100000}, // cheaper session, download alone == $0.10 cap
		provider.Offer{ID: 2, Hourly: 600000},
	)
	f := provider.OfferFilter{MaxHourly: 800000, MaxDownloadPerGB: 200000, MaxStoragePerGBMonth: 1000000}
	dispatchSearch(context.Background(), c, effSearch{filter: f})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvOfferChosen || ev.Offer.ID != 2 {
		t.Fatalf("event = %+v, ok=%v; want offer 2 (offer 1's download alone reaches maxInstanceSpendUSD)", ev, ok)
	}
}

func TestDispatchSearchAllOffersAboveInstanceSpendCapNoOffer(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.deps.Config.MaxInstanceSpendUSD = 0.10
	prov.SetOffers(provider.Offer{ID: 1, Hourly: 100000, DownloadPerGB: 100000})
	f := provider.OfferFilter{MaxHourly: 800000, MaxDownloadPerGB: 200000, MaxStoragePerGBMonth: 1000000}
	dispatchSearch(context.Background(), c, effSearch{filter: f})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvNoOffer || ev.Reason != "no_offer" {
		t.Fatalf("event = %+v, ok=%v; want EvNoOffer no_offer when every offer's download alone reaches the cap", ev, ok)
	}
}

func TestDispatchSearchInstanceSpendCapZeroDisablesFilter(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.SetOffers(
		provider.Offer{ID: 1, Hourly: 100000, DownloadPerGB: 100000},
		provider.Offer{ID: 2, Hourly: 600000},
	)
	f := provider.OfferFilter{MaxHourly: 800000, MaxDownloadPerGB: 200000, MaxStoragePerGBMonth: 1000000}
	dispatchSearch(context.Background(), c, effSearch{filter: f})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvOfferChosen || ev.Offer.ID != 1 {
		t.Fatalf("event = %+v, ok=%v; want offer 1 (cheaper session, maxInstanceSpendUSD is off)", ev, ok)
	}
}

func TestDispatchCreateRefusesOfferOutsideFilter(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	offer := provider.Offer{ID: 11, Hourly: 500000, DownloadPerGB: 200000} // $0.20/GB > $0.10 cap
	filter := provider.OfferFilter{MaxHourly: 800000, MaxDownloadPerGB: 100000, MaxStoragePerGBMonth: 1000000}
	dispatchCreate(context.Background(), c, effCreate{offer: offer, filter: filter, gen: 1, label: "vkllm:iid:1"})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvCreateFailed || ev.Class != provider.Fatal {
		t.Fatalf("event = %+v, ok=%v; want fatal EvCreateFailed", ev, ok)
	}
	if prov.Calls("create") != 0 {
		t.Fatalf("Calls(create) = %d, want 0", prov.Calls("create"))
	}
}

func TestGateRefusesOfferWhoseDownloadExceedsRemainingBudget(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	c.deps.Budget = budget.New(budget.Config{MonthlyUSD: 30, MaxMonthlyUSD: 100, MaxHourlyPriceUSD: 0.80})
	doc, _ := store.NewDocument("iid")
	doc.Ledger = store.Ledger{Month: clk.Now().UTC().Format("2006-01"), TotalUSD: 29}
	over := Event{Kind: EvOfferChosen, Breakdown: offercost.Breakdown{DownloadUSD: 1_140_640}}
	if r := c.gateReason(Searching, over, doc, clk.Now()); r != "budget_exhausted" {
		t.Fatalf("gateReason = %q, want budget_exhausted ($29 spent + $1.14 download >= $30)", r)
	}
	under := Event{Kind: EvOfferChosen, Breakdown: offercost.Breakdown{DownloadUSD: 500_000}}
	if r := c.gateReason(Searching, under, doc, clk.Now()); r != "" {
		t.Fatalf("gateReason = %q, want admit ($29.50 < $30)", r)
	}
}
