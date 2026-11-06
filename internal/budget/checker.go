package budget

import (
	"errors"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// Config holds every value this package needs; none are hard-coded.
type Config struct {
	MonthlyUSD        float64
	WarnFraction      float64
	MaxHourlyPriceUSD float64
}

type Checker struct {
	cfg Config
}

// New builds a Checker. No clock — every time-sensitive method takes
// now as a parameter from its caller.
func New(cfg Config) *Checker {
	return &Checker{cfg: cfg}
}

var ErrPriceTooHigh = errors.New("budget: offer price above lifecycle.safety.maxHourlyPriceUSD")

// Allow checks the monthly cap before the balance floor and returns on
// the first failure — never both reasons at once.
func (c *Checker) Allow(doc store.Document, balance provider.Money, known bool) string {
	if c.EstimatedMTD(doc) >= c.cfg.MonthlyUSD {
		return "budget_exhausted"
	}
	if !known || balance.USD() < 2*c.cfg.MaxHourlyPriceUSD {
		return "balance_low"
	}
	return ""
}

// UsedRatio never denies by itself; it exists for a future metric.
func (c *Checker) UsedRatio(doc store.Document) float64 {
	if c.cfg.MonthlyUSD == 0 {
		return 0
	}
	return c.EstimatedMTD(doc) / c.cfg.MonthlyUSD
}

// EstimatedMTD is the closed-entries total plus every open entry's
// current estimate; a closed entry's EstUSD is never added twice.
func (c *Checker) EstimatedMTD(doc store.Document) float64 {
	total := doc.Ledger.TotalUSD
	for _, e := range doc.Ledger.Entries {
		if e.End == nil {
			total += e.EstUSD
		}
	}
	return total
}

// AssertPrice re-checks the price cap immediately before CreateInstance,
// closing the race between a search-time offer and the matching create.
func (c *Checker) AssertPrice(hourlyUSD, capUSD float64) error {
	if hourlyUSD > capUSD {
		return ErrPriceTooHigh
	}
	return nil
}
