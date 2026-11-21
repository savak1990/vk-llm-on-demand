package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
)

func budgetDoc(budgetUSD float64) string {
	return fmt.Sprintf(`{"state":"STOPPED","cost":{"estimatedMonthUSD":12.34,"budgetMonthUSD":%v}}`, budgetUSD)
}

func TestBudgetShow(t *testing.T) {
	f := newFake(t)
	f.script("GET /api/v1/gpu/status", fakeResponse{code: 200, body: budgetDoc(30)})
	var stdout, stderr bytes.Buffer
	code := run([]string{"budget"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, fake.New(time.Unix(0, 0)))
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{"Budget  $30.00 / month", "Spent   $12.34 this month (estimate)"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout %q does not contain %q", stdout.String(), want)
		}
	}
}

func TestBudgetSet(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/budget", fakeResponse{code: 200, body: budgetDoc(40)})
	var stdout, stderr bytes.Buffer
	code := run([]string{"budget", "set", "40"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, fake.New(time.Unix(0, 0)))
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	calls := f.callsFor("POST /api/v1/gpu/budget")
	if len(calls) != 1 || calls[0].body != `{"monthlyUSD":40}` {
		t.Fatalf("calls = %+v, want one call with body {\"monthlyUSD\":40}", calls)
	}
	if !strings.Contains(stdout.String(), "Budget  $40.00 / month") {
		t.Fatalf("stdout = %q, want the new budget", stdout.String())
	}
}

func TestBudgetSetRejectsBadAmount(t *testing.T) {
	for _, amount := range []string{"x", "0", "-5", "NaN", "Inf"} {
		f := newFake(t)
		var stdout, stderr bytes.Buffer
		code := run([]string{"budget", "set", amount}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, fake.New(time.Unix(0, 0)))
		if code != 2 {
			t.Fatalf("amount %q: exit = %d, want 2", amount, code)
		}
		if n := f.callCount("POST /api/v1/gpu/budget"); n != 0 {
			t.Fatalf("amount %q: %d requests made, want 0", amount, n)
		}
	}
}

func TestBudgetSetAboveCeilingExits1(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/budget", fakeResponse{code: 400,
		body: `{"error":{"code":"budget_above_ceiling","message":"monthlyUSD must not exceed the configured ceiling of $100.00"}}`})
	var stdout, stderr bytes.Buffer
	code := run([]string{"budget", "set", "150"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, fake.New(time.Unix(0, 0)))
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "budget_above_ceiling") || !strings.Contains(stderr.String(), "$100.00") {
		t.Fatalf("stderr = %q, want code and ceiling", stderr.String())
	}
}

func TestBudgetBadForm(t *testing.T) {
	for _, args := range [][]string{{"budget", "set"}, {"budget", "raise", "40"}, {"budget", "set", "40", "extra"}} {
		f := newFake(t)
		var stdout, stderr bytes.Buffer
		if code := run(args, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, fake.New(time.Unix(0, 0))); code != 2 {
			t.Fatalf("%v: exit = %d, want 2", args, code)
		}
	}
}

func TestOtherSubcommandsRejectExtraArgs(t *testing.T) {
	for _, sub := range []string{"status", "start", "stop", "version"} {
		f := newFake(t)
		var stdout, stderr bytes.Buffer
		if code := run([]string{sub, "extra"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, fake.New(time.Unix(0, 0))); code != 2 {
			t.Fatalf("%s extra: exit = %d, want 2", sub, code)
		}
		if !strings.Contains(stderr.String(), "unexpected argument: extra") {
			t.Fatalf("%s extra: stderr = %q, want unexpected argument", sub, stderr.String())
		}
	}
}
