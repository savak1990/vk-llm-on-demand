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

	// Provider
	_, _ = fmt.Fprintf(tw, "Provider\t%s\n", doc.Provider)

	// State
	state := doc.State
	if doc.Desired != "" && doc.Desired != stateImplied(doc.State) {
		state = fmt.Sprintf("%s (desired %s)", state, doc.Desired)
	}
	_, _ = fmt.Fprintf(tw, "State\t%s\n", state)

	// GPU
	if doc.Instance == nil {
		_, _ = fmt.Fprintf(tw, "GPU\t—\n")
	} else {
		_, _ = fmt.Fprintf(tw, "GPU\t%s\n", doc.Instance.GPU)
	}

	// Model
	if doc.Model.Alias == "" {
		_, _ = fmt.Fprintf(tw, "Model\t—\n")
	} else {
		_, _ = fmt.Fprintf(tw, "Model\t%s (%d tokens)\n", doc.Model.Alias, doc.Model.ContextSize)
	}

	// Ready
	readyStr := "no"
	if doc.Ready {
		readyStr = "yes"
	}
	_, _ = fmt.Fprintf(tw, "Ready\t%s\n", readyStr)

	// Price
	if doc.Instance == nil {
		_, _ = fmt.Fprintf(tw, "Price\t—\n")
	} else {
		_, _ = fmt.Fprintf(tw, "Price\t$%.2f/h\n", doc.Instance.PriceUSDPerHour)
	}

	// Uptime
	if doc.UptimeSeconds == 0 {
		_, _ = fmt.Fprintf(tw, "Uptime\t0s\n")
	} else {
		_, _ = fmt.Fprintf(tw, "Uptime\t%s\n", formatDuration(doc.UptimeSeconds))
	}

	// Est. cost
	costStr := fmt.Sprintf("$%.2f this instance, $%.2f / $%.2f this month (estimate)",
		doc.Cost.EstimatedInstanceUSD, doc.Cost.EstimatedMonthUSD, doc.Cost.BudgetMonthUSD)
	_, _ = fmt.Fprintf(tw, "Est. cost\t%s\n", costStr)

	// Last inference
	if doc.LastInferenceFinishedAt == nil {
		_, _ = fmt.Fprintf(tw, "Last inference\tnever\n")
	} else {
		_, _ = fmt.Fprintf(tw, "Last inference\t%s\n", doc.LastInferenceFinishedAt.Format(time.DateTime))
	}

	// Idle shutdown in
	if doc.SecondsUntilIdleShutdown == nil {
		_, _ = fmt.Fprintf(tw, "Idle shutdown in\t—\n")
	} else {
		_, _ = fmt.Fprintf(tw, "Idle shutdown in\t%s\n", formatDuration(*doc.SecondsUntilIdleShutdown))
	}

	// Unavailable
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
		return fmt.Sprintf("%dh%dm%ds", hours, minutes, secs)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm%ds", minutes, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

func stateImplied(state string) string {
	// Per CORE-055, there's no explicit mapping of state -> desired,
	// but we compare them only when desired differs.
	// This helper is here for symmetry; the comparison happens in renderStatus.
	return state
}
