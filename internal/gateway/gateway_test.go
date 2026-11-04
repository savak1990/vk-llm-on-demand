package gateway

import (
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
