// Package config loads and validates environment configuration.
package config

import (
	"fmt"
	"os"
)

type Config struct {
	InstallationID     string
	StateConfigMapName string
}

func Load() (Config, error) {
	id := os.Getenv("VKLLM_INSTALLATION_ID")
	if id == "" {
		return Config{}, fmt.Errorf("config: VKLLM_INSTALLATION_ID is required")
	}
	name := os.Getenv("VKLLM_STATE_CONFIGMAP")
	if name == "" {
		return Config{}, fmt.Errorf("config: VKLLM_STATE_CONFIGMAP is required")
	}
	return Config{InstallationID: id, StateConfigMapName: name}, nil
}
