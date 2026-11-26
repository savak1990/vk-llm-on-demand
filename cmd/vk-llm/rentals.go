package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

type rentalDoc struct {
	Gen          int64            `json:"gen"`
	StartedAt    time.Time        `json:"startedAt"`
	ReadyAt      *time.Time       `json:"readyAt"`
	Outcome      string           `json:"outcome"`
	Geo          string           `json:"geo"`
	DPH          float64          `json:"dph"`
	Stages       map[string]int64 `json:"stages"`
	DownloadMBps float64          `json:"downloadMBps"`
}

type rentalsDoc struct {
	Rentals []rentalDoc `json:"rentals"`
}

func cmdRentals(c *client, stdout, stderr io.Writer, jsonOut bool) int {
	res, code, err := c.rentals(context.Background())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return code
	}
	if jsonOut {
		renderJSON(stdout, res.raw)
		return 0
	}
	var doc rentalsDoc
	if err := json.Unmarshal(res.raw, &doc); err != nil {
		_, _ = fmt.Fprintln(stderr, "decode rentals:", err)
		return 1
	}
	renderRentals(stdout, doc.Rentals)
	return 0
}

func renderRentals(w io.Writer, rentals []rentalDoc) {
	if len(rentals) == 0 {
		_, _ = fmt.Fprintln(w, "No rentals recorded yet.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "GEN\tSTARTED\tWHERE\t$/H\tBOOT\tDOWNLOAD\tLOAD\tTOTAL\tOUTCOME")
	for _, r := range rentals {
		download := stageMMSS(r.Stages, "download")
		if download != "-" && r.DownloadMBps > 0 {
			download += fmt.Sprintf(" %.1fMB/s", r.DownloadMBps)
		}
		total := "-"
		if r.ReadyAt != nil {
			total = mmss(int64(r.ReadyAt.Sub(r.StartedAt).Round(time.Second) / time.Second))
		}
		outcome := r.Outcome
		if outcome == "" {
			outcome = "starting"
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%.3f\t%s\t%s\t%s\t%s\t%s\n",
			r.Gen, r.StartedAt.UTC().Format("01-02 15:04")+" UTC", r.Geo, r.DPH,
			stageMMSS(r.Stages, "boot"), download, stageMMSS(r.Stages, "load"), total, outcome)
	}
	_ = tw.Flush()
}

func stageMMSS(stages map[string]int64, name string) string {
	v, ok := stages[name]
	if !ok {
		return "-"
	}
	return mmss(v)
}

func mmss(seconds int64) string {
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
