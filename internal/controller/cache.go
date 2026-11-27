package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// Vast labels allow letters, digits and underscore only, so the "-" an
// installation id may contain is replaced.
func volumeLabelPrefix(installationID string) string {
	return "vkllm_" + strings.ReplaceAll(installationID, "-", "_") + "_v"
}

func volumeLabel(installationID string, gen int64) string {
	return fmt.Sprintf("%s%d", volumeLabelPrefix(installationID), gen)
}

func storageDPH(sizeGB int, usdPerGBMonth float64) float64 {
	return float64(sizeGB) * usdPerGBMonth / 730
}

func markDeleting(v *store.CacheVolume, now time.Time, reason string) {
	if v.State == store.CacheDeleting {
		return
	}
	t := now
	v.State, v.DeleteRequestedAt, v.DeleteReason = store.CacheDeleting, &t, reason
}

// ReleaseCache marks every volume for deletion, the one in use too: the
// sweep skips it until its GPU ends, then deletes it like any other.
func (c *Controller) ReleaseCache(ctx context.Context) (int, error) {
	now, n := c.deps.Clock.Now(), 0
	if _, err := store.Transition(ctx, c.deps.Store, func(d *store.Document) error {
		n = 0
		if d.Cache == nil {
			return nil
		}
		for i := range d.Cache.Volumes {
			if v := &d.Cache.Volumes[i]; v.State != store.CacheDeleting {
				markDeleting(v, now, "manual")
				n++
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	c.dispatchReconcile(c.effCtx())
	return n, nil
}

// withCache derives volume changes from the instance before and after the
// transition: READY verifies a seeding volume, the end of the GPU frees it.
func withCache(mutate func(*store.Document) error, now time.Time, retention time.Duration) func(*store.Document) error {
	return func(d *store.Document) error {
		var before *store.Instance
		if d.Instance != nil {
			cp := *d.Instance
			before = &cp
		}
		gen := d.Generation
		if err := mutate(d); err != nil {
			return err
		}
		if before == nil || d.Cache == nil {
			return nil
		}
		after := d.Instance
		for i := range d.Cache.Volumes {
			v := &d.Cache.Volumes[i]
			if v.InUseGen != gen {
				continue
			}
			switch {
			case after == nil && v.State == store.CacheSeeding:
				v.InUseGen = 0
				markDeleting(v, now, "seed_failed")
			case after == nil && v.State == store.CacheReady && before.ReadyAt == nil && before.ProviderID != "":
				v.InUseGen = 0
				markDeleting(v, now, "hit_failed")
			case after == nil:
				v.InUseGen = 0
				exp := now.Add(retention)
				v.ExpiresAt = &exp
			case before.ReadyAt == nil && after.ReadyAt != nil && v.State == store.CacheSeeding:
				v.State = store.CacheReady
			}
		}
		return nil
	}
}

// releaseRefusedVolume frees the volume the provider would not attach, so
// the next search never offers it again.
func releaseRefusedVolume(d *store.Document, now time.Time) {
	d.Instance.Cache = "none"
	if d.Cache == nil {
		return
	}
	for i := range d.Cache.Volumes {
		if v := &d.Cache.Volumes[i]; v.InUseGen == d.Generation {
			v.InUseGen = 0
			markDeleting(v, now, "attach_failed")
		}
	}
}

// markRoomForSeed sends the oldest replaceable ready volume to deleting
// when every slot is taken; one with no expiry counts as the oldest.
func markRoomForSeed(d *store.Document, now time.Time, maxCaches int) {
	if d.Cache == nil {
		return
	}
	live, oldest := 0, -1
	for i, v := range d.Cache.Volumes {
		if v.State != store.CacheSeeding && v.State != store.CacheReady {
			continue
		}
		live++
		if v.State != store.CacheReady || v.InUseGen != 0 {
			continue
		}
		if oldest == -1 || v.ExpiresAt == nil ||
			(d.Cache.Volumes[oldest].ExpiresAt != nil && v.ExpiresAt.Before(*d.Cache.Volumes[oldest].ExpiresAt)) {
			oldest = i
		}
	}
	if live >= maxCaches && oldest >= 0 {
		markDeleting(&d.Cache.Volumes[oldest], now, "replaced")
	}
}

// applyCachePlan runs inside the T5 mutate, so the record and its storage
// ledger entry are committed before any create is sent.
func applyCachePlan(d *store.Document, p cachePlan, now time.Time, maxCaches int, b Budget) {
	d.Instance.Cache = "none"
	switch p.mode {
	case "hit":
		if d.Cache == nil {
			return
		}
		for i := range d.Cache.Volumes {
			v := &d.Cache.Volumes[i]
			if v.VolumeID == p.volumeID && v.State == store.CacheReady && v.InUseGen == 0 {
				v.InUseGen, v.ExpiresAt = d.Generation, nil
				d.Instance.Cache = "hit"
				return
			}
		}
	case "seed":
		if maxCaches <= 0 {
			return
		}
		if d.Cache == nil {
			d.Cache = &store.CacheState{}
		}
		markRoomForSeed(d, now, maxCaches)
		label := volumeLabel(d.InstallationID, d.Generation)
		d.Cache.Volumes = append(d.Cache.Volumes, store.CacheVolume{
			Label: label, VolumeOfferID: p.volumeOfferID, MachineID: d.Instance.Offer.MachineID,
			ModelURL: p.modelURL, SHA256: p.sha256, SizeGB: p.sizeGB, StorageUSDPerGBMonth: p.storageUSD,
			State: store.CacheSeeding, CreatedGen: d.Generation, InUseGen: d.Generation, CreatedAt: now,
		})
		if b != nil {
			b.OpenStorageEntry(d, now, d.Generation, label, storageDPH(p.sizeGB, p.storageUSD))
		}
		d.Instance.Cache = "seeded"
	}
}

// volumeAttachFor reads the record T5 committed; a store error creates the
// GPU without a volume, and the sweep later resolves the unused record.
func (c *Controller) volumeAttachFor(ctx context.Context, gen int64) *provider.VolumeAttach {
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil || doc.Cache == nil {
		return nil
	}
	for _, v := range doc.Cache.Volumes {
		if v.InUseGen != gen {
			continue
		}
		switch v.State {
		case store.CacheSeeding:
			return &provider.VolumeAttach{CreateNew: true, ID: v.VolumeOfferID, SizeGB: v.SizeGB, MountPath: "/models", Name: v.Label}
		case store.CacheReady:
			return &provider.VolumeAttach{ID: v.VolumeID, MountPath: "/models"}
		}
	}
	return nil
}
