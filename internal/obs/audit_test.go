package obs

import (
	"regexp"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestAuditCatchesDenylistedLabel(t *testing.T) {
	reg := prometheus.NewRegistry()
	bad := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_bad_metric"}, []string{"instance_id"})
	reg.MustRegister(bad)
	bad.WithLabelValues("i-123").Inc()

	err := AuditCardinality(reg, DeniedMetricLabels)
	if err == nil || !regexp.MustCompile("instance_id").MatchString(err.Error()) {
		t.Fatalf("AuditCardinality = %v, want an error naming instance_id", err)
	}

	clean := prometheus.NewRegistry()
	cleanCounter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_clean_metric"}, []string{"route"})
	clean.MustRegister(cleanCounter)
	cleanCounter.WithLabelValues("chat_completions").Inc()
	if err := AuditCardinality(clean, DeniedMetricLabels); err != nil {
		t.Fatalf("AuditCardinality on a clean registry = %v, want nil", err)
	}
}
