package gateway

import (
	"encoding/json"
	"strings"
)

// stripProgress removes the gateway's own progress lines from reasoning
// that a client resends; it leaves every other byte of the body alone.
func stripProgress(body map[string]json.RawMessage) {
	var msgs []json.RawMessage
	if json.Unmarshal(body["messages"], &msgs) != nil {
		return
	}
	changed := false
	for i, raw := range msgs {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		var rc string
		if json.Unmarshal(m["reasoning_content"], &rc) != nil || !strings.Contains(rc, progressPrefix) {
			continue
		}
		lines := strings.Split(rc, "\n")
		kept := lines[:0]
		for j := 0; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], progressPrefix) {
				if j+1 < len(lines)-1 && strings.TrimRight(lines[j+1], "\r") == "" {
					j++
				}
				continue
			}
			kept = append(kept, lines[j])
		}
		if len(kept) == len(lines) {
			continue
		}
		changed = true
		if out := strings.Join(kept, "\n"); strings.TrimSpace(out) == "" {
			delete(m, "reasoning_content")
		} else {
			m["reasoning_content"], _ = json.Marshal(out)
		}
		msgs[i], _ = json.Marshal(m)
	}
	if changed {
		body["messages"], _ = json.Marshal(msgs)
	}
}
