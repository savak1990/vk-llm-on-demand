package controller

import (
	"context"
	"math"

	"github.com/savak1990/vk-llm-on-demand/internal/offercost"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

type cachePlan struct {
	mode          string
	volumeID      int64
	volumeOfferID int64
	storageUSD    float64
	sizeGB        int
	modelURL      string
	sha256        string
}

type pickInput struct {
	ranked    []offercost.Ranked
	hit       map[int64]offercost.Ranked
	ready     map[int64]store.CacheVolume
	seedable  map[int64]provider.VolumeOffer
	threshold float64
}

// cacheReason is why a machine's offer did or did not carry a cache, for
// the offer_choice log; it never changes which offer wins.
type cacheReason string

const (
	reasonCached          cacheReason = "cached"
	reasonSeedable        cacheReason = "seedable"
	reasonNoVolumeOffer   cacheReason = "no_volume_offer"
	reasonOverPriceCap    cacheReason = "volume_over_price_cap"
	reasonOverBudget      cacheReason = "over_budget"
	reasonHasOurVolume    cacheReason = "machine_has_our_volume"
	reasonCacheDisabled   cacheReason = "cache_disabled"
	reasonSearchFailed    cacheReason = "volume_search_failed"
	reasonNoSeedRoom      cacheReason = "no_seed_room"
	reasonStoreLoadFailed cacheReason = "store_load_failed"
)

type machineCache struct {
	reason        cacheReason
	volumeOfferID int64
	priceUSD      float64
	sizeGB        float64
}

// offerDecision is the rule that decided pickOfferWhy's choice and, when a
// comparison decided it, the two session costs it compared.
type offerDecision struct {
	rule       string
	prefUSD    float64
	otherUSD   float64
	threshold  float64
	hasCompare bool
}

type offerChoiceLog struct {
	decision offerDecision
	machines map[int64]machineCache
}

func allMachinesReason(ranked []offercost.Ranked, reason cacheReason) map[int64]machineCache {
	out := make(map[int64]machineCache, len(ranked))
	for _, r := range ranked {
		out[r.Offer.MachineID] = machineCache{reason: reason}
	}
	return out
}

func volumeSizeGB(modelBytes int64) int {
	return int(math.Ceil(float64(modelBytes)*1.1/1e9)) + 2
}

// usableReady maps machine id to a verified volume of the configured model
// whose provider id is known; only such a volume can be linked.
func usableReady(doc store.Document, modelURL, sha string) map[int64]store.CacheVolume {
	out := map[int64]store.CacheVolume{}
	if doc.Cache == nil {
		return out
	}
	for _, v := range doc.Cache.Volumes {
		if v.State == store.CacheReady && v.VolumeID != 0 && v.InUseGen == 0 && v.ModelURL == modelURL && v.SHA256 == sha {
			if _, dup := out[v.MachineID]; !dup {
				out[v.MachineID] = v
			}
		}
	}
	return out
}

// prefers is integer arithmetic in basis points, so "exactly 15 % cheaper"
// is decided the same way on every run.
func prefers(pref, other offercost.Ranked, threshold float64) bool {
	bp := int64(math.Round(threshold * 10000))
	return int64(other.Breakdown.SessionUSD)*10000 > int64(pref.Breakdown.SessionUSD)*(10000-bp)
}

// pickOffer chooses only among in.ranked, which holds admitted offers in
// session-cost order: a cached host first, then a host that can start a cache.
func pickOffer(in pickInput) (offercost.Ranked, cachePlan) {
	r, p, _ := pickOfferWhy(in)
	return r, p
}

// pickOfferWhy is pickOffer plus the rule that decided it, for the
// offer_choice log; the offer and plan it returns are identical to pickOffer.
func pickOfferWhy(in pickInput) (offercost.Ranked, cachePlan, offerDecision) {
	var hit, hitRanked, plain *offercost.Ranked
	var vol store.CacheVolume
	for i := range in.ranked {
		r := in.ranked[i]
		if v, ok := in.ready[r.Offer.MachineID]; ok {
			h, ok := in.hit[r.Offer.ID]
			if !ok {
				h = r
			}
			if hit == nil || h.Breakdown.SessionUSD < hit.Breakdown.SessionUSD {
				hit, hitRanked, vol = &h, &in.ranked[i], v
			}
		} else if plain == nil {
			plain = &in.ranked[i]
		}
	}
	// The hit wins on its estimate without the download, but keeps the
	// ranked download fee so a failed checksum's re-download is budgeted.
	if hit != nil && (plain == nil || prefers(*hit, *plain, in.threshold)) {
		d := offerDecision{rule: "cached_within_threshold", threshold: in.threshold, prefUSD: hit.Breakdown.SessionUSD.USD()}
		if plain != nil {
			d.otherUSD, d.hasCompare = plain.Breakdown.SessionUSD.USD(), true
		}
		return *hitRanked, cachePlan{mode: "hit", volumeID: vol.VolumeID}, d
	}
	// A hit that lost keeps "uncached_cheaper" no matter which uncached
	// offer the scan below settles on: it is the uncached side that won.
	lost := hit != nil
	var lostDecision offerDecision
	if lost {
		lostDecision = offerDecision{rule: "uncached_cheaper", threshold: in.threshold, prefUSD: hit.Breakdown.SessionUSD.USD(), otherUSD: plain.Breakdown.SessionUSD.USD(), hasCompare: true}
	}
	var seed, other *offercost.Ranked
	var vo provider.VolumeOffer
	for i := range in.ranked {
		r := &in.ranked[i]
		if _, cached := in.ready[r.Offer.MachineID]; cached {
			continue
		}
		if o, ok := in.seedable[r.Offer.MachineID]; ok {
			if seed == nil {
				seed, vo = r, o
			}
		} else if other == nil {
			other = r
		}
	}
	if seed != nil && (other == nil || prefers(*seed, *other, in.threshold)) {
		d := offerDecision{rule: "seedable_within_threshold", threshold: in.threshold, prefUSD: seed.Breakdown.SessionUSD.USD()}
		if other != nil {
			d.otherUSD, d.hasCompare = other.Breakdown.SessionUSD.USD(), true
		}
		if lost {
			d = lostDecision
		}
		return *seed, cachePlan{mode: "seed", volumeOfferID: vo.ID, storageUSD: vo.StoragePerGBMonth.USD()}, d
	}
	if other != nil {
		d := offerDecision{rule: "no_seedable"}
		if seed != nil {
			d = offerDecision{rule: "unseedable_cheaper", threshold: in.threshold, prefUSD: seed.Breakdown.SessionUSD.USD(), otherUSD: other.Breakdown.SessionUSD.USD(), hasCompare: true}
		}
		if lost {
			d = lostDecision
		}
		return *other, cachePlan{}, d
	}
	return in.ranked[0], cachePlan{}, offerDecision{rule: "cheapest"}
}

// cacheChoice never fails the search: a store or provider error only
// removes the cache from the choice.
func (c *Controller) cacheChoice(ctx context.Context, ranked []offercost.Ranked, modelBytes int64, disk int) (offercost.Ranked, cachePlan, offerChoiceLog) {
	cfg := c.deps.Config.Cache
	if cfg.MaxCaches == 0 {
		return ranked[0], cachePlan{}, offerChoiceLog{decision: offerDecision{rule: "cheapest"}, machines: allMachinesReason(ranked, reasonCacheDisabled)}
	}
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil {
		return ranked[0], cachePlan{}, offerChoiceLog{decision: offerDecision{rule: "cheapest"}, machines: allMachinesReason(ranked, reasonStoreLoadFailed)}
	}
	ready := usableReady(doc, c.deps.ModelURL, c.deps.ModelSHA256)
	in := pickInput{ranked: ranked, ready: ready, hit: map[int64]offercost.Ranked{}, seedable: map[int64]provider.VolumeOffer{}, threshold: cfg.PreferThreshold}
	horizon := horizon(c.deps.Config)
	for _, r := range ranked {
		if _, ok := ready[r.Offer.MachineID]; ok {
			if h := offercost.Rank([]provider.Offer{r.Offer}, offercost.Inputs{ModelBytes: 0, Horizon: horizon, DiskGB: disk}); len(h) == 1 {
				h[0].Breakdown.ModelBytes = modelBytes
				in.hit[r.Offer.ID] = h[0]
			}
		}
	}
	status := map[int64]machineCache{}
	for m := range ready {
		status[m] = machineCache{reason: reasonCached}
	}
	size := volumeSizeGB(modelBytes)
	if hasSeedRoom(doc, cfg.MaxCaches) {
		occupied := occupiedMachines(doc)
		// A seed rents the first ranked offer on its machine, so that offer's
		// download fee is spent with the volume's storage.
		downloadUSD := map[int64]float64{}
		var machines []int64
		for _, r := range ranked {
			m := r.Offer.MachineID
			if _, seen := downloadUSD[m]; seen {
				continue
			}
			if occupied[m] {
				if _, cached := status[m]; !cached {
					status[m] = machineCache{reason: reasonHasOurVolume}
				}
				continue
			}
			downloadUSD[m] = r.Breakdown.DownloadUSD.USD()
			machines = append(machines, m)
		}
		offers, err := c.deps.Provider.SearchVolumeOffers(ctx, machines, size)
		if err != nil {
			c.warn("controller: volume offer search failed", "err", err)
			for _, m := range machines {
				status[m] = machineCache{reason: reasonSearchFailed}
			}
		}
		for _, o := range offers {
			dl, asked := downloadUSD[o.MachineID]
			if !asked {
				continue
			}
			price := o.StoragePerGBMonth.USD()
			_, seeded := in.seedable[o.MachineID]
			if price > cfg.MaxStoragePriceUSDPerGBMonth {
				if !seeded {
					status[o.MachineID] = machineCache{reason: reasonOverPriceCap, priceUSD: price}
				}
				continue
			}
			pending := float64(size)*price*cfg.Retention.Hours()/730 + dl
			if c.deps.Budget.Exhausted(doc, pending) {
				if !seeded {
					status[o.MachineID] = machineCache{reason: reasonOverBudget, priceUSD: price}
				}
				continue
			}
			if cur, ok := in.seedable[o.MachineID]; !ok || o.StoragePerGBMonth < cur.StoragePerGBMonth {
				in.seedable[o.MachineID] = o
				status[o.MachineID] = machineCache{reason: reasonSeedable, volumeOfferID: o.ID, priceUSD: price, sizeGB: o.DiskSpaceGB}
			}
		}
		for _, m := range machines {
			if _, ok := status[m]; !ok {
				status[m] = machineCache{reason: reasonNoVolumeOffer}
			}
		}
	} else {
		for _, r := range ranked {
			if _, ok := status[r.Offer.MachineID]; !ok {
				status[r.Offer.MachineID] = machineCache{reason: reasonNoSeedRoom}
			}
		}
	}
	choice, plan, decision := pickOfferWhy(in)
	plan.sizeGB, plan.modelURL, plan.sha256 = size, c.deps.ModelURL, c.deps.ModelSHA256
	return choice, plan, offerChoiceLog{decision: decision, machines: status}
}

// occupiedMachines holds every machine with a live volume of ours, so a
// second volume is never created on one machine.
func occupiedMachines(doc store.Document) map[int64]bool {
	out := map[int64]bool{}
	if doc.Cache == nil {
		return out
	}
	for _, v := range doc.Cache.Volumes {
		if v.State == store.CacheSeeding || v.State == store.CacheReady {
			out[v.MachineID] = true
		}
	}
	return out
}

// hasSeedRoom is true when a slot is free or a ready volume not in use
// can be replaced.
func hasSeedRoom(doc store.Document, maxCaches int) bool {
	if doc.Cache == nil {
		return true
	}
	live, replaceable := 0, false
	for _, v := range doc.Cache.Volumes {
		if v.State == store.CacheSeeding || v.State == store.CacheReady {
			live++
			if v.State == store.CacheReady && v.InUseGen == 0 {
				replaceable = true
			}
		}
	}
	return live < maxCaches || replaceable
}
