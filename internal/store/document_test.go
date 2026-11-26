package store

import (
	"bytes"
	"encoding/json"
	"strings"
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

func TestDocumentOldJSONHasNoOriginOrGeo(t *testing.T) {
	var d Document
	if err := json.Unmarshal([]byte(`{"schemaVersion":2,"installationId":"iid","instance":{"offer":{"machineId":1,"gpu":"L40S"}}}`), &d); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if d.StartOrigin != "" || d.Instance.Offer.Geo != "" {
		t.Fatalf("StartOrigin %q Geo %q, want both empty", d.StartOrigin, d.Instance.Offer.Geo)
	}
	d.StartOrigin, d.Instance.Offer.Geo = "demand", "Sweden"
	b, _ := json.Marshal(d)
	if !bytes.Contains(b, []byte(`"startOrigin":"demand"`)) || !bytes.Contains(b, []byte(`"geo":"Sweden"`)) {
		t.Fatalf("marshal lost fields: %s", b)
	}
}

func TestDocumentWithoutRentals(t *testing.T) {
	d, err := Decode([]byte(`{"schemaVersion":2,"installationId":"4f2c1a9b0e77","desired":"STOPPED"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if d.Rentals != nil {
		t.Fatalf("Rentals = %v, want nil", d.Rentals)
	}
	b, err := Encode(d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.Contains(string(b), `"rentals"`) {
		t.Fatalf("encoded document has a rentals key: %s", b)
	}
}
