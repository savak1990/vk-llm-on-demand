package gateway

import (
	"encoding/json"
	"strings"
)

// stripReasoning removes thinking lines prefixed with either progress
// prefix, each with the blank line after it, from a reasoning_content string.
func stripReasoning(rc string) (string, bool) {
	if !strings.Contains(rc, progressPrefix) && !strings.Contains(rc, legacyProgressPrefix) {
		return rc, false
	}
	lines := strings.Split(rc, "\n")
	kept := lines[:0]
	for j := 0; j < len(lines); j++ {
		if strings.HasPrefix(lines[j], progressPrefix) || strings.HasPrefix(lines[j], legacyProgressPrefix) {
			if j+1 < len(lines)-1 && strings.TrimRight(lines[j+1], "\r") == "" {
				j++
			}
			continue
		}
		kept = append(kept, lines[j])
	}
	if len(kept) == len(lines) {
		return rc, false
	}
	return strings.Join(kept, "\n"), true
}

// stripVisibleLine removes the one visible status line and the blank line
// after it from the start of an assistant message's content, if present.
func stripVisibleLine(content string) (string, bool) {
	if !strings.HasPrefix(content, visibleStatusLine) {
		return content, false
	}
	rest := content[len(visibleStatusLine):]
	switch {
	case strings.HasPrefix(rest, "\r\n\r\n"):
		return rest[4:], true
	case strings.HasPrefix(rest, "\n\n"):
		return rest[2:], true
	default:
		return rest, true
	}
}

// stripProgress removes the gateway's own progress lines and visible status
// line that a client resends; it leaves every other byte of the body alone.
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
		var role string
		_ = json.Unmarshal(m["role"], &role)
		edited := false

		if role == "assistant" {
			var content string
			if json.Unmarshal(m["content"], &content) == nil {
				if out, ok := stripVisibleLine(content); ok {
					m["content"], _ = json.Marshal(out)
					edited = true
				}
			}
		}

		var rc string
		if json.Unmarshal(m["reasoning_content"], &rc) == nil {
			if out, ok := stripReasoning(rc); ok {
				edited = true
				if strings.TrimSpace(out) == "" {
					delete(m, "reasoning_content")
				} else {
					m["reasoning_content"], _ = json.Marshal(out)
				}
			}
		}

		if !edited {
			continue
		}
		changed = true
		msgs[i], _ = json.Marshal(m)
	}
	if changed {
		body["messages"], _ = json.Marshal(msgs)
	}
}
