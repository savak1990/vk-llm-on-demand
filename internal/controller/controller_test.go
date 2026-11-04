package controller

import (
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
)

func TestNewRejectsNonExplicitMode(t *testing.T) {
	_, err := New(Deps{Config: config.Controller{ProvisioningMode: "on-demand"}})
	if err == nil {
		t.Fatal("New: want error for non-explicit provisioningMode, got nil")
	}
}

func TestNewAcceptsExplicitMode(t *testing.T) {
	c, err := New(Deps{Config: config.Controller{ProvisioningMode: "explicit"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.state != Idle {
		t.Errorf("initial state = %v, want Idle", c.state)
	}
}
