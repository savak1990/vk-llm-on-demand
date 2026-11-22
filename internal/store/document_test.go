package store

import (
	"encoding/json"
	"testing"
)

func TestIsFresh(t *testing.T) {
	d, err := NewDocument("4f2c1a9b0e77")
	if err != nil {
		t.Fatalf("NewDocument: %v", err)
	}
	if !d.IsFresh() {
		t.Error("new document should be fresh")
	}
	d.Generation = 3
	if d.IsFresh() {
		t.Error("document with generation 3 should not be fresh")
	}
}

func TestNewDocumentRejectsBadID(t *testing.T) {
	for _, id := range []string{"", "Has-Caps", "has:colon", "-leading-dash", "0123456789012345678901234567890123"} {
		if _, err := NewDocument(id); err == nil {
			t.Errorf("NewDocument(%q): want error, got nil", id)
		}
	}
	if _, err := NewDocument("4f2c1a9b0e77"); err != nil {
		t.Errorf("NewDocument(valid): %v", err)
	}
}

func TestBudgetMonthUSDRoundTrip(t *testing.T) {
	var legacy Document
	if err := json.Unmarshal([]byte(`{"schemaVersion":2,"installationId":"iid","desired":"STOPPED"}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if legacy.BudgetMonthUSD != nil {
		t.Fatalf("legacy BudgetMonthUSD = %v, want nil", *legacy.BudgetMonthUSD)
	}

	v := 40.0
	b, err := json.Marshal(Document{BudgetMonthUSD: &v})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Document
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.BudgetMonthUSD == nil || *back.BudgetMonthUSD != 40 {
		t.Fatalf("round trip = %v, want 40", back.BudgetMonthUSD)
	}
}
