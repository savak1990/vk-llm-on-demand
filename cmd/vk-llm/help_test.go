package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
)

func runHelp(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, func(string) string { return "" }, &stdout, &stderr, fake.New(time.Unix(0, 0)))
	return code, stdout.String(), stderr.String()
}

func TestHelpStopAll(t *testing.T) {
	for _, args := range [][]string{{"help", "stop-all"}, {"stop-all", "--help"}, {"stop-all", "-h"}} {
		code, out, errOut := runHelp(t, args...)
		if code != 0 || out != stopAllHelp || errOut != "" {
			t.Fatalf("%v: exit %d stderr %q stdout:\n%s", args, code, errOut, out)
		}
	}
	if !strings.HasPrefix(stopAllHelp, "vk-llm stop-all - stop the GPU and release every cache volume\n") ||
		!strings.Contains(stopAllHelp, "vk-llm budget set 0.01") || !strings.Contains(stopAllHelp, "config.cache.maxCaches: 0") {
		t.Fatalf("stop-all help lost its content:\n%s", stopAllHelp)
	}
	if strings.Contains(stopAllHelp, "--force") || !strings.Contains(stopAllHelp, "  # Release everything, do not wait:\n  vk-llm stop-all --detach\n") ||
		!strings.Contains(stopAllHelp, "the volumes get 3 more minutes.") {
		t.Fatalf("stop-all help must drop --force, show the detach example and the 3-minute window:\n%s", stopAllHelp)
	}
	for i, line := range strings.Split(stopAllHelp, "\n") {
		if len(line) > 80 {
			t.Errorf("line %d is %d columns: %q", i+1, len(line), line)
		}
		for _, r := range line {
			if r > 0x7f {
				t.Errorf("line %d has non-ASCII %q: %q", i+1, r, line)
			}
		}
	}
}

func TestHelpGeneral(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		code, out, _ := runHelp(t, args...)
		if code != 0 || !strings.HasPrefix(out, usageLine+"\n") ||
			!strings.Contains(out, "  stop-all  stop the GPU and delete every cache volume\n") ||
			!strings.Contains(out, "vk-llm help <command>") {
			t.Fatalf("%v: exit %d stdout:\n%s", args, code, out)
		}
	}
}

func TestHelpOtherCommand(t *testing.T) {
	want := "vk-llm status - show the GPU state, cost and idle shutdown\n\nUsage:\n  vk-llm status [--json]\n"
	for _, args := range [][]string{{"status", "-h"}, {"help", "status"}} {
		code, out, _ := runHelp(t, args...)
		if code != 0 || out != want {
			t.Fatalf("%v: exit %d stdout %q, want %q", args, code, out, want)
		}
	}
	if _, out, _ := runHelp(t, "help", "stop"); !strings.Contains(out, "  vk-llm stop [--force] [--detach]") {
		t.Fatalf("help stop = %q, want --force in its usage", out)
	}
	if code, _, errOut := runHelp(t, "help", "frobnicate"); code != 2 || !strings.Contains(errOut, "unknown subcommand: frobnicate") {
		t.Fatalf("help frobnicate: exit %d stderr %q, want 2", code, errOut)
	}
}
