package budget

import (
	"errors"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// Config holds every value this package needs; none are hard-coded.
type Config struct {
	MonthlyUSD        float64
	MaxMonthlyUSD     float64
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
	if c.Exhausted(doc, 0) {
		return "budget_exhausted"
	}
	if !known || balance.USD() < 2*c.cfg.MaxHourlyPriceUSD {
		return "balance_low"
	}
	return ""
}

// Exhausted reports whether month-to-date spend plus a charge not yet on
// the ledger (an offer's model download) reaches the monthly cap.
func (c *Checker) Exhausted(doc store.Document, pendingUSD float64) bool {
	return c.EstimatedMTD(doc)+pendingUSD >= c.CapUSD(doc)
}

// UsedRatio never denies by itself; it exists for a future metric.
func (c *Checker) UsedRatio(doc store.Document) float64 {
	limit := c.CapUSD(doc)
	if limit == 0 {
		return 0
	}
	return c.EstimatedMTD(doc) / limit
}

// CapUSD is the enforced monthly cap: an operator-set value wins over the
// configured default, and neither may exceed the configured ceiling.
func (c *Checker) CapUSD(doc store.Document) float64 {
	v := c.cfg.MonthlyUSD
	if doc.BudgetMonthUSD != nil {
		v = *doc.BudgetMonthUSD
	}
	if c.cfg.MaxMonthlyUSD > 0 && v > c.cfg.MaxMonthlyUSD {
		v = c.cfg.MaxMonthlyUSD
	}
	return v
}

func (c *Checker) CeilingUSD() float64 { return c.cfg.MaxMonthlyUSD }

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
