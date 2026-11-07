// internal/obs/catalog_test.go
package obs

import "testing"

func TestCatalogHas43SortedUniqueNames(t *testing.T) {
	c := Catalog()
	if len(c) != 43 {
		t.Fatalf("len(Catalog()) = %d, want 43", len(c))
	}
	seen := map[string]bool{}
	for i, s := range c {
		if seen[s.Name] {
			t.Fatalf("duplicate name %s", s.Name)
		}
		seen[s.Name] = true
		if i > 0 && c[i-1].Name >= s.Name {
			t.Fatalf("Catalog() not sorted: %s before %s", c[i-1].Name, s.Name)
		}
	}
}

func TestVastaiOnlyMetricsAreInCatalog(t *testing.T) {
	names := map[string]bool{}
	for _, s := range Catalog() {
		names[s.Name] = true
	}
	for _, n := range VastaiOnlyMetrics() {
		if !names[n] {
			t.Fatalf("%s from VastaiOnlyMetrics is not in Catalog()", n)
		}
	}
	if len(VastaiOnlyMetrics()) != 3 {
		t.Fatalf("len(VastaiOnlyMetrics()) = %d, want 3", len(VastaiOnlyMetrics()))
	}
}
