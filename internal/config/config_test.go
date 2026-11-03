package config

import "testing"

func TestLoadReadsInstallationIDAndConfigMapName(t *testing.T) {
	t.Setenv("VKLLM_INSTALLATION_ID", "4f2c1a9b0e77")
	t.Setenv("VKLLM_STATE_CONFIGMAP", "vkllm-state")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.InstallationID != "4f2c1a9b0e77" {
		t.Errorf("InstallationID = %q, want 4f2c1a9b0e77", cfg.InstallationID)
	}
	if cfg.StateConfigMapName != "vkllm-state" {
		t.Errorf("StateConfigMapName = %q, want vkllm-state", cfg.StateConfigMapName)
	}
}

func TestLoadMissingInstallationID(t *testing.T) {
	t.Setenv("VKLLM_INSTALLATION_ID", "")
	t.Setenv("VKLLM_STATE_CONFIGMAP", "vkllm-state")

	if _, err := Load(); err == nil {
		t.Fatal("Load: want error for missing VKLLM_INSTALLATION_ID, got nil")
	}
}

func TestLoadMissingStateConfigMap(t *testing.T) {
	t.Setenv("VKLLM_INSTALLATION_ID", "4f2c1a9b0e77")
	t.Setenv("VKLLM_STATE_CONFIGMAP", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load: want error for missing VKLLM_STATE_CONFIGMAP, got nil")
	}
}
