package controller

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/budget"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func withCeiling(c *Controller) {
	c.deps.Budget = budget.New(budget.Config{MonthlyUSD: 30, MaxMonthlyUSD: 100, MaxHourlyPriceUSD: 0.80})
}

func putSpent(t *testing.T, c *Controller, spent float64) {
	t.Helper()
	doc, _ := store.NewDocument("iid")
	doc.Ledger.TotalUSD = spent
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)
}

func TestSetBudgetMonthUSDStoresValueAndKeepsLedger(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	withCeiling(c)
	putSpent(t, c, 30)
	ctx := context.Background()

	snap, err := c.SetBudgetMonthUSD(ctx, 40)
	if err != nil {
		t.Fatalf("SetBudgetMonthUSD: %v", err)
	}
	if snap.BudgetMonthUSD != 40 || snap.BudgetCeilingUSD != 100 {
		t.Fatalf("snap budget = %v ceiling = %v, want 40 and 100", snap.BudgetMonthUSD, snap.BudgetCeilingUSD)
	}
	doc, _, _ := c.deps.Store.Load(ctx)
	if doc.BudgetMonthUSD == nil || *doc.BudgetMonthUSD != 40 {
		t.Fatalf("stored BudgetMonthUSD = %v, want 40", doc.BudgetMonthUSD)
	}
	if doc.Ledger.TotalUSD != 30 {
		t.Fatalf("Ledger.TotalUSD = %v, want 30 (unchanged)", doc.Ledger.TotalUSD)
	}
}

func TestSetBudgetMonthUSDRejects(t *testing.T) {
	for _, tc := range []struct {
		usd  float64
		want error
	}{
		{0, ErrBudgetInvalid},
		{-5, ErrBudgetInvalid},
		{math.NaN(), ErrBudgetInvalid},
		{150, ErrBudgetAboveCeiling},
	} {
		c, _, _, _, _ := newTestController(t)
		withCeiling(c)
		ctx := context.Background()
		if _, err := c.SetBudgetMonthUSD(ctx, tc.usd); !errors.Is(err, tc.want) {
			t.Fatalf("SetBudgetMonthUSD(%v) err = %v, want %v", tc.usd, err, tc.want)
		}
		doc, _, _ := c.deps.Store.Load(ctx)
		if doc.BudgetMonthUSD != nil {
			t.Fatalf("SetBudgetMonthUSD(%v) stored %v, want nothing stored", tc.usd, *doc.BudgetMonthUSD)
		}
	}
}

func TestRaisedBudgetUnblocksNextStart(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	withCeiling(c)
	putSpent(t, c, 30)
	ctx := context.Background()

	if _, err := c.Start(ctx); !errors.Is(err, ErrGPUUnavailable) {
		t.Fatalf("Start at the $30 cap: err = %v, want ErrGPUUnavailable", err)
	}
	if snap := c.Status(); snap.Unavailable != "budget_exhausted" {
		t.Fatalf("Unavailable = %q, want budget_exhausted", snap.Unavailable)
	}
	if _, err := c.SetBudgetMonthUSD(ctx, 40); err != nil {
		t.Fatalf("SetBudgetMonthUSD: %v", err)
	}
	if _, err := c.Start(ctx); err != nil {
		t.Fatalf("Start after raising the cap: %v", err)
	}
}

func TestStatusBudgetDefaultsToConfig(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	if snap := c.Status(); snap.BudgetMonthUSD != 30 {
		t.Fatalf("BudgetMonthUSD = %v, want 30 (configured default, nothing stored)", snap.BudgetMonthUSD)
	}
}
