package controller

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// markForRelease runs in every reconcile tick, with or without a GPU,
// because a detached volume bills when no GPU runs.
func markForRelease(d *store.Document, now time.Time, maxCaches int, modelURL, sha string, budgetExhausted bool) {
	if d.Cache == nil {
		return
	}
	live := 0
	for i := range d.Cache.Volumes {
		v := &d.Cache.Volumes[i]
		if v.State == store.CacheDeleting {
			continue
		}
		if v.InUseGen != 0 {
			live++
			continue
		}
		switch {
		case budgetExhausted:
			markDeleting(v, now, "budget")
		case v.ModelURL != modelURL || v.SHA256 != sha:
			markDeleting(v, now, "model_changed")
		case v.ExpiresAt != nil && !now.Before(*v.ExpiresAt):
			markDeleting(v, now, "expired")
		default:
			live++
		}
	}
	for live > maxCaches {
		oldest := -1
		for i, v := range d.Cache.Volumes {
			if v.State == store.CacheDeleting || v.InUseGen != 0 {
				continue
			}
			if oldest == -1 || v.ExpiresAt == nil ||
				(d.Cache.Volumes[oldest].ExpiresAt != nil && v.ExpiresAt.Before(*d.Cache.Volumes[oldest].ExpiresAt)) {
				oldest = i
			}
		}
		if oldest == -1 {
			return
		}
		markDeleting(&d.Cache.Volumes[oldest], now, "over_limit")
		live--
	}
}

// ownVolume keeps another installation whose id merely extends ours
// ("iid-v2" under "iid") out of the orphan rule.
func ownVolume(label, prefix string) bool {
	_, err := strconv.ParseInt(strings.TrimPrefix(label, prefix), 10, 64)
	return strings.HasPrefix(label, prefix) && err == nil
}

// matchVolumes pairs each entry with its listed volume: by id once known,
// else by label on the entry's machine, preferring one attached to an
// instance, then the lowest id.
func matchVolumes(entries []store.CacheVolume, vols []provider.Volume) map[string]provider.Volume {
	m := map[string]provider.Volume{}
	for _, e := range entries {
		for _, v := range vols {
			if v.Label != e.Label || (e.VolumeID != 0 && int64(v.ID) != e.VolumeID) ||
				(e.VolumeID == 0 && e.MachineID != 0 && v.MachineID != e.MachineID) {
				continue
			}
			b, ok := m[e.Label]
			attached, bAttached := len(v.Instances) > 0, len(b.Instances) > 0
			if !ok || (attached && !bAttached) || (attached == bAttached && v.ID < b.ID) {
				m[e.Label] = v
			}
		}
	}
	return m
}

// reconcileVolumes lists before it loads the document: a volume only exists
// after its record was committed, so the later load always knows it.
func (c *Controller) reconcileVolumes(ctx context.Context) {
	prefix := volumeLabelPrefix(c.deps.InstallationID)
	vols, err := c.deps.Provider.ListVolumes(ctx, prefix)
	if err != nil {
		return
	}
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil {
		return
	}
	var entries []store.CacheVolume
	if doc.Cache != nil {
		entries = doc.Cache.Volumes
	}
	matched := matchVolumes(entries, vols)
	known := map[provider.VolumeID]bool{}
	for _, v := range matched {
		known[v.ID] = true
	}
	var removeAbsent []string
	var orphans []provider.VolumeID
	c.reconcileMu.Lock()
	for _, e := range entries {
		if _, seen := matched[e.Label]; seen || e.InUseGen != 0 {
			delete(c.volumeAbsentObserved, e.Label)
			continue
		}
		c.volumeAbsentObserved[e.Label]++
		if c.volumeAbsentObserved[e.Label] >= 2 {
			removeAbsent = append(removeAbsent, e.Label)
		}
	}
	seenIDs := map[provider.VolumeID]bool{}
	for _, v := range vols {
		if known[v.ID] || !ownVolume(v.Label, prefix) {
			continue
		}
		seenIDs[v.ID] = true
		c.volumeOrphanObserved[v.ID]++
		if c.volumeOrphanObserved[v.ID] >= 2 {
			orphans = append(orphans, v.ID)
		}
	}
	for id := range c.volumeOrphanObserved {
		if !seenIDs[id] {
			delete(c.volumeOrphanObserved, id)
		}
	}
	c.reconcileMu.Unlock()

	now := c.deps.Clock.Now()
	var deleted []store.CacheVolume
	for _, e := range entries {
		v, seen := matched[e.Label]
		if e.State != store.CacheDeleting || e.InUseGen != 0 || !seen {
			continue
		}
		if err := c.deps.Provider.DeleteVolume(ctx, v.ID); err != nil && !errors.Is(err, provider.ErrNotFound) {
			cacheDeleteFailuresTotal.Inc()
			c.warn("controller: volume delete failed", "reason", e.DeleteReason, "err", err)
			continue
		}
		deleted = append(deleted, e)
	}
	for _, id := range orphans {
		if err := c.deps.Provider.DeleteVolume(ctx, id); err != nil && !errors.Is(err, provider.ErrNotFound) {
			cacheDeleteFailuresTotal.Inc()
			c.warn("controller: volume delete failed", "reason", "orphan", "err", err)
			continue
		}
		cacheDeletedTotal.WithLabelValues("orphan").Inc()
	}
	c.commitVolumeSweep(ctx, now, matched, deleted, removeAbsent)
}

// commitVolumeSweep writes ids learned from the list and drops entries that
// are gone, closing their storage entries in the same transition.
func (c *Controller) commitVolumeSweep(ctx context.Context, now time.Time, matched map[string]provider.Volume, deleted []store.CacheVolume, absent []string) {
	gone := map[string]bool{}
	for _, e := range deleted {
		gone[e.Label] = true
	}
	for _, l := range absent {
		gone[l] = true
	}
	_, err := store.Transition(ctx, c.deps.Store, func(d *store.Document) error {
		if d.Cache == nil {
			return nil
		}
		kept := d.Cache.Volumes[:0]
		for _, e := range d.Cache.Volumes {
			if gone[e.Label] && e.InUseGen == 0 {
				c.deps.Budget.CloseStorageEntry(d, now, e.Label)
				continue
			}
			if v, ok := matched[e.Label]; ok && e.VolumeID == 0 {
				e.VolumeID, e.MachineID = int64(v.ID), v.MachineID
			}
			kept = append(kept, e)
		}
		d.Cache.Volumes = kept
		return nil
	})
	if err != nil {
		c.warn("controller: volume sweep commit failed", "err", err)
		return
	}
	for _, e := range deleted {
		cacheDeletedTotal.WithLabelValues(e.DeleteReason).Inc()
	}
	c.reconcileMu.Lock()
	for _, l := range absent {
		delete(c.volumeAbsentObserved, l)
	}
	c.reconcileMu.Unlock()
}
