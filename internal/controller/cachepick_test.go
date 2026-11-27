package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/offercost"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	fakestore "github.com/savak1990/vk-llm-on-demand/internal/store/fake"
)

func ranked(id, machine int64, sessionUSD float64) offercost.Ranked {
	return offercost.Ranked{
		Offer:     provider.Offer{ID: id, MachineID: machine, Hourly: provider.FromUSD(sessionUSD)},
		Breakdown: offercost.Breakdown{SessionUSD: provider.FromUSD(sessionUSD)},
	}
}

func TestPickOfferCachedHost(t *testing.T) {
	cached := ranked(1, 100, 0.40)
	ready := map[int64]store.CacheVolume{100: {Label: "vkllm_x_v1", VolumeID: 987, MachineID: 100, State: store.CacheReady}}
	for _, c := range []struct {
		other  float64
		wantID int64
		mode   string
	}{{0.35, 1, "hit"}, {0.34, 2, ""}, {0.33, 2, ""}} {
		other := ranked(2, 200, c.other)
		got, plan := pickOffer(pickInput{ranked: []offercost.Ranked{other, cached}, hit: map[int64]offercost.Ranked{1: cached}, ready: ready, threshold: 0.15})
		if got.Offer.ID != c.wantID || plan.mode != c.mode {
			t.Fatalf("other %.2f: chose %d mode %q, want %d %q", c.other, got.Offer.ID, plan.mode, c.wantID, c.mode)
		}
		if plan.mode == "hit" && plan.volumeID != 987 {
			t.Fatalf("hit plan volume = %d, want 987", plan.volumeID)
		}
	}
}

func TestPickOfferOnlyCachedOfferWins(t *testing.T) {
	cached := ranked(1, 100, 0.40)
	ready := map[int64]store.CacheVolume{100: {VolumeID: 987, State: store.CacheReady}}
	got, plan := pickOffer(pickInput{ranked: []offercost.Ranked{cached}, hit: map[int64]offercost.Ranked{1: cached}, ready: ready, threshold: 0.15})
	if got.Offer.ID != 1 || plan.mode != "hit" {
		t.Fatalf("chose %d %q", got.Offer.ID, plan.mode)
	}
}

func TestPickOfferSeedablePreference(t *testing.T) {
	seed := ranked(3, 300, 0.40)
	seedable := map[int64]provider.VolumeOffer{300: {ID: 55800361, MachineID: 300, StoragePerGBMonth: provider.FromUSD(0.30)}}
	for _, c := range []struct {
		other  float64
		wantID int64
		mode   string
	}{{0.35, 3, "seed"}, {0.34, 4, ""}} {
		other := ranked(4, 400, c.other)
		got, plan := pickOffer(pickInput{ranked: []offercost.Ranked{other, seed}, seedable: seedable, threshold: 0.15})
		if got.Offer.ID != c.wantID || plan.mode != c.mode {
			t.Fatalf("other %.2f: chose %d %q, want %d %q", c.other, got.Offer.ID, plan.mode, c.wantID, c.mode)
		}
		if plan.mode == "seed" && (plan.volumeOfferID != 55800361 || plan.storageUSD != 0.30) {
			t.Fatalf("seed plan = %+v", plan)
		}
	}
}

func TestPickOfferNoCacheIsPlainOrder(t *testing.T) {
	a, b := ranked(5, 500, 0.30), ranked(6, 600, 0.31)
	got, plan := pickOffer(pickInput{ranked: []offercost.Ranked{a, b}, threshold: 0.15})
	if got.Offer.ID != 5 || plan.mode != "" {
		t.Fatalf("chose %d %q, want 5 plain", got.Offer.ID, plan.mode)
	}
}

func TestVolumeSizeGB(t *testing.T) {
	if got := volumeSizeGB(28595763840); got != 34 {
		t.Fatalf("volumeSizeGB = %d, want 34", got)
	}
}

func TestUsableReadyNeedsResolvedIDAndSameModel(t *testing.T) {
	d, _ := store.NewDocument("abc")
	d.Cache = &store.CacheState{Volumes: []store.CacheVolume{
		{Label: "a", MachineID: 1, VolumeID: 0, State: store.CacheReady, ModelURL: "u", SHA256: "s"},
		{Label: "b", MachineID: 2, VolumeID: 9, State: store.CacheReady, ModelURL: "u", SHA256: "s"},
		{Label: "c", MachineID: 3, VolumeID: 8, State: store.CacheReady, ModelURL: "u", SHA256: "other"},
		{Label: "d", MachineID: 4, VolumeID: 7, State: store.CacheReady, ModelURL: "u", SHA256: "s", InUseGen: 3},
		{Label: "e", MachineID: 5, VolumeID: 6, State: store.CacheSeeding, ModelURL: "u", SHA256: "s"},
	}}
	got := usableReady(d, "u", "s")
	if len(got) != 1 || got[2].Label != "b" {
		t.Fatalf("usableReady = %+v, want only machine 2", got)
	}
}

func TestPickOfferNeverReturnsAnOfferOutsideRanked(t *testing.T) {
	inRanked := ranked(7, 700, 0.50)
	cachedButNotAdmitted := ranked(8, 800, 0.10)
	ready := map[int64]store.CacheVolume{800: {VolumeID: 1, State: store.CacheReady}}
	got, _ := pickOffer(pickInput{ranked: []offercost.Ranked{inRanked}, hit: map[int64]offercost.Ranked{8: cachedButNotAdmitted}, ready: ready, threshold: 0.15})
	if got.Offer.ID != 7 {
		t.Fatalf("chose %d, want 7 (machine 800 was not admitted)", got.Offer.ID)
	}
}

// With 1 GB of model the volume is 4 GB; at $0.30 over a 730 h retention
// it costs $1.20, and the seed offer's download fee adds $1.00.
func TestCacheChoice(t *testing.T) {
	const modelURL, sha = "https://example.invalid/model.gguf", "s"
	seedOffer := ranked(1, 10, 0.40)
	seedOffer.Offer.InetDownMbps = 1000
	seedOffer.Breakdown.DownloadUSD = provider.FromUSD(1.00)
	plain := ranked(2, 20, 0.35)
	ready := func(machine, volumeID, inUse int64) store.CacheVolume {
		return store.CacheVolume{Label: "v", VolumeID: volumeID, MachineID: machine, State: store.CacheReady, ModelURL: modelURL, SHA256: sha, InUseGen: inUse}
	}
	for _, c := range []struct {
		name         string
		maxCaches    int
		priceUSD     float64
		budgetUSD    float64
		volumes      []store.CacheVolume
		failSearch   bool
		failLoad     bool
		wantID       int64
		wantMode     string
		wantSearches int
	}{
		{name: "seedable within cap", maxCaches: 1, priceUSD: 0.30, wantID: 1, wantMode: "seed", wantSearches: 1},
		{name: "storage price above cap", maxCaches: 1, priceUSD: 0.50, wantID: 2, wantSearches: 1},
		{name: "budget covers storage and download", maxCaches: 1, priceUSD: 0.30, budgetUSD: 2.5, wantID: 1, wantMode: "seed", wantSearches: 1},
		{name: "budget covers storage but not download", maxCaches: 1, priceUSD: 0.30, budgetUSD: 2.0, wantID: 2, wantSearches: 1},
		{name: "volume search fails", maxCaches: 1, priceUSD: 0.30, failSearch: true, wantID: 2, wantSearches: 1},
		{name: "store load fails", maxCaches: 1, priceUSD: 0.30, failLoad: true, wantID: 2, wantSearches: 0},
		{name: "slots full and all in use", maxCaches: 1, priceUSD: 0.30, volumes: []store.CacheVolume{ready(99, 3, 5)}, wantID: 2, wantSearches: 0},
		{name: "cache disabled ignores a ready volume", maxCaches: 0, priceUSD: 0.30, volumes: []store.CacheVolume{ready(10, 7, 0)}, wantID: 2, wantSearches: 0},
		{name: "ready volume is a hit", maxCaches: 1, priceUSD: 0.30, volumes: []store.CacheVolume{ready(10, 7, 0)}, wantID: 1, wantMode: "hit", wantSearches: 1},
		{name: "machine already seeding is not seedable", maxCaches: 2, priceUSD: 0.30, volumes: []store.CacheVolume{{Label: "v", MachineID: 10, State: store.CacheSeeding}}, wantID: 2, wantSearches: 1},
		{name: "machine with an in-use ready volume is not seedable", maxCaches: 2, priceUSD: 0.30, volumes: []store.CacheVolume{ready(10, 7, 5)}, wantID: 2, wantSearches: 1},
		{name: "machine with an unresolved ready volume is not seedable", maxCaches: 2, priceUSD: 0.30, volumes: []store.CacheVolume{ready(10, 0, 0)}, wantID: 2, wantSearches: 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctl, prov, _, _, _ := newTestController(t)
			ctl.deps.ModelSHA256 = sha
			ctl.deps.Config.Cache.MaxCaches = c.maxCaches
			ctl.deps.Config.Cache.Retention = 730 * time.Hour
			ctl.deps.Config.Cache.PreferThreshold = 0.15
			ctl.deps.Config.Cache.MaxStoragePriceUSDPerGBMonth = 0.40
			prov.SetVolumeOffers(provider.VolumeOffer{ID: 500, MachineID: 10, StoragePerGBMonth: provider.FromUSD(c.priceUSD), DiskSpaceGB: 100})
			st := ctl.deps.Store.(*fakestore.Fake)
			doc, _ := store.NewDocument("iid")
			doc.Cache = &store.CacheState{Volumes: c.volumes}
			if c.budgetUSD > 0 {
				doc.BudgetMonthUSD = &c.budgetUSD
			}
			st.Put(doc)
			if c.failSearch {
				prov.FailNext("search_volumes", 1, errors.New("boom"))
			}
			if c.failLoad {
				st.FailNext(1, errors.New("boom"))
			}
			got, plan := ctl.cacheChoice(context.Background(), []offercost.Ranked{plain, seedOffer}, 1_000_000_000, 10)
			if got.Offer.ID != c.wantID || plan.mode != c.wantMode {
				t.Fatalf("chose %d %q, want %d %q", got.Offer.ID, plan.mode, c.wantID, c.wantMode)
			}
			if n := prov.Calls("search_volumes"); n != c.wantSearches {
				t.Fatalf("search_volumes calls = %d, want %d", n, c.wantSearches)
			}
			if plan.mode == "seed" && (plan.volumeOfferID != 500 || plan.storageUSD != 0.30 || plan.sizeGB != 4) {
				t.Fatalf("seed plan = %+v", plan)
			}
			if plan.mode == "hit" && (plan.volumeID != 7 || got.Breakdown.DownloadUSD != seedOffer.Breakdown.DownloadUSD) {
				t.Fatalf("hit plan = %+v download %v, want the ranked download fee", plan, got.Breakdown.DownloadUSD)
			}
		})
	}
}

func TestHasSeedRoom(t *testing.T) {
	readyVol := func(inUse int64) store.CacheVolume {
		return store.CacheVolume{State: store.CacheReady, InUseGen: inUse}
	}
	for _, c := range []struct {
		name    string
		volumes []store.CacheVolume
		max     int
		want    bool
	}{
		{"empty cache", nil, 1, true},
		{"live below max", []store.CacheVolume{readyVol(3)}, 2, true},
		{"full but one replaceable", []store.CacheVolume{readyVol(3), readyVol(0)}, 2, true},
		{"full and none replaceable", []store.CacheVolume{readyVol(3), {State: store.CacheSeeding}}, 2, false},
	} {
		doc, _ := store.NewDocument("iid")
		doc.Cache = &store.CacheState{Volumes: c.volumes}
		if got := hasSeedRoom(doc, c.max); got != c.want {
			t.Fatalf("%s: hasSeedRoom = %v, want %v", c.name, got, c.want)
		}
	}
}

// The hit wins on its zero-download estimate, but the offer it returns
// keeps the ranked download fee, so the ledger never charges $0.
func TestPickOfferHitKeepsRankedDownloadFee(t *testing.T) {
	cached := ranked(1, 100, 0.50)
	cached.Breakdown.DownloadUSD = provider.FromUSD(0.10)
	estimate := ranked(1, 100, 0.40)
	ready := map[int64]store.CacheVolume{100: {VolumeID: 987, State: store.CacheReady}}
	other := ranked(2, 200, 0.35)
	got, plan := pickOffer(pickInput{ranked: []offercost.Ranked{other, cached}, hit: map[int64]offercost.Ranked{1: estimate}, ready: ready, threshold: 0.15})
	if got.Offer.ID != 1 || plan.mode != "hit" {
		t.Fatalf("chose %d %q, want the hit", got.Offer.ID, plan.mode)
	}
	if got.Breakdown != cached.Breakdown {
		t.Fatalf("breakdown = %+v, want the ranked %+v", got.Breakdown, cached.Breakdown)
	}
}
