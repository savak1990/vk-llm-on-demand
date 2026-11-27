package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

var rentalT0 = time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)

func rentalOffer() store.Offer {
	return store.Offer{
		MachineID: 42, GPU: "L40S", Geo: "Quebec, CA", DPH: 0.64, InetDown: 2400,
		Estimate: &store.OfferEstimate{ModelBytes: 28595763840, DownloadSeconds: 96},
	}
}

func TestChecksumMismatchOnHit(t *testing.T) {
	if !checksumMismatchOnHit("hit", map[string]time.Time{"download_begin": rentalT0}) {
		t.Fatal("a hit that downloads again is a mismatch")
	}
	if checksumMismatchOnHit("hit", map[string]time.Time{"download_skipped": rentalT0}) || checksumMismatchOnHit("seeded", map[string]time.Time{"download_begin": rentalT0}) {
		t.Fatal("no mismatch")
	}
}

// step runs one transition's mutate through withRental, as processEvent does.
func step(t *testing.T, d *store.Document, now time.Time, outcome string, mutate func(*store.Document)) rentalChange {
	t.Helper()
	var ch rentalChange
	fn := withRental(func(d *store.Document) error { mutate(d); return nil }, rentalInput{now: now, searchStart: now.Add(-2 * time.Second), outcome: outcome}, &ch)
	if err := fn(d); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	return ch
}

func TestRentalStagesFollowInstanceFields(t *testing.T) {
	d, _ := store.NewDocument("iid")
	d.StartOrigin = "demand"
	ch := step(t, &d, rentalT0, "", func(d *store.Document) {
		d.Generation = 1
		d.Instance = &store.Instance{State: Creating.String(), Offer: rentalOffer(), CreatedAt: rentalT0}
	})
	if ch != (rentalChange{"search", 2}) {
		t.Fatalf("open change = %+v, want search 2", ch)
	}
	steps := []struct {
		after  time.Duration
		mutate func(*store.Document)
		want   rentalChange
	}{
		{1 * time.Second, func(d *store.Document) { d.Instance.ProviderID = "7"; d.Instance.State = Booting.String() }, rentalChange{"create", 1}},
		{16 * time.Second, func(d *store.Document) { d.Instance.SSHHost = "203.0.113.9" }, rentalChange{"boot", 15}},
		{21 * time.Second, func(d *store.Document) { d.Instance.State = Loading.String() }, rentalChange{"link", 5}},
		{46 * time.Second, func(d *store.Document) {
			ready := rentalT0.Add(46 * time.Second)
			d.Instance.State, d.Instance.ReadyAt = Ready.String(), &ready
		}, rentalChange{"ready", 25}},
	}
	for _, s := range steps {
		if got := step(t, &d, rentalT0.Add(s.after), "", s.mutate); got != s.want {
			t.Fatalf("at +%v change = %+v, want %+v", s.after, got, s.want)
		}
	}
	r := d.Rentals[0]
	if r.Gen != 1 || r.Origin != "demand" || r.MachineID != 42 || r.Geo != "Quebec, CA" || r.ModelBytes != 28595763840 || r.EstDownloadSeconds != 96 {
		t.Fatalf("offer fields = %+v", r)
	}
	if r.Outcome != "ready" || r.ReadyAt == nil || r.MarkAt != nil {
		t.Fatalf("outcome %q readyAt %v markAt %v, want ready, set, nil", r.Outcome, r.ReadyAt, r.MarkAt)
	}
	if !r.StartedAt.Equal(rentalT0.Add(-2 * time.Second)) {
		t.Fatalf("StartedAt = %v, want search start", r.StartedAt)
	}
}

func TestRentalExplicitOriginNamed(t *testing.T) {
	d, _ := store.NewDocument("iid")
	step(t, &d, rentalT0, "", func(d *store.Document) {
		d.Generation = 1
		d.Instance = &store.Instance{Offer: rentalOffer()}
	})
	if d.Rentals[0].Origin != "explicit" {
		t.Fatalf("Origin = %q, want explicit", d.Rentals[0].Origin)
	}
}

// Transition reruns fn on a fresh copy after a conflict; each run must
// produce the same record and the same change.
func TestRentalRerunOnFreshCopyIsIdentical(t *testing.T) {
	base, _ := store.NewDocument("iid")
	base.Generation = 1
	base.Instance = &store.Instance{State: Booting.String(), ProviderID: "7", Offer: rentalOffer()}
	mark := rentalT0
	base.Rentals = []store.Rental{{Gen: 1, StartedAt: rentalT0, MarkAt: &mark, Stages: map[string]int64{"search": 0, "create": 0}}}
	raw, _ := json.Marshal(base)
	var out [2]string
	var changes [2]rentalChange
	for i := range out {
		var d store.Document
		_ = json.Unmarshal(raw, &d)
		changes[i] = step(t, &d, rentalT0.Add(15*time.Second), "", func(d *store.Document) { d.Instance.SSHHost = "203.0.113.9" })
		b, _ := json.Marshal(d.Rentals)
		out[i] = string(b)
	}
	if out[0] != out[1] || changes[0] != changes[1] {
		t.Fatalf("runs differ:\n%s\n%s\n%+v %+v", out[0], out[1], changes[0], changes[1])
	}
}

func TestRentalOutcomeFromSearchingNeverTouchesPreviousRental(t *testing.T) {
	d, _ := store.NewDocument("iid")
	d.Generation = 1
	d.Rentals = []store.Rental{{Gen: 1, Outcome: "ready"}}
	step(t, &d, rentalT0, "no_offer", func(*store.Document) {})
	if len(d.Rentals) != 1 || d.Rentals[0].Outcome != "ready" {
		t.Fatalf("rentals = %+v, want one ready record", d.Rentals)
	}
}

func TestRentalFailureOutcomeThenEnd(t *testing.T) {
	d, _ := store.NewDocument("iid")
	d.Generation = 3
	d.Instance = &store.Instance{State: Booting.String(), ProviderID: "9", Offer: rentalOffer()}
	mark := rentalT0
	d.Rentals = []store.Rental{{Gen: 3, StartedAt: rentalT0, MarkAt: &mark, Stages: map[string]int64{"search": 0}}}
	step(t, &d, rentalT0.Add(10*time.Second), "failed_status", func(d *store.Document) { d.Instance.State = Destroying.String() })
	if d.Rentals[0].Outcome != "failed_status" {
		t.Fatalf("Outcome = %q, want failed_status", d.Rentals[0].Outcome)
	}
	end := rentalT0.Add(20 * time.Second)
	d.Ledger.Entries = []store.LedgerEntry{{Gen: 3, End: &end, EstUSD: 0.18436672}}
	step(t, &d, end, "", func(d *store.Document) { d.Instance = nil })
	r := d.Rentals[0]
	if r.Outcome != "failed_status" || r.EndedAt == nil || !r.EndedAt.Equal(end) || r.CostUSD != 0.1844 || r.MarkAt != nil {
		t.Fatalf("ended record = %+v", r)
	}
}

func TestRentalEndFallsBackToCreateOpOutcome(t *testing.T) {
	d, _ := store.NewDocument("iid")
	d.Generation = 4
	d.Instance = &store.Instance{State: Destroying.String(), ProviderID: "9", Offer: rentalOffer()}
	d.Ops = []store.Op{{Key: createOpKey(4), Outcome: "duplicate_label"}}
	d.Rentals = []store.Rental{{Gen: 4, StartedAt: rentalT0}}
	step(t, &d, rentalT0.Add(time.Minute), "", func(d *store.Document) { d.Instance = nil })
	if d.Rentals[0].Outcome != "duplicate_label" {
		t.Fatalf("Outcome = %q, want duplicate_label", d.Rentals[0].Outcome)
	}
}

// TestMarkStageSkipsAlreadyPresentStage is a pod restart during Loading:
// recovery rewrites the state to Booting, and the second EvLinkUp that
// follows must not re-observe or overwrite the "link" stage.
func TestMarkStageSkipsAlreadyPresentStage(t *testing.T) {
	d, _ := store.NewDocument("iid")
	d.Generation = 1
	d.Instance = &store.Instance{State: Booting.String(), ProviderID: "7", SSHHost: "203.0.113.9", Offer: rentalOffer()}
	mark := rentalT0
	d.Rentals = []store.Rental{{Gen: 1, MarkAt: &mark, Stages: map[string]int64{"link": 5}}}
	got := step(t, &d, rentalT0.Add(10*time.Second), "", func(d *store.Document) { d.Instance.State = Loading.String() })
	if got != (rentalChange{}) {
		t.Fatalf("change = %+v, want zero value", got)
	}
	r := d.Rentals[0]
	if r.Stages["link"] != 5 {
		t.Fatalf("Stages[link] = %d, want unchanged 5", r.Stages["link"])
	}
	if r.MarkAt == nil || !r.MarkAt.Equal(mark) {
		t.Fatalf("MarkAt = %v, want unchanged %v", r.MarkAt, mark)
	}
}

func TestSummarizeWorkerStages(t *testing.T) {
	at := func(s float64) time.Time { return rentalT0.Add(time.Duration(s * float64(time.Second))) }
	cases := []struct {
		name       string
		times      map[string]time.Time
		wantStages map[string]int64
		wantMBps   float64
	}{
		{"miss", map[string]time.Time{
			"download_begin": at(0), "download_end": at(285), "verified": at(300),
			"server_spawn": at(301), "server_healthy": at(310),
		}, map[string]int64{"download": 300, "load": 9}, 100.3},
		{"skipped", map[string]time.Time{
			"download_skipped": at(0), "verified": at(20), "server_spawn": at(21), "server_healthy": at(30),
		}, map[string]int64{"download": 0, "load": 9}, 0},
		{"not healthy yet", map[string]time.Time{
			"download_begin": at(0), "download_end": at(285), "verified": at(300), "server_spawn": at(301),
		}, map[string]int64{"download": 300}, 100.3},
		{"empty", map[string]time.Time{}, map[string]int64{}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stages, mbps := summarizeWorkerStages(c.times, 28595763840)
			if len(stages) != len(c.wantStages) || mbps != c.wantMBps {
				t.Fatalf("stages %v mbps %v, want %v %v", stages, mbps, c.wantStages, c.wantMBps)
			}
			for k, v := range c.wantStages {
				if stages[k] != v {
					t.Fatalf("stages[%s] = %d, want %d", k, stages[k], v)
				}
			}
		})
	}
}

// TestReadWorkerStagesRetriesForServerHealthy: the worker only stamps
// server_healthy inside its own poll loop, so the read right after READY
// commonly misses it; readWorkerStages must retry rather than give up, and
// must not drop what an earlier, incomplete read already found (a stop
// that tears the connection down mid-retry must not erase the download stage).
func TestReadWorkerStagesRetriesForServerHealthy(t *testing.T) {
	c, _, clk, _, w := newTestController(t)
	logs := captureLog(c)
	c.state, c.generation = Ready, 3
	c.conn = tunnelConnForTest(func() error { return nil })
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3", ProviderID: "9"}
		mark := clk.Now()
		d.Rentals = []store.Rental{{Gen: 3, StartedAt: mark, ModelBytes: 28595763840}}
	})
	w0 := clk.Now()
	noHealthy := map[string]time.Time{
		"download_begin": w0, "download_end": w0.Add(285 * time.Second),
		"verified": w0.Add(300 * time.Second), "server_spawn": w0.Add(301 * time.Second),
	}
	healthy := map[string]time.Time{}
	for k, v := range noHealthy {
		healthy[k] = v
	}
	healthy["server_healthy"] = w0.Add(310 * time.Second)
	w.SetProgressSequence(DownloadProgress{Times: noHealthy}, DownloadProgress{Times: healthy})

	done := make(chan struct{})
	go func() {
		c.readWorkerStages(context.Background(), 3)
		close(done)
	}()
	clk.BlockUntil(1)
	if ws := c.workerStagesFor(3); ws == nil || ws.stages["download"] != 300 {
		t.Fatalf("mid-wait workerStages = %+v, want download 300 cached from the first read", ws)
	}
	clk.Advance(pollInterval)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readWorkerStages did not finish after the clock advanced")
	}

	ws := c.workerStagesFor(3)
	if ws == nil || ws.stages["download"] != 300 || ws.stages["load"] != 9 {
		t.Fatalf("workerStages = %+v, want download 300, load 9", ws)
	}
	if calls := w.progressCallCount(); calls != 2 {
		t.Fatalf("Progress calls = %d, want 2", calls)
	}
	if got := logs.lines("rental_ready"); len(got) != 1 {
		t.Fatalf("rental_ready lines = %q, want exactly 1", got)
	}
}

// TestReadWorkerStagesKeepsEarlierReadWhenConnDrops: a stop that closes the
// connection between retries must not erase what the first read found.
func TestReadWorkerStagesKeepsEarlierReadWhenConnDrops(t *testing.T) {
	c, _, clk, _, w := newTestController(t)
	logs := captureLog(c)
	c.state, c.generation = Ready, 3
	c.conn = tunnelConnForTest(func() error { return nil })
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3", ProviderID: "9"}
		mark := clk.Now()
		d.Rentals = []store.Rental{{Gen: 3, StartedAt: mark, ModelBytes: 28595763840}}
	})
	w0 := clk.Now()
	w.SetProgressSequence(DownloadProgress{Times: map[string]time.Time{
		"download_begin": w0, "download_end": w0.Add(285 * time.Second),
		"verified": w0.Add(300 * time.Second), "server_spawn": w0.Add(301 * time.Second),
	}})

	done := make(chan struct{})
	go func() {
		c.readWorkerStages(context.Background(), 3)
		close(done)
	}()
	clk.BlockUntil(1)
	// Simulate Stop tearing the connection and generation down mid-retry.
	c.mu.Lock()
	c.conn, c.generation = nil, 4
	c.mu.Unlock()
	clk.Advance(pollInterval)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readWorkerStages did not finish after the clock advanced")
	}

	ws := c.workerStagesFor(3)
	if ws == nil || ws.stages["download"] != 300 {
		t.Fatalf("workerStages = %+v, want the first read's download 300 preserved", ws)
	}
	if _, hasLoad := ws.stages["load"]; hasLoad {
		t.Fatalf("workerStages = %+v, want no load (server_healthy never read)", ws)
	}
	if got := logs.lines("rental_ready"); len(got) != 1 {
		t.Fatalf("rental_ready lines = %q, want exactly 1", got)
	}
}

func TestRentalEndMergesWorkerStages(t *testing.T) {
	d, _ := store.NewDocument("iid")
	d.Generation = 5
	d.Instance = &store.Instance{State: Destroying.String(), ProviderID: "9"}
	d.Rentals = []store.Rental{{Gen: 5, Outcome: "ready", Stages: map[string]int64{"boot": 15}}}
	var ch rentalChange
	in := rentalInput{now: rentalT0, worker: &workerStages{gen: 5, stages: map[string]int64{"download": 300, "load": 9}, mbps: 100.3}}
	if err := withRental(func(d *store.Document) error { d.Instance = nil; return nil }, in, &ch)(&d); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	r := d.Rentals[0]
	if r.Stages["download"] != 300 || r.Stages["load"] != 9 || r.Stages["boot"] != 15 || r.DownloadMBps != 100.3 {
		t.Fatalf("record = %+v", r)
	}
}

func TestRentalCostIgnoresStorageEntryOfSameGen(t *testing.T) {
	d, _ := store.NewDocument("iid")
	d.Generation = 6
	d.Instance = &store.Instance{State: Destroying.String(), ProviderID: "9", Offer: rentalOffer()}
	end := rentalT0
	d.Ledger.Entries = []store.LedgerEntry{{Gen: 6, End: &end, EstUSD: 0.18}, {Gen: 6, Volume: "vkllm_iid_v6", End: &end, EstUSD: 9.99}}
	d.Rentals = []store.Rental{{Gen: 6, Outcome: "ready"}}
	step(t, &d, rentalT0, "", func(d *store.Document) { d.Instance = nil })
	if d.Rentals[0].CostUSD != 0.18 {
		t.Fatalf("CostUSD = %v, want the GPU entry 0.18", d.Rentals[0].CostUSD)
	}
}
