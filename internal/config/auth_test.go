// internal/config/auth_test.go
package config

import "testing"

func TestLoadAuthDefaults(t *testing.T) {
	a, err := LoadAuth()
	if err != nil {
		t.Fatalf("LoadAuth: %v", err)
	}
	if a.ClientKeysFile == "" || a.ManagementTokensFile == "" || a.WorkerSeedFile == "" {
		t.Fatalf("LoadAuth: got empty default path(s): %+v", a)
	}
}
