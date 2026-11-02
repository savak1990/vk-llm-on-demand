// internal/provider/select.go
package provider

import "sort"

// Select applies the price cap and denylist locally (the server filter is
// not the guarantee) and returns offers sorted by Hourly then ID ascending.
func Select(offers []Offer, f OfferFilter) []Offer {
	deny := make(map[int64]bool, len(f.ExcludeMachineIDs))
	for _, id := range f.ExcludeMachineIDs {
		deny[id] = true
	}
	out := make([]Offer, 0, len(offers))
	for _, o := range offers {
		if o.Hourly > f.MaxHourly || deny[o.MachineID] {
			continue
		}
		out = append(out, o)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hourly != out[j].Hourly {
			return out[i].Hourly < out[j].Hourly
		}
		return out[i].ID < out[j].ID
	})
	return out
}
