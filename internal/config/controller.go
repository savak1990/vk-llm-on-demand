package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Controller is internal/controller's own env-parsed configuration. It
// never includes the monthly budget cap: the controller only calls
// Budget.Allow, it never reads the cap itself (that belongs to the
// future internal/budget package).
type Controller struct {
	ProvisioningMode string

	ProviderFilter ProviderFilter

	IdleShutdownEnabled         bool
	IdleShutdownTimeout         time.Duration
	ShutdownDrainTimeout        time.Duration
	ShutdownAllowForce          bool
	StartupTimeout              time.Duration
	MaxHourlyPriceUSD           float64
	MaxLifetime                 time.Duration
	MaxInstanceSpendUSD         float64
	MaxProvisionAttemptsPerHour int

	ReconcileInterval time.Duration

	TunnelDialer     string
	TunnelDirectAddr string
}

// ProviderFilter mirrors provider.OfferFilter field-for-field, minus
// MaxHourly (Controller.MaxHourlyPriceUSD) and ExcludeMachineIDs (set
// per search from the live denylist, never from config).
type ProviderFilter struct {
	GPUName         string
	NumGPUs         int
	MinCPUCores     int
	MinCPURAMGB     int
	MinDiskGB       int
	MinReliability  float64
	MinInetDownMBps float64
	MinDiskBwMBps   float64
	MinCUDA         float64
	Geolocation     []string
}

// LoadController reads internal/controller's configuration, applying the
// chart's own defaults (architecture §9) where a variable is unset.
func LoadController() (Controller, error) {
	var c Controller
	var err error

	c.ProvisioningMode = envString("VKLLM_LIFECYCLE_PROVISIONING_MODE", "explicit")

	c.ProviderFilter.GPUName = envString("VKLLM_PROVIDER_FILTER_GPU_NAME", "L40S")
	if c.ProviderFilter.NumGPUs, err = envInt("VKLLM_PROVIDER_FILTER_NUM_GPUS", 1); err != nil {
		return Controller{}, err
	}
	if c.ProviderFilter.MinCPUCores, err = envInt("VKLLM_PROVIDER_FILTER_MIN_CPU_CORES", 8); err != nil {
		return Controller{}, err
	}
	if c.ProviderFilter.MinCPURAMGB, err = envInt("VKLLM_PROVIDER_FILTER_MIN_CPU_RAM_GB", 32); err != nil {
		return Controller{}, err
	}
	if c.ProviderFilter.MinDiskGB, err = envInt("VKLLM_PROVIDER_FILTER_MIN_DISK_GB", 60); err != nil {
		return Controller{}, err
	}
	if c.ProviderFilter.MinReliability, err = envFloat("VKLLM_PROVIDER_FILTER_MIN_RELIABILITY", 0.95); err != nil {
		return Controller{}, err
	}
	if c.ProviderFilter.MinInetDownMBps, err = envFloat("VKLLM_PROVIDER_FILTER_MIN_INET_DOWN_MBPS", 500); err != nil {
		return Controller{}, err
	}
	if c.ProviderFilter.MinDiskBwMBps, err = envFloat("VKLLM_PROVIDER_FILTER_MIN_DISK_BW_MBPS", 500); err != nil {
		return Controller{}, err
	}
	if c.ProviderFilter.MinCUDA, err = envFloat("VKLLM_PROVIDER_FILTER_MIN_CUDA_VERSION", 12.8); err != nil {
		return Controller{}, err
	}
	c.ProviderFilter.Geolocation = envStringList("VKLLM_PROVIDER_FILTER_GEOLOCATION")

	if c.IdleShutdownEnabled, err = envBool("VKLLM_LIFECYCLE_IDLE_SHUTDOWN_ENABLED", true); err != nil {
		return Controller{}, err
	}
	if c.IdleShutdownTimeout, err = envDuration("VKLLM_LIFECYCLE_IDLE_SHUTDOWN_TIMEOUT", 30*time.Minute); err != nil {
		return Controller{}, err
	}
	if c.ShutdownDrainTimeout, err = envDuration("VKLLM_LIFECYCLE_SHUTDOWN_DRAIN_TIMEOUT", 60*time.Second); err != nil {
		return Controller{}, err
	}
	if c.ShutdownAllowForce, err = envBool("VKLLM_LIFECYCLE_SHUTDOWN_ALLOW_FORCE", false); err != nil {
		return Controller{}, err
	}
	if c.StartupTimeout, err = envDuration("VKLLM_LIFECYCLE_STARTUP_TIMEOUT", 15*time.Minute); err != nil {
		return Controller{}, err
	}
	if c.MaxHourlyPriceUSD, err = envFloat("VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD", 0.80); err != nil {
		return Controller{}, err
	}
	if c.MaxLifetime, err = envDuration("VKLLM_LIFECYCLE_SAFETY_MAX_LIFETIME", 12*time.Hour); err != nil {
		return Controller{}, err
	}
	if c.MaxInstanceSpendUSD, err = envFloat("VKLLM_LIFECYCLE_SAFETY_MAX_INSTANCE_SPEND_USD", 0); err != nil {
		return Controller{}, err
	}
	if c.MaxProvisionAttemptsPerHour, err = envInt("VKLLM_LIFECYCLE_SAFETY_MAX_PROVISION_ATTEMPTS_PER_HOUR", 3); err != nil {
		return Controller{}, err
	}

	if c.ReconcileInterval, err = envDuration("VKLLM_CONTROLLER_RECONCILE_INTERVAL", 60*time.Second); err != nil {
		return Controller{}, err
	}

	c.TunnelDialer = envString("VKLLM_TUNNEL_DIALER", "ssh")
	if c.TunnelDialer != "ssh" && c.TunnelDialer != "direct" {
		return Controller{}, fmt.Errorf("config: VKLLM_TUNNEL_DIALER must be ssh or direct, got %q", c.TunnelDialer)
	}
	c.TunnelDirectAddr = envString("VKLLM_TUNNEL_DIRECT_ADDR", "")

	return c, nil
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envStringList(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return n, nil
}

func envFloat(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return f, nil
}

func envBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config: %s: %w", key, err)
	}
	return b, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return d, nil
}
