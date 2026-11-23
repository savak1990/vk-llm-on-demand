package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/progress"
)

const (
	pollInterval       = 5 * time.Second
	defaultStopTimeout = 5 * time.Minute
)

// run is the only seam between main and the rest of the CLI: tests pass a
// fake env, buffers and a fake clock instead of the real ones.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer, c clock.Clock) int {
	fs := flag.NewFlagSet("vk-llm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	endpointFlag := fs.String("endpoint", "", "")
	tokenEnvFlag := fs.String("token-env", "", "")
	configFlag := fs.String("config", "", "")
	timeoutFlag := fs.String("timeout", "", "")
	detachFlag := fs.Bool("detach", false, "")
	jsonFlag := fs.Bool("json", false, "")
	fs.String("force", "", "")

	// Global flags are accepted before or after the subcommand name, so
	// keep re-parsing the same flag set until nothing but the subcommand
	// and its own positional arguments are left.
	var subArgs []string
	sub, rest := "", args
	for {
		if err := fs.Parse(rest); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		positional := fs.Args()
		if len(positional) == 0 {
			break
		}
		if sub == "" {
			sub = positional[0]
		} else {
			subArgs = append(subArgs, positional[0])
		}
		rest = positional[1:]
	}
	if sub != "budget" && len(subArgs) > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected argument: %s\n", subArgs[0])
		return 2
	}
	if sub == "" {
		_, _ = fmt.Fprintln(stderr, "usage: vk-llm <start|status|stop|budget|version> [flags]")
		return 2
	}
	if sub == "version" {
		return cmdVersion(stdout)
	}
	switch sub {
	case "start", "status", "stop", "budget":
	default:
		_, _ = fmt.Fprintf(stderr, "unknown subcommand: %s\n", sub)
		return 2
	}

	var loadArgs []string
	if *endpointFlag != "" {
		loadArgs = append(loadArgs, "--endpoint", *endpointFlag)
	}
	if *tokenEnvFlag != "" {
		loadArgs = append(loadArgs, "--token-env", *tokenEnvFlag)
	}
	if *configFlag != "" {
		loadArgs = append(loadArgs, "--config", *configFlag)
	}
	cfg, code, usage := loadConfig(loadArgs, getenv)
	if code != 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return code
	}
	cl := newClient(cfg)
	detach := *detachFlag || !cfg.WaitForReady

	switch sub {
	case "start":
		return cmdStart(cfg, cl, c, stdout, stderr, *timeoutFlag, detach, *jsonFlag)
	case "status":
		return cmdStatus(cl, stdout, stderr, *jsonFlag)
	case "stop":
		return cmdStop(cl, c, stdout, stderr, *timeoutFlag, detach, *jsonFlag)
	case "budget":
		return cmdBudget(cl, stdout, stderr, subArgs, *jsonFlag)
	default:
		return 2
	}
}

func cmdVersion(stdout io.Writer) int {
	_, _ = fmt.Fprintf(stdout, "vk-llm %s\n", version)
	return 0
}

func cmdBudget(c *client, stdout, stderr io.Writer, args []string, jsonOut bool) int {
	var res apiResult
	var code int
	var err error
	switch {
	case len(args) == 0:
		res, code, err = c.status(context.Background())
	case len(args) == 2 && args[0] == "set":
		usd, perr := strconv.ParseFloat(args[1], 64)
		if perr != nil || math.IsNaN(usd) || math.IsInf(usd, 0) || usd <= 0 {
			_, _ = fmt.Fprintf(stderr, "bad amount: %s (want a positive number)\n", args[1])
			return 2
		}
		res, code, err = c.setBudget(context.Background(), usd)
	default:
		_, _ = fmt.Fprintln(stderr, "usage: vk-llm budget [set <amount>]")
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return code
	}
	if jsonOut {
		renderJSON(stdout, res.raw)
	} else {
		renderBudget(stdout, res.doc)
	}
	return 0
}

func cmdStatus(c *client, stdout, stderr io.Writer, jsonOut bool) int {
	res, code, err := c.status(context.Background())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return code
	}
	if jsonOut {
		renderJSON(stdout, res.raw)
	} else {
		renderStatus(stdout, res.doc)
	}
	return 0
}

func cmdStart(cfg Config, c *client, clk clock.Clock, stdout, stderr io.Writer, timeoutStr string, detach, jsonOut bool) int {
	timeout := cfg.StartupTimeout
	if timeoutStr != "" {
		d, err := time.ParseDuration(timeoutStr)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "bad --timeout: %v\n", err)
			return 2
		}
		timeout = d
	}
	deadline := clk.Now().Add(timeout)

	ctx := context.Background()
	res, code, err := c.start(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return code
	}
	if detach {
		if jsonOut {
			renderJSON(stdout, res.raw)
		}
		return 0
	}
	if res.doc.State == "READY" {
		_, _ = fmt.Fprintf(stdout, "already running, idle shutdown in %d min\n", idleMinutes(res.doc))
		return 0
	}

	var tracker progress.Tracker
	var gen int64
	last := printStateChange(stderr, clk, "", res.doc.State)
	resetOnNewGeneration(&tracker, &gen, res.doc)
	printProgress(stderr, clk, &tracker, res.doc)
	wait := pollInterval
	for first := true; ; first = false {
		if !first {
			<-clk.After(wait)
			wait = pollInterval
		}
		if !clk.Now().Before(deadline) {
			printTimeout(stderr, timeout, last)
			return 1
		}

		res, code, err = c.status(ctx)
		if err != nil {
			if code == 3 {
				_, _ = fmt.Fprintln(stderr, err)
				return code
			}
			wait = retryWait(err)
			continue
		}

		switch {
		case res.doc.State == "READY":
			_, _ = fmt.Fprintf(stderr, "ready: %s on %s, idle shutdown in %d min\n", res.doc.Model.Alias, gpuName(res.doc), idleMinutes(res.doc))
			if jsonOut {
				renderJSON(stdout, res.raw)
			}
			return 0
		case res.doc.State == "STOPPED" && res.doc.Unavailable != "":
			_, _ = fmt.Fprintf(stderr, "start failed: %s\n", res.doc.Unavailable)
			if jsonOut {
				renderJSON(stdout, res.raw)
			}
			return 1
		default:
			last = printStateChange(stderr, clk, last, res.doc.State)
			resetOnNewGeneration(&tracker, &gen, res.doc)
			printProgress(stderr, clk, &tracker, res.doc)
		}
	}
}

func cmdStop(c *client, clk clock.Clock, stdout, stderr io.Writer, timeoutStr string, detach, jsonOut bool) int {
	timeout := defaultStopTimeout
	if timeoutStr != "" {
		d, err := time.ParseDuration(timeoutStr)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "bad --timeout: %v\n", err)
			return 2
		}
		timeout = d
	}
	deadline := clk.Now().Add(timeout)

	ctx := context.Background()
	res, code, err := c.stop(ctx, false)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return code
	}
	if detach {
		if jsonOut {
			renderJSON(stdout, res.raw)
		}
		return 0
	}
	if res.doc.State == "STOPPED" {
		_, _ = fmt.Fprintln(stdout, "already stopped")
		return 0
	}

	last := printStateChange(stderr, clk, "", res.doc.State)
	wait := pollInterval
	for first := true; ; first = false {
		if !first {
			<-clk.After(wait)
			wait = pollInterval
		}
		if !clk.Now().Before(deadline) {
			printTimeout(stderr, timeout, last)
			return 1
		}

		res, code, err = c.status(ctx)
		if err != nil {
			if code == 3 {
				_, _ = fmt.Fprintln(stderr, err)
				return code
			}
			wait = retryWait(err)
			continue
		}

		last = printStateChange(stderr, clk, last, res.doc.State)
		if res.doc.State == "STOPPED" {
			if jsonOut {
				renderJSON(stdout, res.raw)
			}
			return 0
		}
	}
}

// printStateChange prints "<HH:MM:SS> <STATE>" on stderr only when state
// differs from the last printed one, returning the new last-printed state.
func printStateChange(stderr io.Writer, clk clock.Clock, last, state string) string {
	if state == last {
		return last
	}
	_, _ = fmt.Fprintf(stderr, "%s %s\n", clk.Now().Format("15:04:05"), state)
	return state
}

// resetOnNewGeneration replaces the tracker when doc names a new,
// non-zero generation, so a restart's milestones are not suppressed by
// the previous generation's percentages and once-only lines.
func resetOnNewGeneration(tr *progress.Tracker, lastGen *int64, doc statusDoc) {
	if doc.Generation != 0 && doc.Generation != *lastGen {
		*tr = progress.Tracker{}
		*lastGen = doc.Generation
	}
}

func stateOf(doc statusDoc) progress.State {
	s := progress.State{Public: doc.State, Ready: doc.Ready}
	if doc.Instance != nil {
		s.MachineID, s.GPUName, s.Geolocation, s.HourlyUSD = doc.Instance.MachineID, doc.Instance.GPU, doc.Instance.Geolocation, doc.Instance.PriceUSDPerHour
	}
	if doc.Progress != nil {
		s.DownloadedBytes, s.ModelBytes, s.WorkerStages = doc.Progress.DownloadedBytes, doc.Progress.ModelBytes, doc.Progress.WorkerStages
	}
	return s
}

// printProgress skips a bare document (nothing new) and a
// draining/deleting/failed one (still carries the old instance); every
// other document's new milestone lines print after the state-change line.
func printProgress(stderr io.Writer, clk clock.Clock, tr *progress.Tracker, doc statusDoc) {
	switch doc.State {
	case "DRAINING", "DELETING", "FAILED":
		return
	}
	if doc.Instance == nil && doc.Progress == nil {
		return
	}
	for _, l := range tr.Lines(stateOf(doc)) {
		_, _ = fmt.Fprintf(stderr, "%s %s\n", clk.Now().Format("15:04:05"), l)
	}
}

func printTimeout(stderr io.Writer, timeout time.Duration, state string) {
	_, _ = fmt.Fprintf(stderr, "timed out after %s in state %s\n", timeout, state)
	_, _ = fmt.Fprintln(stderr, "the GPU is not stopped; run vk-llm stop, or it stops after its idle timeout")
}

// retryWait decides how long to wait before the next poll after a non-fatal
// client error: a 429's Retry-After if present (floored at pollInterval),
// otherwise the normal interval for a 5xx or network error.
func retryWait(err error) time.Duration {
	var ae *apiError
	if errors.As(err, &ae) && ae.RetryAfterSeconds > 0 {
		if d := time.Duration(ae.RetryAfterSeconds) * time.Second; d > pollInterval {
			return d
		}
	}
	return pollInterval
}

func idleMinutes(doc statusDoc) int64 {
	if doc.SecondsUntilIdleShutdown == nil {
		return 0
	}
	return *doc.SecondsUntilIdleShutdown / 60
}

func gpuName(doc statusDoc) string {
	if doc.Instance == nil {
		return ""
	}
	return doc.Instance.GPU
}
