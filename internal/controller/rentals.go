package controller

import (
	"math"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

type rentalInput struct {
	now         time.Time
	searchStart time.Time
	outcome     string
	worker      *workerStages
}

type workerStages struct {
	gen    int64
	stages map[string]int64
	mbps   float64
}

// rentalChange names the controller stage a committed transition set, so
// the caller observes it once even though Transition can rerun the mutate.
type rentalChange struct {
	stage string
	secs  int64
}

func withRental(mutate func(*store.Document) error, in rentalInput, out *rentalChange) func(*store.Document) error {
	return func(d *store.Document) error {
		*out = rentalChange{}
		var before *store.Instance
		if d.Instance != nil {
			cp := *d.Instance
			before = &cp
		}
		beforeGen := d.Generation
		if err := mutate(d); err != nil {
			return err
		}
		*out = updateRental(d, before, beforeGen, in)
		return nil
	}
}

// updateRental derives the change from the instance before and after the
// transition, so it holds for every transition without naming each one.
func updateRental(d *store.Document, before *store.Instance, beforeGen int64, in rentalInput) rentalChange {
	if d.Instance != nil && d.Generation != beforeGen {
		return openRental(d, in)
	}
	r := findRental(d, beforeGen)
	if r == nil || before == nil {
		return rentalChange{}
	}
	if in.outcome != "" && in.outcome != "ok" && r.Outcome == "" {
		r.Outcome = in.outcome
	}
	after := d.Instance
	if after == nil {
		endRental(d, r, in)
		return rentalChange{}
	}
	switch {
	case before.ProviderID == "" && after.ProviderID != "":
		return markStage(r, "create", in.now)
	case before.SSHHost == "" && after.SSHHost != "":
		return markStage(r, "boot", in.now)
	case before.State != Loading.String() && after.State == Loading.String():
		return markStage(r, "link", in.now)
	case before.ReadyAt == nil && after.ReadyAt != nil:
		ready := *after.ReadyAt
		r.ReadyAt = &ready
		if r.Outcome == "" {
			r.Outcome = "ready"
		}
		ch := markStage(r, "ready", in.now)
		r.MarkAt = nil
		return ch
	}
	return rentalChange{}
}

func openRental(d *store.Document, in rentalInput) rentalChange {
	start := in.searchStart
	if start.IsZero() || start.After(in.now) {
		start = in.now
	}
	origin := d.StartOrigin
	if origin == "" {
		origin = "explicit"
	}
	o := d.Instance.Offer
	r := store.Rental{
		Gen: d.Generation, Origin: origin, StartedAt: start,
		MachineID: o.MachineID, Geo: o.Geo, GPU: o.GPU, DPH: o.DPH, InetDownMbps: o.InetDown,
	}
	if o.Estimate != nil {
		r.EstDownloadSeconds, r.ModelBytes = o.Estimate.DownloadSeconds, o.Estimate.ModelBytes
	}
	mark := start
	r.MarkAt = &mark
	ch := markStage(&r, "search", in.now)
	d.Rentals = append(d.Rentals, r)
	return ch
}

func endRental(d *store.Document, r *store.Rental, in rentalInput) {
	end := in.now
	r.EndedAt, r.MarkAt = &end, nil
	for _, e := range d.Ledger.Entries {
		if e.Gen == r.Gen && e.End != nil {
			r.CostUSD = math.Round(e.EstUSD*1e4) / 1e4
		}
	}
	if r.Outcome == "" {
		r.Outcome = "ended"
		if op := findOp(d, createOpKey(r.Gen)); op != nil && op.Outcome != "" && op.Outcome != "created" {
			r.Outcome = op.Outcome
		}
	}
	if w := in.worker; w != nil && w.gen == r.Gen {
		if r.Stages == nil {
			r.Stages = map[string]int64{}
		}
		for k, v := range w.stages {
			r.Stages[k] = v
		}
		r.DownloadMBps = w.mbps
	}
}

func markStage(r *store.Rental, stage string, now time.Time) rentalChange {
	if r.MarkAt == nil {
		return rentalChange{}
	}
	secs := wholeSeconds(now.Sub(*r.MarkAt))
	if r.Stages == nil {
		r.Stages = map[string]int64{}
	}
	r.Stages[stage] = secs
	mark := now
	r.MarkAt = &mark
	return rentalChange{stage: stage, secs: secs}
}

func findRental(d *store.Document, gen int64) *store.Rental {
	for i := len(d.Rentals) - 1; i >= 0; i-- {
		if d.Rentals[i].Gen == gen {
			return &d.Rentals[i]
		}
	}
	return nil
}

func wholeSeconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(math.Round(d.Seconds()))
}
