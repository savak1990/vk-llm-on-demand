package mgmt

import (
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

func TestBuildStatusDocumentReadySnapshot(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	readyAt := now.Add(-30 * time.Minute)
	deadline := now.Add(24 * time.Minute)
	snap := controller.Snapshot{
		Provider: "vastai", Public: "READY", Desired: "RUNNING", Generation: 7,
		GPUName: "L40S", MachineID: 12345, Hourly: provider.Money(710000),
		CreatedAt: readyAt, ReadyAt: &readyAt, IdleDeadline: &deadline,
		InstallationID: "4f2c1a9b0e77", LabelPrefix: "vkllm:4f2c1a9b0e77:",
	}
	cfg := config.Mgmt{ProviderName: "vastai", ModelAlias: "qwen3.6-35b-a3b", ModelContextSize: 32768}
	doc := buildStatusDocument(snap, cfg, now)

	if doc.State != "READY" || doc.Desired != "RUNNING" || !doc.Ready {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.SecondsUntilIdleShutdown == nil || *doc.SecondsUntilIdleShutdown != 1440 {
		t.Fatalf("SecondsUntilIdleShutdown = %v, want 1440", doc.SecondsUntilIdleShutdown)
	}
	if doc.Instance == nil || doc.Instance.GPU != "L40S" || doc.Instance.MachineID != 12345 {
		t.Fatalf("Instance = %+v", doc.Instance)
	}
	if doc.Model.Alias != "qwen3.6-35b-a3b" || doc.Model.ContextSize != 32768 {
		t.Fatalf("Model = %+v", doc.Model)
	}
}

func TestBuildStatusDocumentStoppedSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	snap := controller.Snapshot{Provider: "vastai", Public: "STOPPED", Desired: "STOPPED"}
	doc := buildStatusDocument(snap, config.Mgmt{}, now)
	if doc.Instance != nil || doc.IdleDeadline != nil || doc.SecondsUntilIdleShutdown != nil {
		t.Fatalf("stopped doc = %+v, want instance/idleDeadline/secondsUntilIdleShutdown all nil", doc)
	}
	if doc.UptimeSeconds != 0 || doc.Ready {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestBuildStatusDocumentNextCreateAtOnlyWhenFuture(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Minute)

	doc := buildStatusDocument(controller.Snapshot{NextCreateAt: past}, config.Mgmt{}, now)
	if doc.NextCreateAt != nil {
		t.Fatalf("NextCreateAt = %v, want nil for a past timestamp", doc.NextCreateAt)
	}
	doc = buildStatusDocument(controller.Snapshot{NextCreateAt: future}, config.Mgmt{}, now)
	if doc.NextCreateAt == nil || !doc.NextCreateAt.Equal(future) {
		t.Fatalf("NextCreateAt = %v, want %v", doc.NextCreateAt, future)
	}
	doc = buildStatusDocument(controller.Snapshot{}, config.Mgmt{}, now) // zero value
	if doc.NextCreateAt != nil {
		t.Fatalf("NextCreateAt = %v, want nil for the zero time", doc.NextCreateAt)
	}
}

func TestBuildCostDocument(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	createdAt := now.Add(-30 * time.Minute)
	balance := 12.5
	snap := controller.Snapshot{
		Hourly: provider.Money(710000), CreatedAt: createdAt,
		SpendInstanceUSD: 0.36, SpendMTDUSD: 3.42, ProviderBalanceUSD: &balance,
	}
	doc := buildCostDocument(snap, config.Mgmt{BudgetMonthUSD: 30}, now)
	if !doc.Estimates || doc.PriceUSDPerHour != 0.71 || doc.UptimeSeconds != 1800 {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.EstimatedInstanceUSD != 0.36 || doc.EstimatedMonthUSD != 3.42 || doc.BudgetMonthUSD != 30 {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.ProviderBalanceUSD == nil || *doc.ProviderBalanceUSD != 12.5 {
		t.Fatalf("ProviderBalanceUSD = %v", doc.ProviderBalanceUSD)
	}
	if doc.Limitations == "" {
		t.Fatal("Limitations must be non-empty")
	}
}

func TestBuildCostDocumentNoInstance(t *testing.T) {
	doc := buildCostDocument(controller.Snapshot{}, config.Mgmt{BudgetMonthUSD: 30}, time.Now())
	if doc.PriceUSDPerHour != 0 || doc.EstimatedInstanceUSD != 0 || doc.ProviderBalanceUSD != nil {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestResultFor(t *testing.T) {
	cases := []struct {
		op, public, want string
	}{
		{"start", "READY", "already_running"},
		{"start", "ALLOCATING", "accepted"},
		{"start", "STOPPED", "accepted"}, // e.g. desired=RUNNING during a create backoff
		{"stop", "STOPPED", "already_stopped"},
		{"stop", "DRAINING", "accepted"},
	}
	for _, c := range cases {
		if got := resultFor(c.op, controller.Snapshot{Public: c.public}); got != c.want {
			t.Errorf("resultFor(%q, %q) = %q, want %q", c.op, c.public, got, c.want)
		}
	}
}
