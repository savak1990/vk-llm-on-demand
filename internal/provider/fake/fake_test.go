package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

func TestFakeScript(t *testing.T) {
	f := New(clock.Real{})
	f.SetOffers(provider.Offer{ID: 1, MachineID: 1, GPUName: "L40S", Hourly: 500000})
	f.SetStatusSequence(1, provider.StatusRunning)
	f.FailNext("get", 1, errors.New("fake error"))

	ctx := context.Background()

	// SearchOffers
	offers, err := f.SearchOffers(ctx, provider.OfferFilter{MaxHourly: 1000000})
	if err != nil {
		t.Fatalf("SearchOffers failed: %v", err)
	}
	if len(offers) != 1 || offers[0].ID != 1 {
		t.Fatalf("SearchOffers returned %v, want one offer with ID 1", offers)
	}

	// CreateInstance
	id, err := f.CreateInstance(ctx, provider.CreateRequest{Label: "test"})
	if err != nil {
		t.Fatalf("CreateInstance failed: %v", err)
	}
	if id != 1 {
		t.Fatalf("CreateInstance returned %d, want 1", id)
	}

	// GetInstance with failure
	_, err = f.GetInstance(ctx, id)
	if err == nil {
		t.Fatalf("GetInstance should have failed on first call")
	}

	// GetInstance succeeds, uses SetStatusSequence
	inst, err := f.GetInstance(ctx, id)
	if err != nil {
		t.Fatalf("GetInstance failed: %v", err)
	}
	if inst.Status != provider.StatusRunning {
		t.Fatalf("GetInstance returned status %d, want %d", inst.Status, provider.StatusRunning)
	}

	// DestroyInstance
	err = f.DestroyInstance(ctx, id)
	if err != nil {
		t.Fatalf("DestroyInstance failed: %v", err)
	}

	// Verify call counts
	if f.Calls("search") != 1 {
		t.Errorf("Calls(search) = %d, want 1", f.Calls("search"))
	}
	if f.Calls("create") != 1 {
		t.Errorf("Calls(create) = %d, want 1", f.Calls("create"))
	}
	if f.Calls("get") != 2 {
		t.Errorf("Calls(get) = %d, want 2", f.Calls("get"))
	}
	if f.Calls("destroy") != 1 {
		t.Errorf("Calls(destroy) = %d, want 1", f.Calls("destroy"))
	}
}

func TestFailNextCreate(t *testing.T) {
	f := New(clock.Real{})
	wantErr := errors.New("fake create error")
	f.FailNext("create", 1, wantErr)

	ctx := context.Background()

	if _, err := f.CreateInstance(ctx, provider.CreateRequest{Label: "a"}); !errors.Is(err, wantErr) {
		t.Fatalf("CreateInstance (1st) error = %v, want %v", err, wantErr)
	}
	if _, err := f.CreateInstance(ctx, provider.CreateRequest{Label: "a"}); err != nil {
		t.Fatalf("CreateInstance (2nd) failed: %v", err)
	}
}

func TestFakeSharesSelect(t *testing.T) {
	offers := []provider.Offer{
		{ID: 1, MachineID: 1, GPUName: "L40S", Hourly: 500000},
		{ID: 2, MachineID: 2, GPUName: "A100", Hourly: 1000000},
		{ID: 3, MachineID: 3, GPUName: "H100", Hourly: 2000000},
	}

	f := New(clock.Real{})
	f.SetOffers(offers...)

	filter := provider.OfferFilter{
		MaxHourly:         1500000,
		ExcludeMachineIDs: []int64{2},
	}

	ctx := context.Background()

	// SearchOffers from fake
	fakeOffers, err := f.SearchOffers(ctx, filter)
	if err != nil {
		t.Fatalf("SearchOffers failed: %v", err)
	}

	// Select from provider directly
	selectedOffers := provider.Select(offers, filter)

	// Compare
	if len(fakeOffers) != len(selectedOffers) {
		t.Errorf("SearchOffers returned %d offers, Select returned %d", len(fakeOffers), len(selectedOffers))
	}
	for i, fo := range fakeOffers {
		if i >= len(selectedOffers) {
			break
		}
		so := selectedOffers[i]
		if fo.ID != so.ID {
			t.Errorf("offer %d: ID %d != %d", i, fo.ID, so.ID)
		}
	}
}

func TestFakeSeeded(t *testing.T) {
	f := NewSeeded(clock.Real{})
	ctx := context.Background()

	// Check offers
	offers, err := f.SearchOffers(ctx, provider.OfferFilter{MaxHourly: 1000000})
	if err != nil {
		t.Fatalf("SearchOffers failed: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("Expected 1 offer, got %d", len(offers))
	}
	if offers[0].GPUName != "L40S" {
		t.Errorf("Expected L40S, got %s", offers[0].GPUName)
	}
	if offers[0].Hourly != 500000 {
		t.Errorf("Expected hourly 500000, got %d", offers[0].Hourly)
	}

	// Create instance
	id, err := f.CreateInstance(ctx, provider.CreateRequest{Label: "test"})
	if err != nil {
		t.Fatalf("CreateInstance failed: %v", err)
	}

	// Get instance - should reach StatusRunning
	inst, err := f.GetInstance(ctx, id)
	if err != nil {
		t.Fatalf("GetInstance failed: %v", err)
	}
	if inst.Status != provider.StatusRunning {
		t.Errorf("Expected StatusRunning, got %d", inst.Status)
	}
	if inst.SSHHost != "fake" {
		t.Errorf("Expected SSHHost 'fake', got %q", inst.SSHHost)
	}
	if inst.SSHPort != 22 {
		t.Errorf("Expected SSHPort 22, got %d", inst.SSHPort)
	}

	// Check balance
	balance, err := f.Balance(ctx)
	if err != nil {
		t.Fatalf("Balance failed: %v", err)
	}
	if balance != 100_000_000 {
		t.Errorf("Expected balance 100_000_000, got %d", balance)
	}
}

func TestFailDestroy(t *testing.T) {
	f := New(clock.Real{})
	f.FailDestroy(3)

	ctx := context.Background()
	id := provider.InstanceID(1)
	f.AddInstance(provider.Instance{ID: id, Label: "test", Status: provider.StatusRunning})

	// First 3 calls should fail with Retryable
	for i := 1; i <= 3; i++ {
		err := f.DestroyInstance(ctx, id)
		if err == nil {
			t.Errorf("Call %d: expected error, got nil", i)
		}
		var perr *provider.Error
		if !errors.As(err, &perr) || perr.Class != provider.Retryable {
			t.Errorf("Call %d: expected Retryable error, got %v", i, err)
		}
	}

	// Fourth call should succeed
	err := f.DestroyInstance(ctx, id)
	if err != nil {
		t.Errorf("Call 4: expected nil, got %v", err)
	}

	// Verify call count
	if f.Calls("destroy") != 4 {
		t.Errorf("Calls(destroy) = %d, want 4", f.Calls("destroy"))
	}
}

func TestDestroyMissingIDReturnsNotFound(t *testing.T) {
	f := New(clock.Real{})
	ctx := context.Background()

	err := f.DestroyInstance(ctx, provider.InstanceID(999))
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("DestroyInstance on a missing id = %v, want ErrNotFound", err)
	}
}
