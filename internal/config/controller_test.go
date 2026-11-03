package config

import (
	"testing"
	"time"
)

func clearControllerEnv(t *testing.T) {
	for _, k := range []string{
		"VKLLM_LIFECYCLE_PROVISIONING_MODE",
		"VKLLM_PROVIDER_FILTER_GPU_NAME", "VKLLM_PROVIDER_FILTER_NUM_GPUS",
		"VKLLM_PROVIDER_FILTER_MIN_CPU_CORES", "VKLLM_PROVIDER_FILTER_MIN_CPU_RAM_GB",
		"VKLLM_PROVIDER_FILTER_MIN_DISK_GB", "VKLLM_PROVIDER_FILTER_MIN_RELIABILITY",
		"VKLLM_PROVIDER_FILTER_MIN_INET_DOWN_MBPS", "VKLLM_PROVIDER_FILTER_MIN_DISK_BW_MBPS",
		"VKLLM_PROVIDER_FILTER_MIN_CUDA_VERSION", "VKLLM_PROVIDER_FILTER_GEOLOCATION",
		"VKLLM_LIFECYCLE_IDLE_SHUTDOWN_ENABLED", "VKLLM_LIFECYCLE_IDLE_SHUTDOWN_TIMEOUT",
		"VKLLM_LIFECYCLE_SHUTDOWN_DRAIN_TIMEOUT", "VKLLM_LIFECYCLE_SHUTDOWN_ALLOW_FORCE",
		"VKLLM_LIFECYCLE_STARTUP_TIMEOUT", "VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD",
		"VKLLM_LIFECYCLE_SAFETY_MAX_LIFETIME", "VKLLM_LIFECYCLE_SAFETY_MAX_INSTANCE_SPEND_USD",
		"VKLLM_LIFECYCLE_SAFETY_MAX_PROVISION_ATTEMPTS_PER_HOUR",
		"VKLLM_CONTROLLER_RECONCILE_INTERVAL",
		"VKLLM_TUNNEL_DIALER", "VKLLM_TUNNEL_DIRECT_ADDR",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadControllerDefaults(t *testing.T) {
	clearControllerEnv(t)
	c, err := LoadController()
	if err != nil {
		t.Fatalf("LoadController: %v", err)
	}
	if c.ProvisioningMode != "explicit" {
		t.Errorf("ProvisioningMode = %q, want explicit", c.ProvisioningMode)
	}
	if c.ProviderFilter.GPUName != "L40S" || c.ProviderFilter.NumGPUs != 1 || c.ProviderFilter.MinCPUCores != 8 {
		t.Errorf("ProviderFilter = %+v", c.ProviderFilter)
	}
	if c.ProviderFilter.MinReliability != 0.95 || c.ProviderFilter.MinCUDA != 12.8 {
		t.Errorf("ProviderFilter floats = %+v", c.ProviderFilter)
	}
	if c.ProviderFilter.Geolocation != nil {
		t.Errorf("Geolocation = %v, want nil", c.ProviderFilter.Geolocation)
	}
	if !c.IdleShutdownEnabled || c.IdleShutdownTimeout != 30*time.Minute {
		t.Errorf("idle shutdown = %v %v", c.IdleShutdownEnabled, c.IdleShutdownTimeout)
	}
	if c.ShutdownDrainTimeout != 60*time.Second || c.ShutdownAllowForce {
		t.Errorf("shutdown drain/force = %v %v", c.ShutdownDrainTimeout, c.ShutdownAllowForce)
	}
	if c.StartupTimeout != 15*time.Minute {
		t.Errorf("StartupTimeout = %v", c.StartupTimeout)
	}
	if c.MaxHourlyPriceUSD != 0.80 || c.MaxLifetime != 12*time.Hour || c.MaxInstanceSpendUSD != 0 || c.MaxProvisionAttemptsPerHour != 3 {
		t.Errorf("safety fields = %+v", c)
	}
	if c.ReconcileInterval != 60*time.Second {
		t.Errorf("ReconcileInterval = %v", c.ReconcileInterval)
	}
	if c.TunnelDialer != "ssh" || c.TunnelDirectAddr != "" {
		t.Errorf("tunnel = %q %q", c.TunnelDialer, c.TunnelDirectAddr)
	}
}

func TestLoadControllerOverrides(t *testing.T) {
	clearControllerEnv(t)
	t.Setenv("VKLLM_LIFECYCLE_IDLE_SHUTDOWN_TIMEOUT", "45m")
	t.Setenv("VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD", "1.25")
	t.Setenv("VKLLM_PROVIDER_FILTER_NUM_GPUS", "2")
	t.Setenv("VKLLM_LIFECYCLE_SHUTDOWN_ALLOW_FORCE", "true")
	t.Setenv("VKLLM_PROVIDER_FILTER_GEOLOCATION", "US, DE ,")
	t.Setenv("VKLLM_TUNNEL_DIALER", "direct")
	t.Setenv("VKLLM_TUNNEL_DIRECT_ADDR", "127.0.0.1:9000")

	c, err := LoadController()
	if err != nil {
		t.Fatalf("LoadController: %v", err)
	}
	if c.IdleShutdownTimeout != 45*time.Minute {
		t.Errorf("IdleShutdownTimeout = %v", c.IdleShutdownTimeout)
	}
	if c.MaxHourlyPriceUSD != 1.25 {
		t.Errorf("MaxHourlyPriceUSD = %v", c.MaxHourlyPriceUSD)
	}
	if c.ProviderFilter.NumGPUs != 2 {
		t.Errorf("NumGPUs = %v", c.ProviderFilter.NumGPUs)
	}
	if !c.ShutdownAllowForce {
		t.Error("ShutdownAllowForce = false, want true")
	}
	if want := []string{"US", "DE"}; len(c.ProviderFilter.Geolocation) != 2 || c.ProviderFilter.Geolocation[0] != want[0] || c.ProviderFilter.Geolocation[1] != want[1] {
		t.Errorf("Geolocation = %v, want %v", c.ProviderFilter.Geolocation, want)
	}
	if c.TunnelDialer != "direct" || c.TunnelDirectAddr != "127.0.0.1:9000" {
		t.Errorf("tunnel = %q %q", c.TunnelDialer, c.TunnelDirectAddr)
	}
}

func TestLoadControllerInvalidTunnelDialer(t *testing.T) {
	clearControllerEnv(t)
	t.Setenv("VKLLM_TUNNEL_DIALER", "bogus")
	if _, err := LoadController(); err == nil {
		t.Fatal("LoadController: want error for invalid VKLLM_TUNNEL_DIALER, got nil")
	}
}

func TestLoadControllerMalformedDuration(t *testing.T) {
	clearControllerEnv(t)
	t.Setenv("VKLLM_LIFECYCLE_IDLE_SHUTDOWN_TIMEOUT", "not-a-duration")
	if _, err := LoadController(); err == nil {
		t.Fatal("LoadController: want error for malformed duration, got nil")
	}
}
