package controller

import (
	"context"
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
	// A pod restart during Loading recovers into Booting, so the second
	// EvLinkUp that follows must not re-observe or overwrite a stage already recorded.
	if _, ok := r.Stages[stage]; ok {
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

// summarizeWorkerStages turns worker-clock stage times into whole-second
// stages; the speed uses fetch time only, so sha256 does not lower it.
func summarizeWorkerStages(times map[string]time.Time, modelBytes int64) (map[string]int64, float64) {
	stages := map[string]int64{}
	var mbps float64
	if _, ok := times["download_skipped"]; ok {
		stages["download"] = 0
	} else if begin, ok := times["download_begin"]; ok {
		if v, ok := times["verified"]; ok {
			stages["download"] = wholeSeconds(v.Sub(begin))
		}
		if end, ok := times["download_end"]; ok && end.After(begin) && modelBytes > 0 {
			mbps = math.Round(float64(modelBytes)/1e6/end.Sub(begin).Seconds()*10) / 10
		}
	}
	if spawn, ok := times["server_spawn"]; ok {
		if healthy, ok := times["server_healthy"]; ok {
			stages["load"] = wholeSeconds(healthy.Sub(spawn))
		}
	}
	return stages, mbps
}

func (c *Controller) workerStagesFor(gen int64) *workerStages {
	c.workerStagesMu.Lock()
	defer c.workerStagesMu.Unlock()
	if c.workerStages == nil || c.workerStages.gen != gen {
		return nil
	}
	return c.workerStages
}

// workerStageReadAttempts bounds the wait for server_healthy: the worker
// only stamps it inside its own poll loop, so the read right after READY
// commonly misses it and must be retried a few times, not failed.
const workerStageReadAttempts = 4

// readWorkerStages caches the latest reading after every successful read,
// not only the last one, so a stop that tears the connection down between
// retries still keeps whatever a prior read already found; the histogram
// and the log line use only that final cached reading, once.
func (c *Controller) readWorkerStages(ctx context.Context, gen int64) {
	doc, _, err := c.deps.Store.Load(ctx)
	if err != nil {
		c.warn("controller: worker stage store load failed", "gen", gen, "err", err)
		return
	}
	r := findRental(&doc, gen)
	if r == nil {
		return
	}

	var stages map[string]int64
	var mbps float64
	var read bool
attempts:
	for attempt := 0; attempt < workerStageReadAttempts; attempt++ {
		c.mu.Lock()
		live := c.generation == gen && c.conn != nil
		conn := c.conn
		c.mu.Unlock()
		if !live || ctx.Err() != nil {
			break
		}
		rctx, cancel := context.WithTimeout(ctx, progressReadTimeout)
		p, err := c.deps.Worker.Progress(rctx, conn.Exec)
		cancel()
		if err != nil {
			c.warn("controller: worker stage read failed", "gen", gen, "err", err)
			break
		}
		stages, mbps = summarizeWorkerStages(p.Times, r.ModelBytes)
		read = true
		c.workerStagesMu.Lock()
		c.workerStages = &workerStages{gen: gen, stages: stages, mbps: mbps}
		c.workerStagesMu.Unlock()
		if _, ok := p.Times["server_healthy"]; ok || attempt == workerStageReadAttempts-1 {
			break
		}
		select {
		case <-c.deps.Clock.After(pollInterval):
		case <-ctx.Done():
			break attempts
		}
	}
	if !read {
		return
	}
	for k, v := range stages {
		coldstartStageSeconds.WithLabelValues(k).Observe(float64(v))
	}
	c.logRentalReady(*r, stages, mbps)
}

func (c *Controller) logRentalReady(r store.Rental, worker map[string]int64, mbps float64) {
	if c.deps.Log == nil {
		return
	}
	args := []any{"gen", r.Gen, "origin", r.Origin, "machine_id", r.MachineID, "geo", r.Geo, "gpu", r.GPU,
		"dph", r.DPH, "inet_down_mbps", r.InetDownMbps, "download_mbps", mbps}
	for _, name := range []string{"search", "create", "boot", "link", "download", "load", "ready"} {
		v, ok := r.Stages[name]
		if w, wok := worker[name]; wok {
			v, ok = w, true
		}
		if ok {
			args = append(args, "stage_"+name+"_s", v)
		}
	}
	if r.ReadyAt != nil {
		args = append(args, "total_s", wholeSeconds(r.ReadyAt.Sub(r.StartedAt)))
	}
	c.deps.Log.Info("rental_ready", args...)
}
