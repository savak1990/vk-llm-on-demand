package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var body map[string]map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error body: %v (%s)", err, rec.Body.String())
	}
	return body["error"]["code"], body["error"]["message"]
}

func TestBudgetSetReturnsStatusDocument(t *testing.T) {
	fc := newFakeController()
	fc.budgetSnap = controller.Snapshot{BudgetMonthUSD: 40}
	rec := doReq(newTestServer(t, fc), "POST", "/api/v1/gpu/budget", `{"monthlyUSD":40}`)
	if rec.Code != 200 {
		t.Fatalf("code = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var doc statusDocument
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Cost.BudgetMonthUSD != 40 || fc.budgetUSD != 40 {
		t.Fatalf("budgetMonthUSD = %v, controller got %v, want 40 and 40", doc.Cost.BudgetMonthUSD, fc.budgetUSD)
	}
}

func TestBudgetRejections(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		ctlErr      error
		wantCode    string
		wantMessage string
		wantCalls   int
	}{
		{"not json", `{"monthlyUSD":"x"}`, nil, "invalid_json", "", 0},
		{"out of range", `{"monthlyUSD":1e400}`, nil, "invalid_json", "", 0},
		{"unknown field", `{"monthly":40}`, nil, "invalid_json", "", 0},
		{"zero", `{"monthlyUSD":0}`, controller.ErrBudgetInvalid, "budget_invalid", "monthlyUSD must be a positive number", 1},
		{"missing", `{}`, controller.ErrBudgetInvalid, "budget_invalid", "monthlyUSD must be a positive number", 1},
		{"above ceiling", `{"monthlyUSD":150}`, controller.ErrBudgetAboveCeiling, "budget_above_ceiling", "monthlyUSD must not exceed the configured ceiling of $100.00", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeController()
			fc.budgetErr = tc.ctlErr
			fc.budgetSnap = controller.Snapshot{BudgetCeilingUSD: 100}
			rec := doReq(newTestServer(t, fc), "POST", "/api/v1/gpu/budget", tc.body)
			if rec.Code != 400 {
				t.Fatalf("code = %d, want 400", rec.Code)
			}
			code, message := errorOf(t, rec)
			if code != tc.wantCode || message != tc.wantMessage {
				t.Fatalf("error = %q / %q, want %q / %q", code, message, tc.wantCode, tc.wantMessage)
			}
			if fc.budgetCalls != tc.wantCalls {
				t.Fatalf("controller calls = %d, want %d", fc.budgetCalls, tc.wantCalls)
			}
		})
	}
}

func TestBudgetRejectsGet(t *testing.T) {
	rec := doReq(newTestServer(t, newFakeController()), "GET", "/api/v1/gpu/budget", "")
	if rec.Code != 405 {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}

func TestBudgetChangeIsLogged(t *testing.T) {
	fc := newFakeController()
	fc.statusSnap = controller.Snapshot{BudgetMonthUSD: 30}
	fc.budgetSnap = controller.Snapshot{BudgetMonthUSD: 40}
	s := newTestServer(t, fc)
	var buf bytes.Buffer
	s.d.Log = slog.New(slog.NewTextHandler(&buf, nil))

	keyID := "mgmt-key-1"
	req := httptest.NewRequest("POST", "/api/v1/gpu/budget", strings.NewReader(`{"monthlyUSD":40}`))
	req = req.WithContext(withKeyIDSink(context.Background(), &keyID))
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	line := buf.String()
	for _, want := range []string{"mgmt: budget changed", "key_id=mgmt-key-1", "oldUSD=30", "newUSD=40"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log %q does not contain %q", line, want)
		}
	}
}
