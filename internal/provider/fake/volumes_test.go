package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

func TestFakeVolumeLifecycleMatchesSpike(t *testing.T) {
	ctx := context.Background()
	f := New(fakeclock.New(time.Unix(0, 0)))
	f.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	id, err := f.CreateInstance(ctx, provider.CreateRequest{OfferID: 11, Image: "img", Label: "vkllm:abc:1",
		Volume: &provider.VolumeAttach{CreateNew: true, ID: 900, SizeGB: 34, MountPath: "/models", Name: "vkllm_abc_v1"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	vols, _ := f.ListVolumes(ctx, "vkllm_abc_v")
	if len(vols) != 1 || vols[0].MachineID != 42 || len(vols[0].Instances) != 1 {
		t.Fatalf("after create: %+v", vols)
	}
	if err := f.DeleteVolume(ctx, vols[0].ID); !errors.Is(err, provider.ErrVolumeInUse) {
		t.Fatalf("delete while attached: %v, want ErrVolumeInUse", err)
	}
	if err := f.DestroyInstance(ctx, id); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	vols, _ = f.ListVolumes(ctx, "vkllm_abc_v")
	if len(vols) != 1 || len(vols[0].Instances) != 0 {
		t.Fatalf("after destroy the volume must stay, detached: %+v", vols)
	}
	id2, err := f.CreateInstance(ctx, provider.CreateRequest{OfferID: 11, Image: "img", Label: "vkllm:abc:2",
		Volume: &provider.VolumeAttach{ID: int64(vols[0].ID), MountPath: "/models"}})
	if err != nil {
		t.Fatalf("link on the same machine: %v", err)
	}
	// Detach first: otherwise the next create would fail on "already
	// in-use", masking whether the cross-machine check runs at all.
	if err := f.DestroyInstance(ctx, id2); err != nil {
		t.Fatalf("destroy id2: %v", err)
	}
	f.SetOffers(provider.Offer{ID: 12, MachineID: 43, Hourly: 600000})
	if _, err := f.CreateInstance(ctx, provider.CreateRequest{OfferID: 12, Image: "img", Label: "vkllm:abc:3",
		Volume: &provider.VolumeAttach{ID: int64(vols[0].ID), MountPath: "/models"}}); err == nil {
		t.Fatal("link on another machine must fail")
	}
}
