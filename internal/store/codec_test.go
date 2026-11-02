package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	d, _ := NewDocument("4f2c1a9b0e77")
	d.Generation = 7
	d.Desired = DesiredRunning
	readyAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	d.Instance = &Instance{ProviderID: "12345678", Label: "vkllm:4f2c1a9b0e77:7", ReadyAt: &readyAt}

	b, err := Encode(d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Generation != 7 || got.Desired != DesiredRunning || got.Instance.ProviderID != "12345678" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if !got.Instance.ReadyAt.Equal(readyAt) {
		t.Fatalf("ReadyAt = %v, want %v", got.Instance.ReadyAt, readyAt)
	}
}

func TestIdleTimestampsRoundTrip(t *testing.T) {
	d, _ := NewDocument("4f2c1a9b0e77")
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Minute)
	idle := t1.Add(30 * time.Minute)
	d.Instance = &Instance{ReadyAt: &t0, LastInferenceFinishedAt: &t1, IdleDeadline: &idle}

	b, err := Encode(d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !got.Instance.ReadyAt.Equal(t0) || !got.Instance.LastInferenceFinishedAt.Equal(t1) || !got.Instance.IdleDeadline.Equal(idle) {
		t.Fatalf("timestamps mismatch: %+v", got.Instance)
	}
}

func TestOpsCapped(t *testing.T) {
	d, _ := NewDocument("4f2c1a9b0e77")
	for i := 0; i < 25; i++ {
		d.Ops = append(d.Ops, Op{Key: "create-" + string(rune('a'+i))})
	}
	b, err := Encode(d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got.Ops) != 20 {
		t.Fatalf("len(Ops) = %d, want 20", len(got.Ops))
	}
	if got.Ops[19].Key != d.Ops[24].Key {
		t.Fatalf("Ops not trimmed to the newest 20: got last %q, want %q", got.Ops[19].Key, d.Ops[24].Key)
	}
}

func TestTooLarge(t *testing.T) {
	d, _ := NewDocument("4f2c1a9b0e77")
	huge := make([]DenyEntry, 0, 20000)
	for i := 0; i < 20000; i++ {
		huge = append(huge, DenyEntry{MachineID: int64(i)})
	}
	d.MachineDenylist = huge
	if _, err := Encode(d); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Encode: want ErrTooLarge, got %v", err)
	}
}

func TestCappedDocumentSize(t *testing.T) {
	d, _ := NewDocument("4f2c1a9b0e77")
	readyAt := time.Now().UTC()
	d.Instance = &Instance{ProviderID: "12345678", Label: "vkllm:4f2c1a9b0e77:7", ReadyAt: &readyAt}
	for i := 0; i < 20; i++ {
		d.Ops = append(d.Ops, Op{Key: "create-123456789"})
	}
	for i := 0; i < 100; i++ {
		d.Ledger.Entries = append(d.Ledger.Entries, LedgerEntry{Gen: int64(i), DPH: 0.71})
	}
	for i := 0; i < 10; i++ {
		d.MachineDenylist = append(d.MachineDenylist, DenyEntry{MachineID: int64(i)})
	}
	b, err := Encode(d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	t.Logf("capped document size: %d bytes", len(b))
	if len(b) >= 32768 {
		t.Fatalf("capped document size %d >= 32768", len(b))
	}
}

func TestSchemaTooNew(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"schemaVersion": 3})
	if _, err := Decode(b); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("Decode: want ErrSchemaTooNew, got %v", err)
	}
	b2, _ := json.Marshal(map[string]any{})
	if _, err := Decode(b2); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Decode(no schemaVersion): want ErrCorrupt, got %v", err)
	}
}

func TestUpgradeV1(t *testing.T) {
	raw := map[string]any{
		"schemaVersion": 1, "installationId": "4f2c1a9b0e77", "generation": 7, "desired": 1,
		"leases": []any{}, "activity": map[string]any{},
		"instance": map[string]any{"providerId": "12345678", "readyAt": "2026-11-01T12:00:00Z"},
	}
	b, _ := json.Marshal(raw)
	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.SchemaVersion != 2 || got.Desired != DesiredRunning {
		t.Fatalf("got schemaVersion=%d desired=%q", got.SchemaVersion, got.Desired)
	}
	if got.Instance.LastInferenceFinishedAt != nil || got.Instance.IdleDeadline != nil {
		t.Fatalf("idle timestamps must be null after upgrade, got %+v", got.Instance)
	}

	raw["desired"] = 0
	b2, _ := json.Marshal(raw)
	got2, err := Decode(b2)
	if err != nil || got2.Desired != DesiredStopped {
		t.Fatalf("desired=0: got %q, err %v", got2.Desired, err)
	}

	raw["desired"] = 5
	b3, _ := json.Marshal(raw)
	if _, err := Decode(b3); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("desired=5: want ErrCorrupt, got %v", err)
	}
}
