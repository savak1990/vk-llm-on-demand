package obs

import (
	"fmt"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var DeniedMetricLabels = []string{"request_id", "key", "instance_id", "ip"}

// AuditCardinality gathers reg and returns one error naming every
// family×label whose name matches denylist case-insensitively, so a
// label added later without review still gets caught.
func AuditCardinality(reg *prometheus.Registry, denylist []string) error {
	families, err := reg.Gather()
	if err != nil {
		return fmt.Errorf("obs: gather: %w", err)
	}
	seen := map[string]bool{}
	var msgs []string
	for _, f := range families {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				for _, denied := range denylist {
					if !strings.EqualFold(l.GetName(), denied) {
						continue
					}
					msg := fmt.Sprintf("%s carries denylisted label %q", f.GetName(), l.GetName())
					if !seen[msg] {
						seen[msg] = true
						msgs = append(msgs, msg)
					}
				}
			}
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("obs: cardinality audit failed:\n%s", strings.Join(msgs, "\n"))
}
