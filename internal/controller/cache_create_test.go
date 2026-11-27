package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/offercost"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	fakestore "github.com/savak1990/vk-llm-on-demand/internal/store/fake"
)

func TestRefusedRequest(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"400 refusal", &provider.Error{Op: "create", Class: provider.Terminal, Status: 400}, true},
		{"404", &provider.Error{Op: "create", Class: provider.Terminal, Status: 404}, true},
		{"2xx without an id", &provider.Error{Op: "create", Class: provider.Terminal, Status: 200}, false},
		{"404 from the pre-create list", &provider.Error{Op: "list", Class: provider.Terminal, Status: 404}, false},
		{"no status", &provider.Error{Op: "create", Class: provider.Terminal}, false},
		{"429", &provider.Error{Op: "create", Class: provider.Retryable, Status: 429}, false},
		{"5xx", &provider.Error{Op: "create", Class: provider.Retryable, Status: 503}, false},
		{"plain error", errors.New("boom"), false},
	} {
		if got := refusedRequest(c.err); got != c.want {
			t.Fatalf("%s: refusedRequest = %v, want %v", c.name, got, c.want)
		}
	}
}

// An ambiguous 2xx may have created the instance with its volume, so the
// create must not run again without the volume.
func TestDispatchCreateNoRetryOnAmbiguousSuccess(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	doc, _ := store.NewDocument("iid")
	doc.Generation = 1
	doc.Cache = &store.CacheState{Volumes: []store.CacheVolume{{Label: "vkllm_iid_v1", VolumeOfferID: 900, MachineID: 42, SizeGB: 34, State: store.CacheSeeding, InUseGen: 1}}}
	c.deps.Store.(*fakestore.Fake).Put(doc)
	prov.FailNext("create", 1, &provider.Error{Op: "create", Class: provider.Terminal, Status: 200, Err: errors.New("create rejected")})

	dispatchCreate(context.Background(), c, effCreate{
		offer: provider.Offer{ID: 11, MachineID: 42, Hourly: 600000}, breakdown: offercost.Breakdown{DiskGB: 60},
		filter: provider.OfferFilter{MaxHourly: 800000}, gen: 1, label: "vkllm:iid:1",
	})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvCreateFailed || ev.VolumeDropped {
		t.Fatalf("event = %+v, ok=%v; want EvCreateFailed with the volume kept", ev, ok)
	}
	if got := prov.Calls("create"); got != 1 {
		t.Fatalf("Calls(create) = %d, want 1 (no retry without the volume)", got)
	}
}
