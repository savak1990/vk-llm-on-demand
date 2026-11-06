package config

import (
	"fmt"
	"time"
)

// Worker is cmd/gpu-on-demand's own configuration for the model and
// worker image — nothing else in this package reads these values.
type Worker struct {
	ModelURL             string
	ModelSHA256          string
	ModelParallel        int
	WorkerImage          string
	WorkerDeadManTimeout time.Duration
	ProviderAPIKeyFile   string
	SSHPrivateKeyFile    string
}

// LoadWorker reads cmd/gpu-on-demand's model/worker configuration.
// idleShutdownTimeout is config.Controller's own value, passed in so
// WorkerDeadManTimeout's default has exactly one source.
func LoadWorker(idleShutdownTimeout time.Duration) (Worker, error) {
	var w Worker

	w.ModelURL = envString("VKLLM_MODEL_URL", "")
	if w.ModelURL == "" {
		return Worker{}, fmt.Errorf("config: VKLLM_MODEL_URL is required")
	}
	w.ModelSHA256 = envString("VKLLM_MODEL_SHA256", "")
	if w.ModelSHA256 == "" {
		return Worker{}, fmt.Errorf("config: VKLLM_MODEL_SHA256 is required")
	}
	w.WorkerImage = envString("VKLLM_WORKER_IMAGE", "")
	if w.WorkerImage == "" {
		return Worker{}, fmt.Errorf("config: VKLLM_WORKER_IMAGE is required")
	}

	var err error
	if w.ModelParallel, err = envInt("VKLLM_MODEL_PARALLEL", 1); err != nil {
		return Worker{}, err
	}
	if w.WorkerDeadManTimeout, err = envDuration("VKLLM_WORKER_DEAD_MAN_TIMEOUT", idleShutdownTimeout+10*time.Minute); err != nil {
		return Worker{}, err
	}
	w.ProviderAPIKeyFile = envString("VKLLM_PROVIDER_API_KEY_FILE", "/etc/vkllm/secrets/provider/VAST_API_KEY")
	w.SSHPrivateKeyFile = envString("VKLLM_TUNNEL_SSH_KEY_FILE", "/etc/vkllm/secrets/ssh/id_ed25519")
	return w, nil
}
