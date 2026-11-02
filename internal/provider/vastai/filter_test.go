package vastai

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

func defaultFilter() provider.OfferFilter {
	return provider.OfferFilter{
		MinCPURAMGB: 32,
		MinCUDA:     12.8,
		MaxHourly:   800000, // $0.80
	}
}

func TestFilterBody(t *testing.T) { // AC2
	got, err := json.MarshalIndent(buildFilterBody(defaultFilter()), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/filter_default.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("filter body mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}
