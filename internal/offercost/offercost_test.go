package offercost

import (
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

// Prices from the 2026-11-22 probe (machine 40508) and the run-1 bill.
var (
	run1   = provider.Offer{ID: 1, MachineID: 117871, Hourly: 550000, DphBase: 530000, DownloadPerGB: 12153, InetDownMbps: 1000}
	m40508 = provider.Offer{ID: 2, MachineID: 40508, Hourly: 615556, DphBase: 560000, DownloadPerGB: 39063, InetDownMbps: 889.4}
	qwenIn = Inputs{ModelBytes: 29_200_000_000, Horizon: 30 * time.Minute, DiskGB: 60}
)

func TestEstimateMatchesHandComputedValues(t *testing.T) {
	cases := []struct {
		name string
		o    provider.Offer
		want Breakdown
	}{
		{"run 1 machine", run1, Breakdown{DiskGB: 60, ModelBytes: 29_200_000_000, HorizonSeconds: 1800, DownloadSeconds: 234,
			GPUHourly: 530000, StorageHourly: 20000, DownloadUSD: 354868, SessionUSD: 665618}},
		{"machine 40508", m40508, Breakdown{DiskGB: 60, ModelBytes: 29_200_000_000, HorizonSeconds: 1800, DownloadSeconds: 263,
			GPUHourly: 560000, StorageHourly: 55556, DownloadUSD: 1140640, SessionUSD: 1493388}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := estimate(c.o, qwenIn); got != c.want {
				t.Fatalf("estimate =\n %+v\nwant\n %+v", got, c.want)
			}
		})
	}
}

func TestRankPrefersCheaperSessionOverCheaperGPU(t *testing.T) {
	got := Rank([]provider.Offer{m40508, run1}, qwenIn)
	if len(got) != 2 || got[0].Offer.MachineID != 117871 {
		t.Fatalf("Rank first = %+v, want machine 117871", got[0].Offer)
	}
	if got[0].Breakdown != estimate(run1, qwenIn) {
		t.Fatalf("Rank breakdown differs from estimate")
	}
}

func TestRankPutsLowerSessionBeforeLowerHourly(t *testing.T) {
	a := provider.Offer{ID: 1, Hourly: 600000, DownloadPerGB: 39063, InetDownMbps: 1000}
	b := provider.Offer{ID: 2, Hourly: 650000, DownloadPerGB: 0, InetDownMbps: 1000}
	if a.Hourly >= b.Hourly {
		t.Fatalf("fixture intent broken: a.Hourly %d >= b.Hourly %d", a.Hourly, b.Hourly)
	}
	aBreak := estimate(a, qwenIn)
	bBreak := estimate(b, qwenIn)
	if bBreak.SessionUSD >= aBreak.SessionUSD {
		t.Fatalf("fixture intent broken: b.SessionUSD %d >= a.SessionUSD %d", bBreak.SessionUSD, aBreak.SessionUSD)
	}
	got := Rank([]provider.Offer{a, b}, qwenIn)
	if len(got) != 2 || got[0].Offer.ID != 2 {
		t.Fatalf("Rank first = %+v, want id 2", got[0].Offer)
	}
}

func TestRankDropsUnknownSpeed(t *testing.T) {
	slow := run1
	slow.InetDownMbps = 0
	if got := Rank([]provider.Offer{slow}, qwenIn); len(got) != 0 {
		t.Fatalf("Rank kept an offer with no speed: %+v", got)
	}
}

func TestRankTieBreaksByHourlyThenID(t *testing.T) {
	in := Inputs{ModelBytes: 1_000_000_000, Horizon: time.Hour, DiskGB: 60}
	a := provider.Offer{ID: 9, Hourly: 500000, InetDownMbps: 1000}
	b := provider.Offer{ID: 3, Hourly: 500000, InetDownMbps: 1000}
	got := Rank([]provider.Offer{a, b}, in)
	if got[0].Offer.ID != 3 {
		t.Fatalf("tie order = %d first, want id 3", got[0].Offer.ID)
	}
}

func TestEstimateWithoutDphBaseCountsAllAsGPU(t *testing.T) {
	o := provider.Offer{ID: 1, Hourly: 600000, InetDownMbps: 1000}
	got := estimate(o, qwenIn)
	if got.GPUHourly != 600000 || got.StorageHourly != 0 {
		t.Fatalf("GPU/storage = %d/%d, want 600000/0", got.GPUHourly, got.StorageHourly)
	}
}

func TestDiskGB(t *testing.T) {
	cases := []struct {
		bytes int64
		min   int
		want  int
	}{
		{28_600_000_000, 60, 60}, // 48 computed, the floor wins
		{70_000_000_000, 60, 96},
		{1_000_000_000, 0, 17},
	}
	for _, c := range cases {
		if got := DiskGB(c.bytes, c.min); got != c.want {
			t.Fatalf("DiskGB(%d, %d) = %d, want %d", c.bytes, c.min, got, c.want)
		}
	}
}
