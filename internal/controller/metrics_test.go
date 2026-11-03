package controller

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRegisterMetricsOneHotGPUState(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	setGPUState(PublicState(Ready, false))

	for _, s := range []string{"STOPPED", "ALLOCATING", "STARTING", "LOADING_MODEL", "READY", "DRAINING", "DELETING", "FAILED"} {
		want := 0.0
		if s == "READY" {
			want = 1.0
		}
		if got := testutil.ToFloat64(gpuState.WithLabelValues(s)); got != want {
			t.Errorf("gpuState{%q} = %v, want %v", s, got, want)
		}
	}
}

func TestShutdownsTotalIncrementsByReason(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	before := testutil.ToFloat64(shutdownsTotal.WithLabelValues("idle"))
	shutdownsTotal.WithLabelValues("idle").Inc()
	if got := testutil.ToFloat64(shutdownsTotal.WithLabelValues("idle")); got != before+1 {
		t.Fatalf("shutdownsTotal{idle} = %v, want %v", got, before+1)
	}
}
