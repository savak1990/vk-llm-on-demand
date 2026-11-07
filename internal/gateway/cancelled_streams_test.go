package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
)

func noopTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCancelledStreamsTotalIncrementsOn499(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newGatewayMetrics(reg)
	g := &Gateway{deps: Deps{Clock: fakeclock.New(time.Unix(0, 0)), Log: noopTestLogger()}, metrics: m}

	h := g.accessLogAndMetrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		setCodeOverride(w, "499")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil))

	families, _ := reg.Gather()
	for _, f := range families {
		if f.GetName() == "vkllm_gateway_cancelled_streams_total" {
			if v := f.GetMetric()[0].GetCounter().GetValue(); v != 1 {
				t.Fatalf("cancelled_streams_total = %v, want 1", v)
			}
			return
		}
	}
	t.Fatal("vkllm_gateway_cancelled_streams_total not registered")
}

func TestCancelledStreamsTotalNotIncrementedOnOtherCodes(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newGatewayMetrics(reg)
	g := &Gateway{deps: Deps{Clock: fakeclock.New(time.Unix(0, 0)), Log: noopTestLogger()}, metrics: m}

	h := g.accessLogAndMetrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil))

	families, _ := reg.Gather()
	for _, f := range families {
		if f.GetName() == "vkllm_gateway_cancelled_streams_total" {
			if v := f.GetMetric()[0].GetCounter().GetValue(); v != 0 {
				t.Fatalf("cancelled_streams_total = %v, want 0 on a 200", v)
			}
			return
		}
	}
	t.Fatal("vkllm_gateway_cancelled_streams_total not registered")
}
