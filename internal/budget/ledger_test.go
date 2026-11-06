package budget

import (
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func TestRoundUpToSecond(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	doc := store.Document{Ledger: store.Ledger{Entries: []store.LedgerEntry{{Gen: 1, Start: t0, DPH: 0.71}}}}
	c := New(Config{})

	c.RefreshOpenEntry(&doc, t0.Add(89600*time.Millisecond)) // 89.6s
	got1 := doc.Ledger.Entries[0].EstUSD
	c.RefreshOpenEntry(&doc, t0.Add(90*time.Second)) // 90.0s
	got2 := doc.Ledger.Entries[0].EstUSD

	want := 0.71 * 90 / 3600
	if got1 != want || got2 != want {
		t.Fatalf("EstUSD at 89.6s=%v 90.0s=%v, want both %v (round up to 90 whole seconds)", got1, got2, want)
	}
}

func TestOpenRefreshClose(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	doc := store.Document{}
	c := New(Config{})

	c.OpenEntry(&doc, t0, 7, 0.60)
	if len(doc.Ledger.Entries) != 1 || doc.Ledger.Entries[0].Gen != 7 || doc.Ledger.Entries[0].End != nil {
		t.Fatalf("OpenEntry: Entries = %+v", doc.Ledger.Entries)
	}

	c.RefreshOpenEntry(&doc, t0.Add(time.Hour))
	if doc.Ledger.Entries[0].EstUSD != 0.60 {
		t.Fatalf("RefreshOpenEntry after 1h: EstUSD = %v, want 0.60", doc.Ledger.Entries[0].EstUSD)
	}

	c.CloseEntry(&doc, t0.Add(2*time.Hour), 7)
	e := doc.Ledger.Entries[0]
	if e.End == nil || !e.End.Equal(t0.Add(2*time.Hour)) || e.EstUSD != 1.20 {
		t.Fatalf("CloseEntry after 2h: End=%v EstUSD=%v, want 1.20", e.End, e.EstUSD)
	}
	c.CloseEntry(&doc, t0.Add(3*time.Hour), 7) // repeat close of the same gen
	if doc.Ledger.TotalUSD != 1.20 {
		t.Fatalf("TotalUSD = %v, want 1.20 (added once at close, never twice)", doc.Ledger.TotalUSD)
	}
}

func TestCloseEntryDoesNothingForUnknownGen(t *testing.T) {
	doc := store.Document{Ledger: store.Ledger{Entries: []store.LedgerEntry{{Gen: 1, Start: time.Now(), DPH: 0.5}}}}
	c := New(Config{})
	c.CloseEntry(&doc, time.Now(), 99) // no entry with Gen 99
	if doc.Ledger.Entries[0].End != nil {
		t.Fatalf("CloseEntry(unknown gen) mutated the existing entry: %+v", doc.Ledger.Entries[0])
	}
}

func TestFoldOnOverflow(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	c := New(Config{})
	doc := store.Document{Ledger: store.Ledger{Month: "2026-11"}}

	for gen := int64(1); gen <= 100; gen++ {
		start := t0.Add(time.Duration(gen) * time.Hour)
		doc.Ledger.Entries = append(doc.Ledger.Entries, store.LedgerEntry{
			Gen: gen, Start: start, End: &start, DPH: 1.0, EstUSD: 0.01,
		})
		doc.Ledger.TotalUSD += 0.01 // CloseEntry would already have added this; fold must not re-add it
	}
	totalBeforeFold := doc.Ledger.TotalUSD

	c.OpenEntry(&doc, t0.Add(101*time.Hour), 101, 0.71)
	c.CloseEntry(&doc, t0.Add(101*time.Hour+time.Minute), 101)
	newEntryCost := doc.Ledger.Entries[len(doc.Ledger.Entries)-1].EstUSD

	if len(doc.Ledger.Entries) != 100 {
		t.Fatalf("len(Entries) = %d, want 100 after folding", len(doc.Ledger.Entries))
	}
	wantTotal := totalBeforeFold + newEntryCost
	if doc.Ledger.TotalUSD != wantTotal {
		t.Fatalf("TotalUSD = %v, want %v (fold removes the array entry only; its cost was already added when it closed)", doc.Ledger.TotalUSD, wantTotal)
	}
	last := doc.Ledger.Entries[len(doc.Ledger.Entries)-1]
	if last.Gen != 101 || last.End == nil {
		t.Fatalf("just-closed entry (gen 101) missing or still open: %+v", last)
	}
	for _, e := range doc.Ledger.Entries {
		if e.Gen == 1 {
			t.Fatal("oldest entry (gen 1) should have been folded away, found it still present")
		}
	}
}

func TestRolloverDropsClosedEntries(t *testing.T) {
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	closedStart := now.Add(-time.Hour)
	doc := store.Document{Ledger: store.Ledger{
		Month: "2026-11", TotalUSD: 12.34,
		Entries: []store.LedgerEntry{{Gen: 4, Start: closedStart, End: &closedStart, DPH: 0.5, EstUSD: 0.50}},
	}}
	c := New(Config{})
	c.Rollover(&doc, now)

	if doc.Ledger.Month != "2026-12" || doc.Ledger.TotalUSD != 0 {
		t.Fatalf("Rollover = %+v, want Month=2026-12 TotalUSD=0", doc.Ledger)
	}
	if len(doc.Ledger.Entries) != 0 {
		t.Fatalf("Entries = %+v, want empty (closed entry dropped)", doc.Ledger.Entries)
	}
}

func TestRolloverReopensRunningEntry(t *testing.T) {
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	oldStart := time.Date(2026, 11, 30, 20, 0, 0, 0, time.UTC)
	doc := store.Document{Ledger: store.Ledger{
		Month: "2026-11", TotalUSD: 12.34,
		Entries: []store.LedgerEntry{{Gen: 5, Start: oldStart, DPH: 0.71}}, // End == nil: still open
	}}
	c := New(Config{})
	c.Rollover(&doc, now)

	if doc.Ledger.Month != "2026-12" || doc.Ledger.TotalUSD != 0 {
		t.Fatalf("Rollover = %+v, want Month=2026-12 TotalUSD=0", doc.Ledger)
	}
	if len(doc.Ledger.Entries) != 1 {
		t.Fatalf("Entries = %+v, want exactly one (the still-open entry, re-opened)", doc.Ledger.Entries)
	}
	e := doc.Ledger.Entries[0]
	if e.Gen != 5 || e.DPH != 0.71 || e.End != nil || !e.Start.Equal(now) || e.EstUSD != 0 {
		t.Fatalf("re-opened entry = %+v, want Gen=5 DPH=0.71 End=nil Start=%v EstUSD=0", e, now)
	}
}

func TestRolloverNoOpWithinSameMonth(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	doc := store.Document{Ledger: store.Ledger{Month: "2026-11", TotalUSD: 5}}
	c := New(Config{})
	c.Rollover(&doc, now)
	if doc.Ledger.TotalUSD != 5 {
		t.Fatalf("Rollover within the same month changed TotalUSD to %v, want unchanged 5", doc.Ledger.TotalUSD)
	}
}
