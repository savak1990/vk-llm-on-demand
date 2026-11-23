package provider

import "slices"

// Admit applies the price caps, the disk minimum and the denylist locally;
// the server filter is not the guarantee.
func Admit(o Offer, f OfferFilter) bool {
	if o.Hourly > f.MaxHourly || o.DownloadPerGB > f.MaxDownloadPerGB || o.StoragePerGBMonth > f.MaxStoragePerGBMonth {
		return false
	}
	if o.DiskSpaceGB < float64(f.MinDiskGB) {
		return false
	}
	return !slices.Contains(f.ExcludeMachineIDs, o.MachineID)
}

func Filter(offers []Offer, f OfferFilter) []Offer {
	return slices.DeleteFunc(offers, func(o Offer) bool { return !Admit(o, f) })
}
