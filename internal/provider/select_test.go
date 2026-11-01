// internal/provider/select_test.go
package provider

import "testing"

func TestSelectTable(t *testing.T) { // AC1
	offers := []Offer{
		{ID: 1, MachineID: 10, Hourly: 810000},     // above cap, excluded
		{ID: 2, MachineID: 11, Hourly: 800000},     // at cap, included
		{ID: 3, MachineID: 12, Hourly: 600000},     // cheapest tier, lower id
		{ID: 7, MachineID: 13, Hourly: 600000},     // cheapest tier, tied with id 3
		{ID: 4, MachineID: 99, Hourly: 550000},     // denylisted, excluded (would otherwise be cheapest)
		{ID: 5, MachineID: 14, Hourly: 700000},
		{ID: 6, MachineID: 15, Hourly: 650000},
		{ID: 8, MachineID: 16, Hourly: 750000},
		{ID: 9, MachineID: 17, Hourly: 780000},
		{ID: 10, MachineID: 18, Hourly: 620000},
	}
	f := OfferFilter{MaxHourly: 800000, ExcludeMachineIDs: []int64{99}}
	got := Select(offers, f)
	for _, o := range got {
		if o.Hourly > 800000 {
			t.Fatalf("offer %d above cap: %d", o.ID, o.Hourly)
		}
		if o.MachineID == 99 {
			t.Fatalf("denylisted machine present: %d", o.ID)
		}
	}
	if got[0].ID != 3 {
		t.Fatalf("first offer id = %d, want 3 (lower id at tied price)", got[0].ID)
	}
	found800 := false
	for i, o := range got {
		if o.Hourly == 800000 {
			found800 = true
		}
		if i > 0 && got[i-1].Hourly > o.Hourly {
			t.Fatalf("prices not non-decreasing at index %d", i)
		}
	}
	if !found800 {
		t.Fatal("$0.80 offer missing, should be included (at cap, not above it)")
	}
}
