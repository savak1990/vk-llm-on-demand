package controller

import (
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/modelsize"
)

func TestNewRejectsNonExplicitMode(t *testing.T) {
	_, err := New(Deps{Config: config.Controller{ProvisioningMode: "on-demand"}})
	if err == nil {
		t.Fatal("New: want error for non-explicit provisioningMode, got nil")
	}
}

func TestNewAcceptsExplicitMode(t *testing.T) {
	c, err := New(Deps{
		Config:    config.Controller{ProvisioningMode: "explicit"},
		ModelSize: modelsize.Fixed(1_000_000_000), ModelURL: "https://example.invalid/model.gguf",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.state != Idle {
		t.Errorf("initial state = %v, want Idle", c.state)
	}
}

func TestNewRequiresModelSizeAndURL(t *testing.T) {
	base := config.Controller{ProvisioningMode: "explicit"}
	if _, err := New(Deps{Config: base, ModelURL: "https://example.invalid/model.gguf"}); err == nil {
		t.Fatal("New: want error when ModelSize is nil")
	}
	if _, err := New(Deps{Config: base, ModelSize: modelsize.Fixed(1_000_000_000)}); err == nil {
		t.Fatal("New: want error when ModelURL is empty")
	}
}
