// internal/provider/filter_test.go
package provider

import "testing"

func TestAdmitTable(t *testing.T) {
	f := OfferFilter{MaxHourly: 800000, MaxDownloadPerGB: 100000, MaxStoragePerGBMonth: 1000000, MinDiskGB: 60, ExcludeMachineIDs: []int64{99}}
	atCaps := Offer{ID: 1, MachineID: 10, Hourly: 800000, DownloadPerGB: 100000, StoragePerGBMonth: 1000000, DiskSpaceGB: 60}
	cases := []struct {
		name string
		mut  func(*Offer)
		want bool
	}{
		{"at every cap", func(*Offer) {}, true},
		{"hourly above cap", func(o *Offer) { o.Hourly++ }, false},
		{"download above cap", func(o *Offer) { o.DownloadPerGB++ }, false},
		{"storage above cap", func(o *Offer) { o.StoragePerGBMonth++ }, false},
		{"disk below minimum", func(o *Offer) { o.DiskSpaceGB = 59.9 }, false},
		{"denylisted machine", func(o *Offer) { o.MachineID = 99 }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := atCaps
			c.mut(&o)
			if got := Admit(o, f); got != c.want {
				t.Fatalf("Admit = %v, want %v for %+v", got, c.want, o)
			}
		})
	}
}

func TestFilterKeepsInputOrder(t *testing.T) {
	f := OfferFilter{MaxHourly: 800000}
	offers := []Offer{{ID: 3, Hourly: 700000}, {ID: 1, Hourly: 900000}, {ID: 2, Hourly: 500000}}
	got := Filter(offers, f)
	if len(got) != 2 || got[0].ID != 3 || got[1].ID != 2 {
		t.Fatalf("Filter = %+v, want ids [3 2] in input order", got)
	}
}
