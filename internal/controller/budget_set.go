package controller

import (
	"context"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// SetBudgetMonthUSD stores an operator-chosen monthly cap. !(usd > 0)
// also rejects NaN, which would disable Allow's >= comparison; Transition
// reloads the document per attempt, so no stale write can overwrite it.
func (c *Controller) SetBudgetMonthUSD(ctx context.Context, usd float64) (Snapshot, error) {
	if !(usd > 0) {
		return c.Status(), ErrBudgetInvalid
	}
	if ceiling := c.deps.Budget.CeilingUSD(); ceiling > 0 && usd > ceiling {
		return c.Status(), ErrBudgetAboveCeiling
	}
	if _, err := store.Transition(ctx, c.deps.Store, func(d *store.Document) error {
		d.BudgetMonthUSD = &usd
		return nil
	}); err != nil {
		return c.Status(), err
	}
	return c.Status(), nil
}
