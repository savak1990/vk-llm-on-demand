package budget

import (
	"math"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// MaxLedgerEntries bounds the ConfigMap document's size; with one
// instance at a time this is months of history, not a real limit.
const MaxLedgerEntries = 100

// estUSD rounds elapsed time up to the next whole second — never
// understate a deny-gate input — before applying the hourly rate.
func estUSD(dph float64, start, now time.Time) float64 {
	seconds := math.Ceil(now.Sub(start).Seconds())
	return dph * seconds / 3600
}

func (c *Checker) OpenEntry(doc *store.Document, now time.Time, gen int64, dphUSD float64) {
	doc.Ledger.Entries = append(doc.Ledger.Entries, store.LedgerEntry{Gen: gen, Start: now, DPH: dphUSD})
}

func (c *Checker) RefreshOpenEntry(doc *store.Document, now time.Time) {
	for i := range doc.Ledger.Entries {
		e := &doc.Ledger.Entries[i]
		if e.End == nil {
			e.EstUSD = estUSD(e.DPH, e.Start, now)
		}
	}
}

func (c *Checker) CloseEntry(doc *store.Document, now time.Time, gen int64) {
	for i := range doc.Ledger.Entries {
		e := &doc.Ledger.Entries[i]
		if e.Gen == gen && e.End == nil {
			e.EstUSD = estUSD(e.DPH, e.Start, now)
			t := now
			e.End = &t
			break
		}
	}
	foldOverflow(doc)
}

// foldOverflow removes the oldest closed entries once the list exceeds
// MaxLedgerEntries, adding each one's EstUSD into TotalUSD first. It
// never removes an open entry.
func foldOverflow(doc *store.Document) {
	for len(doc.Ledger.Entries) > MaxLedgerEntries {
		idx := -1
		for i, e := range doc.Ledger.Entries {
			if e.End != nil {
				idx = i
				break
			}
		}
		if idx == -1 {
			return
		}
		doc.Ledger.TotalUSD += doc.Ledger.Entries[idx].EstUSD
		doc.Ledger.Entries = append(doc.Ledger.Entries[:idx], doc.Ledger.Entries[idx+1:]...)
	}
}

// Rollover fires at most once per call, when now's UTC month differs
// from the stored one. A still-open entry survives the boundary with a
// fresh Start — its pre-rollover partial cost is dropped.
func (c *Checker) Rollover(doc *store.Document, now time.Time) {
	month := now.UTC().Format("2006-01")
	if doc.Ledger.Month == month {
		return
	}
	var stillOpen []store.LedgerEntry
	for _, e := range doc.Ledger.Entries {
		if e.End == nil {
			stillOpen = append(stillOpen, store.LedgerEntry{Gen: e.Gen, Start: now, DPH: e.DPH})
		}
	}
	doc.Ledger.Month = month
	doc.Ledger.TotalUSD = 0
	doc.Ledger.Entries = stillOpen
}
