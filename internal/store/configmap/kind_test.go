//go:build integration

package configmap

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func kindConfig(t *testing.T) Config {
	apiServer := os.Getenv("KIND_API_SERVER")
	caPath := os.Getenv("KIND_CA_PATH")
	tokenPath := os.Getenv("KIND_TOKEN_PATH")
	if apiServer == "" || caPath == "" || tokenPath == "" {
		t.Skip("KIND_API_SERVER/KIND_CA_PATH/KIND_TOKEN_PATH not set; run under the store-kind CI job")
	}
	return Config{
		APIServer: apiServer, Namespace: "default", Name: "vkllm-state",
		TokenPath: tokenPath, CAPath: caPath, InstallationID: "4f2c1a9b0e77",
	}
}

// TestKindRace covers AC12: two writers each run 1000 Transitions, final
// generation 2000, real conflicts observed. ErrRetriesExhausted retries
// the same attempt, so AC12 counts successful transitions, not tries.
func TestKindRace(t *testing.T) {
	cfg := kindConfig(t)
	c1, err := New(cfg)
	if err != nil {
		t.Fatalf("New (writer 1): %v", err)
	}
	c2, err := New(cfg)
	if err != nil {
		t.Fatalf("New (writer 2): %v", err)
	}

	run := func(c *Client) []int64 {
		seen := make([]int64, 0, 1000)
		for i := 0; i < 1000; i++ {
			for {
				doc, err := store.Transition(context.Background(), c, func(d *store.Document) error {
					d.Generation++
					return nil
				})
				if err == nil {
					seen = append(seen, doc.Generation)
					break
				}
				if errors.Is(err, store.ErrRetriesExhausted) {
					continue
				}
				t.Errorf("Transition: %v", err)
				return seen
			}
		}
		return seen
	}

	var wg sync.WaitGroup
	results := make([][]int64, 2)
	wg.Add(2)
	go func() { defer wg.Done(); results[0] = run(c1) }()
	go func() { defer wg.Done(); results[1] = run(c2) }()
	wg.Wait()

	for w, seen := range results {
		for i := 1; i < len(seen); i++ {
			if seen[i] <= seen[i-1] {
				t.Fatalf("writer %d: generation not strictly increasing: %v", w, seen[i-1:i+1])
			}
		}
	}
	final, _, err := c1.Load(context.Background())
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	if final.Generation != 2000 {
		t.Fatalf("final generation = %d, want 2000", final.Generation)
	}
	if store.ConflictsTotalValue() <= 0 {
		t.Fatal("want vkllm_store_conflicts_total > 0 after a real race")
	}
}

// TestKindRBAC covers R18: the Role's own ServiceAccount token, not an
// admin impersonation, must be rejected on every verb but get/update.
func TestKindRBAC(t *testing.T) {
	cfg := kindConfig(t)
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	doc, v, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load (get must be allowed): %v", err)
	}
	if _, err := c.Save(context.Background(), doc, v); err != nil {
		t.Fatalf("Save (update must be allowed): %v", err)
	}

	base := c.url()
	collection := cfg.APIServer + "/api/v1/namespaces/" + cfg.Namespace + "/configmaps"
	forbidden := []struct{ method, url string }{
		{http.MethodPost, collection},
		{http.MethodPatch, base},
		{http.MethodDelete, base},
		{http.MethodGet, collection},
	}
	for _, f := range forbidden {
		status, _, err := c.doOnce(context.Background(), f.method, f.url, []byte("{}"))
		if err != nil {
			t.Fatalf("%s %s: %v", f.method, f.url, err)
		}
		if status != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want 403", f.method, f.url, status)
		}
	}
}

// TestKindMissingAndRecreate covers AC13: a deleted ConfigMap fails Load
// and Ready with ErrStoreMissing; recreating the empty shell restores the
// fresh-document path.
func TestKindMissingAndRecreate(t *testing.T) {
	cfg := kindConfig(t)
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	doc, v, err := c.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	doc.Generation = 3
	if _, err := c.Save(context.Background(), doc, v); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := exec.Command("kubectl", "delete", "configmap", cfg.Name).Run(); err != nil {
		t.Fatalf("kubectl delete configmap: %v", err)
	}

	if _, err := store.Transition(context.Background(), c, func(*store.Document) error { return nil }); !errors.Is(err, store.ErrStoreMissing) {
		t.Fatalf("Transition after delete: want ErrStoreMissing, got %v", err)
	}
	if err := store.Ready(c)(); !errors.Is(err, store.ErrStoreMissing) {
		t.Fatalf("Ready after delete: want ErrStoreMissing, got %v", err)
	}

	if err := exec.Command("kubectl", "create", "configmap", cfg.Name).Run(); err != nil {
		t.Fatalf("kubectl create configmap: %v", err)
	}

	final, err := store.Transition(context.Background(), c, func(*store.Document) error { return nil })
	if err != nil {
		t.Fatalf("Transition after recreate: %v", err)
	}
	if !final.IsFresh() || final.InstallationID != cfg.InstallationID {
		t.Fatalf("got %+v, want a fresh document for %s", final, cfg.InstallationID)
	}
}
