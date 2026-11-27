package controller

import (
	"slices"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func cacheDoc(vols ...store.CacheVolume) store.Document {
	d, _ := store.NewDocument("abc")
	d.Cache = &store.CacheState{Volumes: vols}
	return d
}

func TestMarkForRelease(t *testing.T) {
	past, future := cacheT0.Add(-time.Minute), cacheT0.Add(time.Hour)
	ready := func(label string, exp time.Time) store.CacheVolume {
		return store.CacheVolume{Label: label, VolumeID: 1, State: store.CacheReady, ModelURL: "u", SHA256: "s", ExpiresAt: &exp}
	}
	noExpiry := store.CacheVolume{Label: "b", VolumeID: 2, State: store.CacheReady, ModelURL: "u", SHA256: "s"}
	cases := []struct {
		name     string
		doc      store.Document
		max      int
		exhaust  bool
		wantDel  []string
		reasonOf string
	}{
		{"expired", cacheDoc(ready("a", past)), 1, false, []string{"a"}, "expired"},
		{"not yet", cacheDoc(ready("a", future)), 1, false, nil, ""},
		{"budget", cacheDoc(ready("a", future)), 1, true, []string{"a"}, "budget"},
		{"disabled", cacheDoc(ready("a", future)), 0, false, []string{"a"}, "over_limit"},
		{"model changed", cacheDoc(store.CacheVolume{Label: "a", State: store.CacheReady, ModelURL: "old", SHA256: "s", ExpiresAt: &future}), 1, false, []string{"a"}, "model_changed"},
		{"in use never", cacheDoc(store.CacheVolume{Label: "a", State: store.CacheReady, ModelURL: "u", SHA256: "s", InUseGen: 5}), 0, true, nil, ""},
		{"lowered limit drops the oldest", cacheDoc(ready("a", future.Add(time.Hour)), ready("b", future)), 1, false, []string{"b"}, "over_limit"},
		{"no expiry counts as oldest", cacheDoc(ready("a", future), noExpiry), 1, false, []string{"b"}, "over_limit"},
	}
	for _, c := range cases {
		d := c.doc
		markForRelease(&d, cacheT0, c.max, "u", "s", c.exhaust)
		var del []string
		for _, v := range d.Cache.Volumes {
			if v.State == store.CacheDeleting {
				del = append(del, v.Label)
				if v.DeleteReason != c.reasonOf {
					t.Fatalf("%s: reason %q, want %q", c.name, v.DeleteReason, c.reasonOf)
				}
			}
		}
		if !slices.Equal(del, c.wantDel) {
			t.Fatalf("%s: deleting %v, want %v", c.name, del, c.wantDel)
		}
	}
}

func TestMatchVolumesByLabelNeedsSameMachine(t *testing.T) {
	vols := []provider.Volume{{ID: 5, Label: "l", MachineID: 43}, {ID: 6, Label: "l", MachineID: 42}}
	for _, c := range []struct {
		name  string
		entry store.CacheVolume
		want  provider.VolumeID
	}{
		{"machine known", store.CacheVolume{Label: "l", MachineID: 42}, 6},
		{"machine unknown", store.CacheVolume{Label: "l"}, 5},
		{"no volume on the machine", store.CacheVolume{Label: "l", MachineID: 44}, 0},
		{"id known", store.CacheVolume{Label: "l", VolumeID: 5, MachineID: 42}, 5},
	} {
		got := matchVolumes([]store.CacheVolume{c.entry}, vols)["l"]
		if got.ID != c.want {
			t.Fatalf("%s: matched %d, want %d", c.name, got.ID, c.want)
		}
	}
}
