// internal/config/auth_test.go
package config

import "testing"

func clearAuthEnv(t *testing.T) {
	for _, k := range []string{
		"VKLLM_AUTH_CLIENT_KEYS_FILE",
		"VKLLM_AUTH_MANAGEMENT_TOKENS_FILE",
		"VKLLM_AUTH_WORKER_SEED_FILE",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadAuthDefaults(t *testing.T) {
	clearAuthEnv(t)
	a, err := LoadAuth()
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if a.ClientKeysFile == "" || a.ManagementTokensFile == "" || a.WorkerSeedFile == "" {
		t.Fatalf("LoadAuth: got empty default path(s): %+v", a)
	}
}
