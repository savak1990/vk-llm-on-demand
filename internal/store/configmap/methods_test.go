package configmap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	// These tests exercise Load/Save, not New's own CA-file handling (that's
	// TestNewReadsTokenAndCA's job) — build the Client directly on the test
	// server's already-trusted http.Client instead of loading a CA file.
	c := &Client{
		cfg: Config{
			APIServer: srv.URL, Namespace: "default", Name: "vkllm-state",
			TokenPath: tokenPath, InstallationID: "4f2c1a9b0e77",
		},
		httpClient: srv.Client(),
	}
	if err := c.loadToken(); err != nil {
		t.Fatalf("loadToken: %v", err)
	}
	return c
}

func TestLoadKeylessShellIsFresh(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind": "ConfigMap", "metadata": map[string]any{"name": "vkllm-state", "resourceVersion": "7"},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	doc, v, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !doc.IsFresh() || doc.Desired != store.DesiredStopped || doc.InstallationID != "4f2c1a9b0e77" {
		t.Fatalf("got %+v", doc)
	}
	if v != "7" {
		t.Fatalf("Version = %q, want 7", v)
	}
}

func TestLoadMissing404(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, _, err := c.Load(context.Background()); !errors.Is(err, store.ErrStoreMissing) {
		t.Fatalf("Load: want ErrStoreMissing, got %v", err)
	}
}

func TestSaveSendsResourceVersion(t *testing.T) {
	var gotVersion string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotVersion = body["metadata"].(map[string]any)["resourceVersion"].(string)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"metadata": map[string]any{"name": "vkllm-state", "resourceVersion": "42"},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	d, _ := store.NewDocument("4f2c1a9b0e77")
	v, err := c.Save(context.Background(), d, "41")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if gotVersion != "41" {
		t.Fatalf("PUT resourceVersion = %q, want 41", gotVersion)
	}
	if v != "42" {
		t.Fatalf("returned Version = %q, want 42", v)
	}
}

func TestSave409Conflict(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	d, _ := store.NewDocument("4f2c1a9b0e77")
	if _, err := c.Save(context.Background(), d, "41"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Save: want ErrConflict, got %v", err)
	}
}

func TestSave404Missing(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	d, _ := store.NewDocument("4f2c1a9b0e77")
	if _, err := c.Save(context.Background(), d, "41"); !errors.Is(err, store.ErrStoreMissing) {
		t.Fatalf("Save: want ErrStoreMissing, got %v", err)
	}
}

func TestTokenRefreshOn401(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer rotated-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"resourceVersion": "1"}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	if err := os.WriteFile(c.cfg.TokenPath, []byte("rotated-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := c.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (first 401, retry succeeds)", calls)
	}
}

func TestForbiddenTerminal(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	_, _, err := c.Load(context.Background())
	if err == nil || errors.Is(err, store.ErrStoreMissing) || errors.Is(err, store.ErrConflict) {
		t.Fatalf("Load: want a terminal non-sentinel error, got %v", err)
	}
}

func TestCorruptData(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"metadata": map[string]any{"resourceVersion": "7"},
			"data":     map[string]string{"state.json": "not json"},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, _, err := c.Load(context.Background()); !errors.Is(err, store.ErrCorrupt) {
		t.Fatalf("Load: want ErrCorrupt, got %v", err)
	}
}

func TestUnauthorizedAfterRetryIsTerminal(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, _, err := c.Load(context.Background()); err == nil {
		t.Fatal("Load: want an error when every response is 401")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want exactly 2 (one retry, then stop)", calls)
	}
}

func TestSaveEchoesLoadedMetadata(t *testing.T) {
	var gotLabels map[string]any
	get := true
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if get {
			get = false
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]any{
					"resourceVersion": "7",
					"labels":          map[string]any{"app.kubernetes.io/managed-by": "Helm"},
				},
			})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotLabels, _ = body["metadata"].(map[string]any)["labels"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"resourceVersion": "8"}})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	doc, v, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := c.Save(context.Background(), doc, v); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if gotLabels["app.kubernetes.io/managed-by"] != "Helm" {
		t.Fatalf("PUT body labels = %v, want the labels Load saw", gotLabels)
	}
}

func TestInstallationMismatch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		d, _ := store.NewDocument("different-id")
		b, _ := store.Encode(d)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"metadata": map[string]any{"resourceVersion": "7"},
			"data":     map[string]string{"state.json": string(b)},
		})
	}))
	defer srv.Close()
	c := newTestClient(t, srv)

	if _, _, err := c.Load(context.Background()); !errors.Is(err, store.ErrInstallation) {
		t.Fatalf("Load: want ErrInstallation, got %v", err)
	}
}
