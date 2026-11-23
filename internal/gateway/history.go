package gateway

import (
	"encoding/json"
	"strings"
)

// stripProgress removes the gateway's own progress lines from reasoning
// that a client resends; it leaves every other byte of the body alone.
func stripProgress(body map[string]json.RawMessage) {
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(body["messages"], &msgs) != nil {
		return
	}
	changed := false
	for _, m := range msgs {
		var rc string
		if json.Unmarshal(m["reasoning_content"], &rc) != nil || !strings.Contains(rc, progressPrefix) {
			continue
		}
		lines := strings.Split(rc, "\n")
		kept := lines[:0]
		for i := 0; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], progressPrefix) {
				if i+1 < len(lines)-1 && lines[i+1] == "" {
					i++
				}
				continue
			}
			kept = append(kept, lines[i])
		}
		changed = true
		if out := strings.Join(kept, "\n"); strings.TrimSpace(out) == "" {
			delete(m, "reasoning_content")
		} else {
			m["reasoning_content"], _ = json.Marshal(out)
		}
	}
	if changed {
		body["messages"], _ = json.Marshal(msgs)
	}
}
