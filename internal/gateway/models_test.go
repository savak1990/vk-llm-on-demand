package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func TestModelsNoController(t *testing.T) {
	d := testDeps(t)
	fc := &fakeController{status: controller.Snapshot{Public: "STOPPED"}}
	fc.admitErr = controller.ErrBackendUnavailable
	d.Controller = fc

	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	g.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calls, _ := fc.snapshot(); len(calls) != 0 {
		t.Fatalf("Admit called %d times, want 0", len(calls))
	}
	want := `"id":"qwen3.6-35b-a3b"`
	if !contains(rec.Body.String(), want) {
		t.Fatalf("body %q does not contain %q", rec.Body.String(), want)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
