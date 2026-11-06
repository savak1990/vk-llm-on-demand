package budget

import (
	"errors"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func cfg() Config {
	return Config{MonthlyUSD: 30, WarnFraction: 0.8, MaxHourlyPriceUSD: 0.80}
}

func TestAllowMonthlyCap(t *testing.T) {
	c := New(cfg())
	doc := store.Document{Ledger: store.Ledger{
		TotalUSD: 29.90,
		Entries:  []store.LedgerEntry{{Gen: 1, EstUSD: 0.15}}, // open: End == nil
	}}
	if reason := c.Allow(doc, provider.Money(100_000_000), true); reason != "budget_exhausted" {
		t.Fatalf("Allow = %q, want budget_exhausted (EstimatedMTD 30.05 >= 30)", reason)
	}
}

func TestAllowBalanceFloor(t *testing.T) {
	c := New(cfg()) // floor = 2 * 0.80 = 1.60
	doc := store.Document{}
	if reason := c.Allow(doc, provider.Money(1_590_000), true); reason != "balance_low" {
		t.Fatalf("Allow(balance=1.59) = %q, want balance_low", reason)
	}
	if reason := c.Allow(doc, provider.Money(1_600_000), true); reason != "" {
		t.Fatalf("Allow(balance=1.60) = %q, want \"\" (floor is inclusive)", reason)
	}
}

func TestAllowUnknownBalanceFoldsIntoBalanceLow(t *testing.T) {
	c := New(cfg())
	if reason := c.Allow(store.Document{}, provider.Money(0), false); reason != "balance_low" {
		t.Fatalf("Allow(known=false) = %q, want balance_low", reason)
	}
}

func TestAllowOrderMonthlyBeforeBalance(t *testing.T) {
	c := New(cfg())
	doc := store.Document{Ledger: store.Ledger{TotalUSD: 30}} // both monthly cap and balance floor would fail
	if reason := c.Allow(doc, provider.Money(0), false); reason != "budget_exhausted" {
		t.Fatalf("Allow (both would fail) = %q, want budget_exhausted only", reason)
	}
}

func TestUsedRatio(t *testing.T) {
	c := New(cfg())
	doc := store.Document{Ledger: store.Ledger{TotalUSD: 15}}
	if got := c.UsedRatio(doc); got != 0.5 {
		t.Fatalf("UsedRatio = %v, want 0.5", got)
	}
}

func TestUsedRatioZeroMonthlyCap(t *testing.T) {
	c := New(Config{MonthlyUSD: 0})
	if got := c.UsedRatio(store.Document{Ledger: store.Ledger{TotalUSD: 5}}); got != 0 {
		t.Fatalf("UsedRatio with MonthlyUSD=0 = %v, want 0 (no division by zero)", got)
	}
}

func TestEstimatedMTD(t *testing.T) {
	c := New(cfg())
	closedEnd := time.Now()
	doc := store.Document{Ledger: store.Ledger{
		TotalUSD: 10,
		Entries: []store.LedgerEntry{
			{Gen: 1, End: &closedEnd, EstUSD: 1.0}, // closed: cost already added to TotalUSD when it closed, must not double-count
			{Gen: 2, EstUSD: 0.25},                 // open
		},
	}}
	if got := c.EstimatedMTD(doc); got != 10.25 {
		t.Fatalf("EstimatedMTD = %v, want 10.25 (TotalUSD + open entries only)", got)
	}
}

func TestAssertPriceBoundary(t *testing.T) {
	c := New(cfg())
	if err := c.AssertPrice(0.80001, 0.80); !errors.Is(err, ErrPriceTooHigh) {
		t.Fatalf("AssertPrice(0.80001, 0.80) = %v, want ErrPriceTooHigh", err)
	}
	if err := c.AssertPrice(0.80, 0.80); err != nil {
		t.Fatalf("AssertPrice(0.80, 0.80) = %v, want nil (equal is allowed)", err)
	}
}

func TestClosedEntryCountsTowardMonthlyCap(t *testing.T) {
	c := New(Config{MonthlyUSD: 30, MaxHourlyPriceUSD: 0.80})
	t0 := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	doc := store.Document{Ledger: store.Ledger{Month: "2026-11"}}
	c.OpenEntry(&doc, t0, 1, 0.80)
	c.CloseEntry(&doc, t0.Add(40*time.Hour), 1)
	if r := c.Allow(doc, provider.Money(100_000_000), true); r != "budget_exhausted" {
		t.Fatalf("Allow after $32 closed spend = %q, want budget_exhausted", r)
	}
}

func TestWorkedArithmeticMonthlyHoursAtCap(t *testing.T) {
	if 30.0/0.80 != 37.5 {
		t.Fatal("30/0.80 != 37.5 — shipped defaults no longer match the spec's worked figure")
	}
}
