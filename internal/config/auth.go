package config

// Auth configures internal/auth's three key-file paths; the files
// themselves are Secret-mounted paths the platform owns.
type Auth struct {
	ClientKeysFile       string
	ManagementTokensFile string
	WorkerSeedFile       string
}

func LoadAuth() (Auth, error) {
	var a Auth
	a.ClientKeysFile = envString("VKLLM_AUTH_CLIENT_KEYS_FILE", "/etc/vkllm/api-keys/client-keys")
	a.ManagementTokensFile = envString("VKLLM_AUTH_MANAGEMENT_TOKENS_FILE", "/etc/vkllm/api-keys/management-tokens")
	a.WorkerSeedFile = envString("VKLLM_AUTH_WORKER_SEED_FILE", "/etc/vkllm/api-keys/worker-seed")
	return a, nil
}
