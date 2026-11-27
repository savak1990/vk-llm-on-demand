package progress

import (
	"strings"
	"testing"
)

func TestTrackerSequence(t *testing.T) {
	var p Tracker
	gb := int64(1_000_000_000)
	steps := []State{
		{Public: "ALLOCATING"},
		{Public: "ALLOCATING"},
		{Public: "STARTING", MachineID: 117871, GPUName: "RTX 6000Ada", Geolocation: "Sweden", HourlyUSD: 0.55},
		{Public: "LOADING_MODEL", MachineID: 117871, ModelBytes: 28 * gb},
		{Public: "LOADING_MODEL", MachineID: 117871, ModelBytes: 28 * gb, DownloadedBytes: 9 * gb},
		{Public: "LOADING_MODEL", MachineID: 117871, ModelBytes: 28 * gb, DownloadedBytes: 28 * gb, WorkerStages: []string{"download_end"}},
		{Public: "LOADING_MODEL", MachineID: 117871, ModelBytes: 28 * gb, DownloadedBytes: 28 * gb, WorkerStages: []string{"download_end", "server_spawn"}},
		{Public: "READY", Ready: true},
	}
	var got []string
	for _, s := range steps {
		got = append(got, p.Lines(s)...)
	}
	want := []string{
		"Looking for a GPU offer…",
		"Renting an RTX 6000Ada in Sweden at $0.55/h (machine 117871); booting the worker…",
		"Worker up; downloading the model (28.0 GB)…",
		"Downloading the model: 30% (9.0 of 28.0 GB)",
		"Download done; verifying the checksum…",
		"Checksum OK; loading the model into the GPU…",
		"Ready.",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("lines =\n%q\nwant\n%q", got, want)
	}
}

func TestTrackerWithoutModelSize(t *testing.T) {
	var p Tracker
	p.Lines(State{Public: "LOADING_MODEL"})
	got := p.Lines(State{Public: "LOADING_MODEL", DownloadedBytes: 4_100_000_000})
	if len(got) != 1 || got[0] != "Downloading the model: 4 GB so far" {
		t.Fatalf("lines = %q", got)
	}
}

func TestLinesCacheHit(t *testing.T) {
	var tr Tracker
	out := tr.Lines(State{Public: "LOADING_MODEL", CacheHit: true, DownloadedBytes: 5e9, ModelBytes: 28e9})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "The model is cached on this host; checking it (sha256)") || strings.Contains(joined, "Downloading the model") {
		t.Fatalf("lines = %q", out)
	}
}
