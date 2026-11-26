package controller

import (
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/offercost"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
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
