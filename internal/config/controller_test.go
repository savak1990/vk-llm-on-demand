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
		"VKLLM_LIFECYCLE_SAFETY_MAX_DOWNLOAD_PRICE_USD_PER_GB", "VKLLM_LIFECYCLE_SAFETY_MAX_STORAGE_PRICE_USD_PER_GB_MONTH", "VKLLM_LIFECYCLE_SAFETY_MAX_DISK_GB",
		"VKLLM_CONTROLLER_RECONCILE_INTERVAL",
		"VKLLM_TUNNEL_DIALER", "VKLLM_TUNNEL_DIRECT_ADDR",
		"VKLLM_CACHE_MAX_CACHES", "VKLLM_CACHE_RETENTION", "VKLLM_CACHE_PREFER_THRESHOLD", "VKLLM_CACHE_MAX_STORAGE_PRICE_USD_PER_GB_MONTH",
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

func TestLoadControllerDemandGrace(t *testing.T) {
	c, err := LoadController()
	if err != nil {
		t.Fatalf("LoadController: %v", err)
	}
	if c.DemandGrace != 60*time.Second {
		t.Fatalf("DemandGrace %v, want 60s", c.DemandGrace)
	}
}

func TestLoadControllerRejectsNonPositiveDemandGrace(t *testing.T) {
	for _, v := range []string{"0s", "-1s"} {
		clearControllerEnv(t)
		t.Setenv("VKLLM_ONDEMAND_DEMAND_GRACE", v)
		if _, err := LoadController(); err == nil {
			t.Fatalf("LoadController: want error for VKLLM_ONDEMAND_DEMAND_GRACE=%s, got nil", v)
		}
	}
}

func TestLoadControllerCostCapDefaults(t *testing.T) {
	clearControllerEnv(t)
	c, err := LoadController()
	if err != nil {
		t.Fatalf("LoadController: %v", err)
	}
	if c.MaxDownloadPriceUSDPerGB != 0.10 || c.MaxStoragePriceUSDPerGBMonth != 1.00 || c.MaxDiskGB != 200 {
		t.Fatalf("caps = %v %v %v, want 0.10 1.00 200", c.MaxDownloadPriceUSDPerGB, c.MaxStoragePriceUSDPerGBMonth, c.MaxDiskGB)
	}
}

func TestLoadControllerRejectsBadCostCaps(t *testing.T) {
	const dl, st, disk = "VKLLM_LIFECYCLE_SAFETY_MAX_DOWNLOAD_PRICE_USD_PER_GB", "VKLLM_LIFECYCLE_SAFETY_MAX_STORAGE_PRICE_USD_PER_GB_MONTH", "VKLLM_LIFECYCLE_SAFETY_MAX_DISK_GB"
	for _, c := range []struct{ key, val string }{
		{dl, "0"}, {dl, "-1"}, {dl, "NaN"}, {dl, "+Inf"},
		{st, "0"}, {st, "-1"}, {st, "NaN"}, {st, "+Inf"},
		{disk, "59"}, // below the default minDiskGB of 60
	} {
		t.Run(c.key+"="+c.val, func(t *testing.T) {
			clearControllerEnv(t)
			t.Setenv(c.key, c.val)
			if _, err := LoadController(); err == nil {
				t.Fatalf("LoadController accepted %s=%s", c.key, c.val)
			}
		})
	}
}

func TestLoadControllerCacheDefaults(t *testing.T) {
	clearControllerEnv(t)
	c, err := LoadController()
	if err != nil {
		t.Fatalf("LoadController: %v", err)
	}
	want := CacheConfig{MaxCaches: 1, Retention: 72 * time.Hour, PreferThreshold: 0.15, MaxStoragePriceUSDPerGBMonth: 0.40}
	if c.Cache != want {
		t.Fatalf("Cache = %+v, want %+v", c.Cache, want)
	}
}

func TestLoadControllerCacheValidation(t *testing.T) {
	for _, bad := range []struct{ key, val string }{
		{"VKLLM_CACHE_MAX_CACHES", "-1"}, {"VKLLM_CACHE_MAX_CACHES", "4"},
		{"VKLLM_CACHE_RETENTION", "0s"}, {"VKLLM_CACHE_PREFER_THRESHOLD", "1"},
		{"VKLLM_CACHE_PREFER_THRESHOLD", "-0.1"}, {"VKLLM_CACHE_MAX_STORAGE_PRICE_USD_PER_GB_MONTH", "0"},
	} {
		clearControllerEnv(t)
		t.Setenv(bad.key, bad.val)
		if _, err := LoadController(); err == nil {
			t.Fatalf("%s=%s: want an error", bad.key, bad.val)
		}
	}
}

func TestLoadControllerRejectsBadDiskCaps(t *testing.T) {
	const minKey, maxKey = "VKLLM_PROVIDER_FILTER_MIN_DISK_GB", "VKLLM_LIFECYCLE_SAFETY_MAX_DISK_GB"
	for name, env := range map[string]map[string]string{
		"min=0,max=0":  {minKey: "0", maxKey: "0"},
		"min=0,max=-5": {minKey: "0", maxKey: "-5"},
		"min=-1":       {minKey: "-1"},
	} {
		t.Run(name, func(t *testing.T) {
			clearControllerEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := LoadController(); err == nil {
				t.Fatalf("LoadController accepted %v", env)
			}
		})
	}
}
