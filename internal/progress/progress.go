// Package progress builds the milestone lines shown while a GPU starts,
// shared by the gateway's chat progress stream and the vk-llm CLI.
package progress

import (
	"fmt"
	"slices"
)

// State is what the milestone lines are built from; the gateway fills it
// from controller.Snapshot and the CLI from the status document.
type State struct {
	Public          string
	MachineID       int64
	GPUName         string
	Geolocation     string
	HourlyUSD       float64
	DownloadedBytes int64
	ModelBytes      int64
	WorkerStages    []string
	Ready           bool
	CacheHit        bool
}

// Tracker emits each milestone once, in order, from successive states.
type Tracker struct {
	sent    map[string]bool
	lastPct int
	lastGB  int64
}

func (t *Tracker) once(key string, cond bool, text string, out []string) []string {
	if t.sent == nil {
		t.sent = map[string]bool{}
	}
	if !cond || t.sent[key] {
		return out
	}
	t.sent[key] = true
	return append(out, text)
}

func gbOf(b int64) float64 { return float64(b) / 1e9 }

// Lines returns the milestone texts newly reached since the last call,
// in order, with no prefix; the caller adds whatever prefix it needs.
func (t *Tracker) Lines(s State) []string {
	var out []string
	out = t.once("alloc", s.Public == "ALLOCATING" && s.MachineID == 0, "Looking for a GPU offer…", out)
	where := ""
	if s.Geolocation != "" {
		where = " in " + s.Geolocation
	}
	out = t.once("offer", s.MachineID != 0 && s.GPUName != "",
		fmt.Sprintf("Renting an %s%s at $%.2f/h (machine %d); booting the worker…", s.GPUName, where, s.HourlyUSD, s.MachineID), out)
	size := ""
	if s.ModelBytes > 0 {
		size = fmt.Sprintf(" (%.1f GB)", gbOf(s.ModelBytes))
	}
	loading := "Worker up; downloading the model" + size + "…"
	if s.CacheHit {
		loading = "Worker up. The model is cached on this host; checking it (sha256)…"
	}
	out = t.once("loading", s.Public == "LOADING_MODEL", loading, out)
	downloaded := slices.Contains(s.WorkerStages, "download_end")
	if !s.CacheHit && s.Public == "LOADING_MODEL" && !downloaded && s.DownloadedBytes > 0 {
		if s.ModelBytes > 0 {
			pct := int(min(s.DownloadedBytes*100/s.ModelBytes, 99)) / 10 * 10
			if pct > t.lastPct {
				t.lastPct = pct
				out = append(out, fmt.Sprintf("Downloading the model: %d%% (%.1f of %.1f GB)", pct, gbOf(s.DownloadedBytes), gbOf(s.ModelBytes)))
			}
		} else if gb := s.DownloadedBytes / 2e9 * 2; gb > t.lastGB {
			t.lastGB = gb
			out = append(out, fmt.Sprintf("Downloading the model: %d GB so far", gb))
		}
	}
	out = t.once("verify", downloaded, "Download done; verifying the checksum…", out)
	out = t.once("spawn", slices.Contains(s.WorkerStages, "server_spawn"), "Checksum OK; loading the model into the GPU…", out)
	return t.once("ready", s.Ready, "Ready.", out)
}
