package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

func renderStatus(w io.Writer, doc statusDoc) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer func() { _ = tw.Flush() }()

	_, _ = fmt.Fprintf(tw, "Provider\t%s\n", doc.Provider)

	state := doc.State
	if implied, ok := stateImplied(doc.State); ok && doc.Desired != "" && doc.Desired != implied {
		state = fmt.Sprintf("%s (desired %s)", state, doc.Desired)
	}
	_, _ = fmt.Fprintf(tw, "State\t%s\n", state)

	if doc.Instance == nil {
		_, _ = fmt.Fprintf(tw, "GPU\t—\n")
	} else {
		_, _ = fmt.Fprintf(tw, "GPU\t%s\n", doc.Instance.GPU)
	}

	_, _ = fmt.Fprintf(tw, "Model\t%s (%d tokens)\n", doc.Model.Alias, doc.Model.ContextSize)

	readyStr := "no"
	if doc.Ready {
		readyStr = "yes"
	}
	_, _ = fmt.Fprintf(tw, "Ready\t%s\n", readyStr)

	if doc.Instance == nil {
		_, _ = fmt.Fprintf(tw, "Price\t—\n")
	} else {
		_, _ = fmt.Fprintf(tw, "Price\t$%.2f/h\n", doc.Instance.PriceUSDPerHour)
	}

	if doc.UptimeSeconds == 0 {
		_, _ = fmt.Fprintf(tw, "Uptime\t0s\n")
	} else {
		_, _ = fmt.Fprintf(tw, "Uptime\t%s\n", formatDuration(doc.UptimeSeconds))
	}

	costStr := fmt.Sprintf("$%.2f this instance, $%.2f / $%.2f this month (estimate)",
		doc.Cost.EstimatedInstanceUSD, doc.Cost.EstimatedMonthUSD, doc.Cost.BudgetMonthUSD)
	_, _ = fmt.Fprintf(tw, "Est. cost\t%s\n", costStr)

	if doc.LastInferenceFinishedAt == nil {
		_, _ = fmt.Fprintf(tw, "Last inference\tnever\n")
	} else {
		_, _ = fmt.Fprintf(tw, "Last inference\t%s\n", doc.LastInferenceFinishedAt.Local().Format(time.DateTime))
	}

	if doc.SecondsUntilIdleShutdown == nil {
		_, _ = fmt.Fprintf(tw, "Idle shutdown in\t—\n")
	} else {
		_, _ = fmt.Fprintf(tw, "Idle shutdown in\t%s\n", formatDuration(*doc.SecondsUntilIdleShutdown))
	}

	if doc.Unavailable != "" {
		_, _ = fmt.Fprintf(tw, "Unavailable\t%s\n", doc.Unavailable)
	}
}

func renderJSON(w io.Writer, raw []byte) {
	_, _ = w.Write(raw)
}

func formatDuration(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	d -= minutes * time.Minute
	secs := d / time.Second

	if hours > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, secs)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm%02ds", minutes, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// Running/progressing states imply desired=RUNNING, stopping/stopped states
// imply desired=STOPPED; FAILED can occur under either desired value, so it
// implies neither (ok=false suppresses the mismatch display for it).
func stateImplied(state string) (desired string, ok bool) {
	switch state {
	case "ALLOCATING", "STARTING", "LOADING_MODEL", "READY":
		return "RUNNING", true
	case "STOPPED", "DRAINING", "DELETING":
		return "STOPPED", true
	default:
		return "", false
	}
}
