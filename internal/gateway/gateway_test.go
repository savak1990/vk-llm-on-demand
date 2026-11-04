package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
)

const (
	testClientKeysFile = "testdata/client-keys"
	testMgmtTokensFile = "testdata/management-tokens"
	testWorkerSeedFile = "testdata/worker-seed"
)

func testDeps(t *testing.T) Deps {
	t.Helper()
	keys, err := auth.Load(testClientKeysFile, testMgmtTokensFile, clock.Real{}, slog.Default())
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	seed, err := LoadWorkerSeed(testWorkerSeedFile, clock.Real{})
	if err != nil {
		t.Fatalf("loadWorkerSeed: %v", err)
	}
	return Deps{
		Controller: &fakeController{},
		Keys:       keys,
		Seed:       seed,
		Clock:      clock.Real{},
		Config: config.Gateway{
			ModelAlias:         "qwen3.6-35b-a3b",
			MaxBodyBytes:       1 << 20,
			MaxTokensCap:       8192,
			RateLimitPerKeyRPM: 60,
		},
		InstallationID: "test-install",
		Log:            slog.New(slog.DiscardHandler),
	}
}

func TestNewRequiresModelAlias(t *testing.T) {
	d := testDeps(t)
	d.Config.ModelAlias = ""
	if _, err := New(d); err == nil {
		t.Fatal("New: want error for empty ModelAlias")
	}
}

func TestNewRequiresController(t *testing.T) {
	d := testDeps(t)
	d.Controller = nil
	if _, err := New(d); err == nil {
		t.Fatal("New: want error for nil Controller")
	}
}

func TestNewRequiresKeys(t *testing.T) {
	d := testDeps(t)
	d.Keys = nil
	if _, err := New(d); err == nil {
		t.Fatal("New: want error for nil Keys")
	}
}

func TestNewRequiresSeed(t *testing.T) {
	d := testDeps(t)
	d.Seed = nil
	if _, err := New(d); err == nil {
		t.Fatal("New: want error for nil Seed")
	}
}

func TestUnknownPathUnder1GetsJSON404(t *testing.T) {
	g := newTestGateway(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/nope", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q does not parse as errorBody: %v", rec.Body.String(), err)
	}
	if body.Error.Type != "invalid_request_error" || body.Error.Code != "not_found" {
		t.Fatalf("body = %+v, want type=invalid_request_error code=not_found", body.Error)
	}
}

func TestWrongMethodOnKnownPathGetsJSON405(t *testing.T) {
	g := newTestGateway(t)
	req := httptest.NewRequest(http.MethodDelete, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Allow"); got != "GET" {
		t.Fatalf("Allow = %q, want %q", got, "GET")
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q does not parse as errorBody: %v", rec.Body.String(), err)
	}
	if body.Error.Type != "invalid_request_error" || body.Error.Code != "method_not_allowed" {
		t.Fatalf("body = %+v, want type=invalid_request_error code=method_not_allowed", body.Error)
	}
}

func TestRecoverReturns500(t *testing.T) {
	g, err := New(testDeps(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	g.mux.HandleFunc("GET /v1/test-panic", func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/test-panic", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
