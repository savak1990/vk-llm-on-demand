// Package offercost estimates what one session on an offer costs and
// ranks offers by that estimate. It does no I/O and reads no clock.
package offercost

import (
	"math"
	"sort"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

const (
	diskFactor     = 1.15
	diskOverheadGB = 15 // worker image and logs
)

type Inputs struct {
	ModelBytes int64
	Horizon    time.Duration
	DiskGB     int
}

type Breakdown struct {
	DiskGB          int
	ModelBytes      int64
	HorizonSeconds  int64
	DownloadSeconds int64
	GPUHourly       provider.Money
	StorageHourly   provider.Money
	DownloadUSD     provider.Money
	SessionUSD      provider.Money
}

type Ranked struct {
	Offer     provider.Offer
	Breakdown Breakdown
}

func DiskGB(modelBytes int64, minDiskGB int) int {
	need := int(math.Ceil(float64(modelBytes)/1e9*diskFactor)) + diskOverheadGB
	return max(need, minDiskGB)
}

// estimate rounds every money term up, so a ranking input never
// understates what the session will be billed. Caller must guard InetDownMbps > 0.
func estimate(o provider.Offer, in Inputs) Breakdown {
	dlSec := int64(math.Ceil(float64(in.ModelBytes) * 8 / (o.InetDownMbps * 1e6)))
	hours := (in.Horizon.Seconds() + float64(dlSec)) / 3600
	dlUSD := provider.Money(math.Ceil(float64(in.ModelBytes) / 1e9 * float64(o.DownloadPerGB)))
	gpu, storage := o.Hourly, provider.Money(0)
	if o.DphBase > 0 && o.DphBase <= o.Hourly {
		gpu, storage = o.DphBase, o.Hourly-o.DphBase
	}
	return Breakdown{
		DiskGB: in.DiskGB, ModelBytes: in.ModelBytes, HorizonSeconds: int64(in.Horizon.Seconds()),
		DownloadSeconds: dlSec, GPUHourly: gpu, StorageHourly: storage, DownloadUSD: dlUSD,
		SessionUSD: provider.Money(math.Ceil(float64(o.Hourly)*hours)) + dlUSD,
	}
}

// Rank drops offers whose speed is unknown and sorts the rest by session
// cost, then hourly price, then id, so the order is total.
func Rank(offers []provider.Offer, in Inputs) []Ranked {
	out := make([]Ranked, 0, len(offers))
	for _, o := range offers {
		if o.InetDownMbps <= 0 {
			continue
		}
		out = append(out, Ranked{Offer: o, Breakdown: estimate(o, in)})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Breakdown.SessionUSD != b.Breakdown.SessionUSD {
			return a.Breakdown.SessionUSD < b.Breakdown.SessionUSD
		}
		if a.Offer.Hourly != b.Offer.Hourly {
			return a.Offer.Hourly < b.Offer.Hourly
		}
		return a.Offer.ID < b.Offer.ID
	})
	return out
}
