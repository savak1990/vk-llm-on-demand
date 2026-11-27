package controller

import (
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

var cacheT0 = time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

func TestVolumeLabel(t *testing.T) {
	if got := volumeLabel("lab-4f2c", 11); got != "vkllm_lab_4f2c_v11" {
		t.Fatalf("volumeLabel = %q", got)
	}
	if got := volumeLabelPrefix("lab-4f2c"); got != "vkllm_lab_4f2c_v" {
		t.Fatalf("volumeLabelPrefix = %q", got)
	}
}

func runCache(t *testing.T, d *store.Document, now time.Time, mutate func(*store.Document)) {
	t.Helper()
	if err := withCache(func(d *store.Document) error { mutate(d); return nil }, now, 72*time.Hour)(d); err != nil {
		t.Fatalf("mutate: %v", err)
	}
}

func TestWithCacheReadyThenRelease(t *testing.T) {
	d, _ := store.NewDocument("abc")
	d.Generation = 11
	d.Instance = &store.Instance{State: Loading.String()}
	d.Cache = &store.CacheState{Volumes: []store.CacheVolume{{Label: "vkllm_abc_v11", State: store.CacheSeeding, InUseGen: 11}}}
	runCache(t, &d, cacheT0, func(d *store.Document) { r := cacheT0; d.Instance.ReadyAt = &r; d.Instance.State = Ready.String() })
	if v := d.Cache.Volumes[0]; v.State != store.CacheReady || v.InUseGen != 11 {
		t.Fatalf("after READY = %+v", v)
	}
	end := cacheT0.Add(time.Hour)
	runCache(t, &d, end, func(d *store.Document) { d.Instance = nil })
	v := d.Cache.Volumes[0]
	if v.InUseGen != 0 || v.ExpiresAt == nil || !v.ExpiresAt.Equal(end.Add(72*time.Hour)) || v.State != store.CacheReady {
		t.Fatalf("after destroy = %+v", v)
	}
}

func TestWithCacheSeedFailed(t *testing.T) {
	d, _ := store.NewDocument("abc")
	d.Generation = 12
	d.Instance = &store.Instance{State: Booting.String()}
	d.Cache = &store.CacheState{Volumes: []store.CacheVolume{{Label: "vkllm_abc_v12", State: store.CacheSeeding, InUseGen: 12}}}
	runCache(t, &d, cacheT0, func(d *store.Document) { d.Instance = nil })
	v := d.Cache.Volumes[0]
	if v.State != store.CacheDeleting || v.DeleteReason != "seed_failed" || v.InUseGen != 0 || v.DeleteRequestedAt == nil {
		t.Fatalf("seed failed = %+v", v)
	}
}
