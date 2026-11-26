package config

import (
	"fmt"
	"math"
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

	IdleShutdownEnabled          bool
	IdleShutdownTimeout          time.Duration
	ShutdownDrainTimeout         time.Duration
	ShutdownAllowForce           bool
	StartupTimeout               time.Duration
	MaxHourlyPriceUSD            float64
	MaxDownloadPriceUSDPerGB     float64
	MaxStoragePriceUSDPerGBMonth float64
	MaxDiskGB                    int
	MaxLifetime                  time.Duration
	MaxInstanceSpendUSD          float64
	MaxProvisionAttemptsPerHour  int
	DemandGrace                  time.Duration

	ReconcileInterval time.Duration

	TunnelDialer     string
	TunnelDirectAddr string

	Cache CacheConfig
}

// CacheConfig bounds the model cache on Vast.ai volumes.
type CacheConfig struct {
	MaxCaches                    int
	Retention                    time.Duration
	PreferThreshold              float64
	MaxStoragePriceUSDPerGBMonth float64
}

// ProviderFilter mirrors provider.OfferFilter minus the caps (set from
// the Controller's safety fields) and ExcludeMachineIDs (from the live
// denylist); MinDiskGB is the floor that the per-search disk size starts from.
type ProviderFilter struct {
	GPUName         string
	NumGPUs         int
	MinCPUCores     int
	MinCPURAMGB     int
	MinDiskGB       int
	MinReliability  float64
	MinInetDownMbps float64
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
	if c.ProviderFilter.MinDiskGB < 0 {
		return Controller{}, fmt.Errorf("config: VKLLM_PROVIDER_FILTER_MIN_DISK_GB must be >= 0, got %d", c.ProviderFilter.MinDiskGB)
	}
	if c.ProviderFilter.MinReliability, err = envFloat("VKLLM_PROVIDER_FILTER_MIN_RELIABILITY", 0.95); err != nil {
		return Controller{}, err
	}
	if c.ProviderFilter.MinInetDownMbps, err = envFloat("VKLLM_PROVIDER_FILTER_MIN_INET_DOWN_MBPS", 500); err != nil {
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
	if c.MaxDownloadPriceUSDPerGB, err = envFloat("VKLLM_LIFECYCLE_SAFETY_MAX_DOWNLOAD_PRICE_USD_PER_GB", 0.10); err != nil {
		return Controller{}, err
	}
	if c.MaxStoragePriceUSDPerGBMonth, err = envFloat("VKLLM_LIFECYCLE_SAFETY_MAX_STORAGE_PRICE_USD_PER_GB_MONTH", 1.00); err != nil {
		return Controller{}, err
	}
	if c.MaxDiskGB, err = envInt("VKLLM_LIFECYCLE_SAFETY_MAX_DISK_GB", 200); err != nil {
		return Controller{}, err
	}
	if c.MaxDiskGB < 1 {
		return Controller{}, fmt.Errorf("config: VKLLM_LIFECYCLE_SAFETY_MAX_DISK_GB must be >= 1, got %d", c.MaxDiskGB)
	}
	if !positiveFinite(c.MaxDownloadPriceUSDPerGB) {
		return Controller{}, fmt.Errorf("config: VKLLM_LIFECYCLE_SAFETY_MAX_DOWNLOAD_PRICE_USD_PER_GB must be a positive finite number, got %v", c.MaxDownloadPriceUSDPerGB)
	}
	if !positiveFinite(c.MaxStoragePriceUSDPerGBMonth) {
		return Controller{}, fmt.Errorf("config: VKLLM_LIFECYCLE_SAFETY_MAX_STORAGE_PRICE_USD_PER_GB_MONTH must be a positive finite number, got %v", c.MaxStoragePriceUSDPerGBMonth)
	}
	if c.MaxDiskGB < c.ProviderFilter.MinDiskGB {
		return Controller{}, fmt.Errorf("config: VKLLM_LIFECYCLE_SAFETY_MAX_DISK_GB (%d) must be >= VKLLM_PROVIDER_FILTER_MIN_DISK_GB (%d)", c.MaxDiskGB, c.ProviderFilter.MinDiskGB)
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

	if c.DemandGrace, err = envDuration("VKLLM_ONDEMAND_DEMAND_GRACE", 60*time.Second); err != nil {
		return Controller{}, err
	}
	if c.DemandGrace <= 0 {
		return Controller{}, fmt.Errorf("config: VKLLM_ONDEMAND_DEMAND_GRACE must be positive, got %v", c.DemandGrace)
	}

	if c.Cache.MaxCaches, err = envInt("VKLLM_CACHE_MAX_CACHES", 1); err != nil {
		return Controller{}, err
	}
	if c.Cache.MaxCaches < 0 || c.Cache.MaxCaches > 3 {
		return Controller{}, fmt.Errorf("config: VKLLM_CACHE_MAX_CACHES must be 0..3, got %d", c.Cache.MaxCaches)
	}
	if c.Cache.Retention, err = envDuration("VKLLM_CACHE_RETENTION", 72*time.Hour); err != nil {
		return Controller{}, err
	}
	if c.Cache.Retention <= 0 {
		return Controller{}, fmt.Errorf("config: VKLLM_CACHE_RETENTION must be > 0, got %v", c.Cache.Retention)
	}
	if c.Cache.PreferThreshold, err = envFloat("VKLLM_CACHE_PREFER_THRESHOLD", 0.15); err != nil {
		return Controller{}, err
	}
	if c.Cache.PreferThreshold < 0 || c.Cache.PreferThreshold >= 1 {
		return Controller{}, fmt.Errorf("config: VKLLM_CACHE_PREFER_THRESHOLD must be in [0, 1), got %v", c.Cache.PreferThreshold)
	}
	if c.Cache.MaxStoragePriceUSDPerGBMonth, err = envFloat("VKLLM_CACHE_MAX_STORAGE_PRICE_USD_PER_GB_MONTH", 0.40); err != nil {
		return Controller{}, err
	}
	if !positiveFinite(c.Cache.MaxStoragePriceUSDPerGBMonth) {
		return Controller{}, fmt.Errorf("config: VKLLM_CACHE_MAX_STORAGE_PRICE_USD_PER_GB_MONTH must be a positive finite number, got %v", c.Cache.MaxStoragePriceUSDPerGBMonth)
	}
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

// positiveFinite also rejects NaN, which every comparison reports as false.
func positiveFinite(v float64) bool { return v > 0 && !math.IsInf(v, 0) }
