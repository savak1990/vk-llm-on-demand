package controller

import (
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// Budget is the subset of the future internal/budget.Checker this package
// calls. Allow returns "" when spending may continue, or one of this
// package's own Unavailable reasons otherwise.
type Budget interface {
	Allow(doc store.Document, balance provider.Money, known bool) string
	AssertPrice(hourlyUSD, capUSD float64) error
	OpenEntry(doc *store.Document, now time.Time, gen int64, dphUSD float64)
	CloseEntry(doc *store.Document, now time.Time, gen int64)
	Rollover(doc *store.Document, now time.Time)
	RefreshOpenEntry(doc *store.Document, now time.Time)
	UsedRatio(doc store.Document) float64
	EstimatedMTD(doc store.Document) float64
}
