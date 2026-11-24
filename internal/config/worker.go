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
	KCacheType           string
	VCacheType           string
	WorkerImage          string
	WorkerDeadManTimeout time.Duration
	ProviderAPIKeyFile   string
	SSHPrivateKeyFile    string
}

// kvCacheTypes are the llama-server --cache-type-k/-v values this chart lets
// an operator pick; quantized caches need flash attention, already always on.
var kvCacheTypes = map[string]bool{"f16": true, "q8_0": true, "q4_0": true}

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

	w.KCacheType = envString("VKLLM_MODEL_K_CACHE_TYPE", "f16")
	if !kvCacheTypes[w.KCacheType] {
		return Worker{}, fmt.Errorf("config: VKLLM_MODEL_K_CACHE_TYPE must be f16, q8_0 or q4_0, got %q", w.KCacheType)
	}
	w.VCacheType = envString("VKLLM_MODEL_V_CACHE_TYPE", "f16")
	if !kvCacheTypes[w.VCacheType] {
		return Worker{}, fmt.Errorf("config: VKLLM_MODEL_V_CACHE_TYPE must be f16, q8_0 or q4_0, got %q", w.VCacheType)
	}
	w.ProviderAPIKeyFile = envString("VKLLM_PROVIDER_API_KEY_FILE", "/etc/vkllm/secrets/provider/VAST_API_KEY")
	w.SSHPrivateKeyFile = envString("VKLLM_TUNNEL_SSH_KEY_FILE", "/etc/vkllm/secrets/ssh/id_ed25519")
	return w, nil
}
