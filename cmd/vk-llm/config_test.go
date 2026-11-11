package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
api:
  endpoint: https://file.example.com
  tokenEnv: FILE_TOKEN_VAR
defaults:
  startupTimeout: 20m
  waitForReady: true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		args    []string
		env     map[string]string
		wantEnd string
		wantTO  time.Duration
	}{
		{
			name:    "flag beats everything",
			args:    []string{"--config", cfgPath, "--endpoint", "https://flag.example.com"},
			env:     map[string]string{"VK_LLM_API_ENDPOINT": "https://env.example.com", "FILE_TOKEN_VAR": "sk-vkllm-REPLACE_ME"},
			wantEnd: "https://flag.example.com",
		},
		{
			name:    "env beats file",
			args:    []string{"--config", cfgPath},
			env:     map[string]string{"VK_LLM_API_ENDPOINT": "https://env.example.com", "FILE_TOKEN_VAR": "sk-vkllm-REPLACE_ME"},
			wantEnd: "https://env.example.com",
		},
		{
			name:    "file beats default",
			args:    []string{"--config", cfgPath},
			env:     map[string]string{"FILE_TOKEN_VAR": "sk-vkllm-REPLACE_ME"},
			wantEnd: "https://file.example.com",
			wantTO:  20 * time.Minute,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			getenv := func(k string) string { return c.env[k] }
			cfg, code, _ := loadConfig(c.args, getenv)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if cfg.Endpoint != c.wantEnd {
				t.Errorf("Endpoint = %q, want %q", cfg.Endpoint, c.wantEnd)
			}
		})
	}
}

func TestTokenInFileRejected(t *testing.T) {
	for _, body := range []string{
		"api:\n  token: sk-vkllm-REPLACE_ME\n",
		"token: sk-vkllm-REPLACE_ME\n",
		"Token: sk-vkllm-REPLACE_ME\n",
	} {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, code, usage := loadConfig([]string{"--config", cfgPath, "--endpoint", "https://e.example.com"}, func(string) string { return "" })
		if code != 2 {
			t.Errorf("body %q: exit code = %d, want 2", body, code)
		}
		if usage == "" || !contains(usage, "must not contain a token") {
			t.Errorf("body %q: usage = %q, want it to mention the token-in-file error", body, usage)
		}
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	// yaml.v3 with KnownFields(true) on a Decoder catches this; confirm
	// loadConfig wires that in rather than silently ignoring typos.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("api:\n  endpoin: https://typo.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, code, _ := loadConfig([]string{"--config", cfgPath}, func(string) string { return "" })
	if code != 2 {
		t.Errorf("exit code = %d, want 2 for an unknown config key", code)
	}
}

func TestPlainHTTPRejected(t *testing.T) {
	cases := []struct {
		endpoint string
		wantCode int
	}{
		{"http://llm.example.com", 2},
		{"http://127.0.0.1:8080", 0},
		{"http://localhost:8080", 0},
		{"https://llm.example.com", 0},
	}
	for _, c := range cases {
		_, code, _ := loadConfig([]string{"--endpoint", c.endpoint}, func(k string) string {
			if k == "VK_LLM_TOKEN" {
				return "sk-vkllm-REPLACE_ME"
			}
			return ""
		})
		if code != c.wantCode {
			t.Errorf("endpoint %q: exit code = %d, want %d", c.endpoint, code, c.wantCode)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
