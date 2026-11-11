package main

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the fully resolved CLI configuration: flag > environment > file > default.
type Config struct {
	Endpoint       string
	TokenEnv       string
	Token          string
	StartupTimeout time.Duration
	WaitForReady   bool
}

type fileConfig struct {
	API struct {
		Endpoint string `yaml:"endpoint"`
		TokenEnv string `yaml:"tokenEnv"`
	} `yaml:"api"`
	Defaults struct {
		StartupTimeout string `yaml:"startupTimeout"`
		WaitForReady   *bool  `yaml:"waitForReady"`
	} `yaml:"defaults"`
}

func defaultConfigPath(getenv func(string) string) string {
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".config", "vk-llm", "config.yaml")
}

// loadConfig resolves configuration from flags, the environment and the
// config file, in that order, and validates the result before any network
// call. usage carries a human-readable reason when exitCode is non-zero.
func loadConfig(args []string, getenv func(string) string) (cfg Config, exitCode int, usage string) {
	fs := flag.NewFlagSet("vk-llm", flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	endpointFlag := fs.String("endpoint", "", "management API endpoint")
	tokenEnvFlag := fs.String("token-env", "", "environment variable holding the management token")
	configFlag := fs.String("config", "", "path to config.yaml")
	fs.String("timeout", "", "") // consumed by commands.go in Task 4; parsed here only to avoid "flag provided but not defined"
	fs.Bool("detach", false, "")
	fs.Bool("json", false, "")
	fs.String("force", "", "")
	if err := fs.Parse(args); err != nil {
		return Config{}, 2, err.Error()
	}

	cfg.TokenEnv = "VK_LLM_TOKEN"
	cfg.StartupTimeout = 15 * time.Minute
	cfg.WaitForReady = true

	path := *configFlag
	if path == "" {
		path = defaultConfigPath(getenv)
	}
	if data, err := os.ReadFile(path); err == nil { // nolint:gosec
		if msg := rejectTokenKey(data); msg != "" {
			return Config{}, 2, msg
		}
		var fc fileConfig
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&fc); err != nil {
			return Config{}, 2, fmt.Sprintf("bad config file: %v", err)
		}
		if fc.API.Endpoint != "" {
			cfg.Endpoint = fc.API.Endpoint
		}
		if fc.API.TokenEnv != "" {
			cfg.TokenEnv = fc.API.TokenEnv
		}
		if fc.Defaults.StartupTimeout != "" {
			if d, err := time.ParseDuration(fc.Defaults.StartupTimeout); err == nil {
				cfg.StartupTimeout = d
			}
		}
		if fc.Defaults.WaitForReady != nil {
			cfg.WaitForReady = *fc.Defaults.WaitForReady
		}
	}

	if v := getenv("VK_LLM_API_ENDPOINT"); v != "" {
		cfg.Endpoint = v
	}
	if *tokenEnvFlag != "" {
		cfg.TokenEnv = *tokenEnvFlag
	}
	if *endpointFlag != "" {
		cfg.Endpoint = *endpointFlag
	}

	if cfg.Endpoint == "" {
		return Config{}, 2, "no endpoint configured"
	}
	if msg := rejectNonLoopbackHTTP(cfg.Endpoint); msg != "" {
		return Config{}, 2, msg
	}

	cfg.Token = getenv(cfg.TokenEnv)
	if cfg.Token == "" {
		return Config{}, 2, fmt.Sprintf("%s is not set", cfg.TokenEnv)
	}

	return cfg, 0, ""
}

// rejectTokenKey does a structural YAML walk for any key literally named
// "token" (case-insensitive) at any depth, since the file must never hold one.
func rejectTokenKey(data []byte) string {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return ""
	}
	if hasTokenKey(raw) {
		return "config file must not contain a token; set VK_LLM_TOKEN instead"
	}
	return ""
}

func hasTokenKey(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for k, val := range m {
		if strings.EqualFold(k, "token") {
			return true
		}
		if hasTokenKey(val) {
			return true
		}
	}
	return false
}

func rejectNonLoopbackHTTP(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Sprintf("bad endpoint: %v", err)
	}
	if u.Scheme != "http" {
		return ""
	}
	host := u.Hostname()
	if host == "localhost" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	return "plain http:// is only allowed to a loopback address"
}
