package config

import "testing"
import "time"

func clearWorkerEnv(t *testing.T) {
	for _, k := range []string{
		"VKLLM_MODEL_URL", "VKLLM_MODEL_SHA256", "VKLLM_MODEL_PARALLEL",
		"VKLLM_WORKER_IMAGE", "VKLLM_WORKER_DEAD_MAN_TIMEOUT",
		"VKLLM_PROVIDER_API_KEY_FILE", "VKLLM_TUNNEL_SSH_KEY_FILE",
	} {
		t.Setenv(k, "")
	}
}

const validSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85"

func TestLoadWorkerDefaults(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("VKLLM_MODEL_URL", "https://huggingface.co/example/model.gguf")
	t.Setenv("VKLLM_MODEL_SHA256", validSHA256)
	t.Setenv("VKLLM_WORKER_IMAGE", "ghcr.io/example/worker@sha256:"+validSHA256)

	w, err := LoadWorker(30 * time.Minute)
	if err != nil {
		t.Fatalf("LoadWorker: %v", err)
	}
	if w.ModelParallel != 1 {
		t.Errorf("ModelParallel = %d, want 1", w.ModelParallel)
	}
	if w.WorkerDeadManTimeout != 40*time.Minute {
		t.Errorf("WorkerDeadManTimeout = %v, want 40m (idle timeout + 10m)", w.WorkerDeadManTimeout)
	}
	if w.ProviderAPIKeyFile != "/etc/vkllm/secrets/provider/VAST_API_KEY" {
		t.Errorf("ProviderAPIKeyFile = %q", w.ProviderAPIKeyFile)
	}
	if w.SSHPrivateKeyFile != "/etc/vkllm/secrets/ssh/id_ed25519" {
		t.Errorf("SSHPrivateKeyFile = %q", w.SSHPrivateKeyFile)
	}
}

func TestLoadWorkerOverridesAndRequired(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("VKLLM_MODEL_URL", "https://huggingface.co/example/model.gguf")
	t.Setenv("VKLLM_MODEL_SHA256", validSHA256)
	t.Setenv("VKLLM_WORKER_IMAGE", "ghcr.io/example/worker@sha256:"+validSHA256)
	t.Setenv("VKLLM_MODEL_PARALLEL", "2")
	t.Setenv("VKLLM_WORKER_DEAD_MAN_TIMEOUT", "5m")

	w, err := LoadWorker(30 * time.Minute)
	if err != nil {
		t.Fatalf("LoadWorker: %v", err)
	}
	if w.ModelParallel != 2 {
		t.Errorf("ModelParallel = %d, want 2", w.ModelParallel)
	}
	if w.WorkerDeadManTimeout != 5*time.Minute {
		t.Errorf("WorkerDeadManTimeout = %v, want 5m (explicit override)", w.WorkerDeadManTimeout)
	}
}

func TestLoadWorkerRequiresModelURLSHA256Image(t *testing.T) {
	for _, missing := range []string{"VKLLM_MODEL_URL", "VKLLM_MODEL_SHA256", "VKLLM_WORKER_IMAGE"} {
		clearWorkerEnv(t)
		t.Setenv("VKLLM_MODEL_URL", "https://huggingface.co/example/model.gguf")
		t.Setenv("VKLLM_MODEL_SHA256", validSHA256)
		t.Setenv("VKLLM_WORKER_IMAGE", "ghcr.io/example/worker@sha256:"+validSHA256)
		t.Setenv(missing, "")

		if _, err := LoadWorker(30 * time.Minute); err == nil {
			t.Fatalf("LoadWorker with %s unset: want error, got nil", missing)
		}
	}
}
