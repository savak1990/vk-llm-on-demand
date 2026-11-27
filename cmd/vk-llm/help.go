package main

import (
	"fmt"
	"io"
)

const usageLine = "usage: vk-llm <start|status|stop|stop-all|budget|rentals|version|help> [flags]"

var commands = []struct{ name, usage, summary string }{
	{"start", "vk-llm start [--detach] [--timeout <duration>] [--json]", "rent a GPU and wait until the model is ready"},
	{"status", "vk-llm status [--json]", "show the GPU state, cost and idle shutdown"},
	{"stop", "vk-llm stop [--detach] [--timeout <duration>] [--json]", "finish the requests in flight and release the GPU"},
	{"stop-all", "vk-llm stop-all [--force] [--detach] [--timeout <duration>] [--json]", "stop the GPU and delete every cache volume"},
	{"budget", "vk-llm budget [set <amount>] [--json]", "show the monthly budget and spend, or set the budget"},
	{"rentals", "vk-llm rentals [--json]", "list the GPU rentals with their stage times"},
	{"version", "vk-llm version", "print the CLI version"},
	{"help", "vk-llm help [<command>]", "show help for one command"},
}

const stopAllHelp = `vk-llm stop-all - stop the GPU and release every cache volume

Usage:
  vk-llm stop-all [--force] [--detach] [--timeout <duration>] [--json]

Stops a running GPU the same way as ` + "`vk-llm stop`" + ` (it finishes the requests in
flight first), then deletes every model cache volume on Vast.ai, including the
one that GPU used. It waits until nothing that costs money is left.

Afterwards the controller works as normal: the next start rents a GPU and can
create a new cache volume. To stay at zero spend:
  - set config.cache.maxCaches: 0 in the chart values (no new cache volumes), or
  - run ` + "`vk-llm budget set 0.01`" + ` (no new starts until you raise it again).

Flags:
  --force            Skip the drain and cut requests in flight
                     (only if the installation allows a forced stop).
  --detach           Return after the request; do not wait.
  --timeout <dur>    How long to wait for the GPU to stop (default as for stop);
                     the volumes get 2 more minutes.
  --json             Print the final status document as JSON.

Examples:
  # Release everything and wait until Vast.ai bills nothing:
  vk-llm stop-all

  # The same against the dev installation:
  vk-llm --config ~/.config/vk-llm/dev.yaml stop-all

  # Cut requests in flight, do not wait:
  vk-llm stop-all --force --detach

  # Release everything, then block new starts until you raise the budget:
  vk-llm stop-all && vk-llm budget set 0.01

Exit codes: 0 everything released; 1 an error or the timeout passed with
something left; 2 bad usage; 3 the token was refused.
`

// printHelp serves "vk-llm help [cmd]" and "-h" anywhere; it needs no
// config, so it works before an endpoint or token is set up.
func printHelp(stdout, stderr io.Writer, sub string) int {
	if sub == "" {
		_, _ = fmt.Fprintf(stdout, "%s\n\nCommands:\n", usageLine)
		for _, c := range commands {
			_, _ = fmt.Fprintf(stdout, "  %-8s  %s\n", c.name, c.summary)
		}
		_, _ = fmt.Fprint(stdout, "\nGlobal flags: --endpoint <url>, --token-env <name>, --config <path>\n"+
			"Run `vk-llm help <command>` for one command.\n")
		return 0
	}
	if sub == "stop-all" {
		_, _ = fmt.Fprint(stdout, stopAllHelp)
		return 0
	}
	for _, c := range commands {
		if c.name == sub {
			_, _ = fmt.Fprintf(stdout, "vk-llm %s - %s\n\nUsage:\n  %s\n", c.name, c.summary, c.usage)
			return 0
		}
	}
	_, _ = fmt.Fprintf(stderr, "unknown subcommand: %s\n", sub)
	return 2
}
