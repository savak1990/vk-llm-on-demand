package gpucontrol

import (
	"strings"
	"testing"
)

const budgetStatus = `{"provider":"vastai","state":"STOPPED","ready":false,"cost":{"estimatedMonthUSD":12.34,"budgetMonthUSD":40}}`

func TestBudgetShowCallsStatus(t *testing.T) {
	f := newFake(t)
	f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: budgetStatus})
	r := runScript(t, map[string]string{"ACTION": "budget", "ENDPOINT": f.URL()})
	if r.exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", r.exitCode, r.stderr)
	}
	if n := len(f.callLog("POST /api/v1/gpu/budget")); n != 0 {
		t.Fatalf("POST budget calls = %d, want 0 for the read form", n)
	}
	if !strings.Contains(r.stdout, "estimatedMonthUSD=12.34") {
		t.Fatalf("stdout = %q, want the status row", r.stdout)
	}
	if !strings.Contains(r.stdout, "budgetMonthUSD=40") {
		t.Fatalf("stdout = %q, want budgetMonthUSD=40", r.stdout)
	}
}

func TestBudgetSetPostsBody(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/budget", fakeResp{code: 200, body: budgetStatus})
	r := runScript(t, map[string]string{"ACTION": "budget", "ENDPOINT": f.URL(), "MONTHLY_USD": "40"})
	if r.exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", r.exitCode, r.stderr)
	}
	calls := f.callLog("POST /api/v1/gpu/budget")
	if len(calls) != 1 || calls[0].reqBody != `{"monthlyUSD":40}` {
		t.Fatalf("calls = %+v, want one call with body {\"monthlyUSD\":40}", calls)
	}
	if !strings.Contains(r.stdout, "budgetMonthUSD=40") {
		t.Fatalf("stdout = %q, want budgetMonthUSD=40", r.stdout)
	}
}

func TestBudgetSetRejectsNonNumeric(t *testing.T) {
	for _, v := range []string{"abc", "-5", "1e3", "40;rm"} {
		f := newFake(t)
		r := runScript(t, map[string]string{"ACTION": "budget", "ENDPOINT": f.URL(), "MONTHLY_USD": v})
		if r.exitCode != 2 {
			t.Fatalf("MONTHLY_USD=%q: exit = %d, want 2", v, r.exitCode)
		}
		if n := len(f.callLog("POST /api/v1/gpu/budget")); n != 0 {
			t.Fatalf("MONTHLY_USD=%q: %d requests, want 0", v, n)
		}
	}
}

func TestBudgetAboveCeilingShowsMessage(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/budget", fakeResp{code: 400,
		body: `{"error":{"code":"budget_above_ceiling","message":"monthlyUSD must not exceed the configured ceiling of $100.00"}}`})
	r := runScript(t, map[string]string{"ACTION": "budget", "ENDPOINT": f.URL(), "MONTHLY_USD": "150"})
	if r.exitCode != 1 {
		t.Fatalf("exit = %d, want 1", r.exitCode)
	}
	if !strings.Contains(r.stderr, "budget_above_ceiling") || !strings.Contains(r.stderr, "$100.00") {
		t.Fatalf("stderr = %q, want code and ceiling", r.stderr)
	}
	if strings.Contains(r.stderr, `{"error"`) {
		t.Fatalf("stderr dumped the raw body: %q", r.stderr)
	}
}
