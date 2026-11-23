package gateway

import (
	"encoding/json"
	"testing"
)

func TestStripProgress(t *testing.T) {
	body := map[string]json.RawMessage{
		"messages": json.RawMessage(`[{"role":"user","content":"q"},{"role":"assistant","content":"a","reasoning_content":"[vk-llm] A\n\nreal thought\n[vk-llm] B\n\n"}]`),
	}
	stripProgress(body)
	var msgs []map[string]any
	_ = json.Unmarshal(body["messages"], &msgs)
	if got := msgs[1]["reasoning_content"]; got != "real thought\n" {
		t.Fatalf("reasoning_content = %q", got)
	}
	if msgs[0]["content"] != "q" || msgs[1]["content"] != "a" {
		t.Fatalf("other fields changed: %v", msgs)
	}
}

func TestStripProgressDropsEmptyField(t *testing.T) {
	body := map[string]json.RawMessage{"messages": json.RawMessage(`[{"role":"assistant","content":"a","reasoning_content":"[vk-llm] Ready.\n\n"}]`)}
	stripProgress(body)
	var msgs []map[string]any
	_ = json.Unmarshal(body["messages"], &msgs)
	if _, ok := msgs[0]["reasoning_content"]; ok {
		t.Fatalf("empty reasoning_content kept: %v", msgs[0])
	}
}

func TestStripProgressLeavesOtherBodiesAlone(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":"[vk-llm] typed by a user"}]`)
	body := map[string]json.RawMessage{"messages": raw}
	stripProgress(body)
	if string(body["messages"]) != string(raw) {
		t.Fatalf("messages rewritten: %s", body["messages"])
	}
	stripProgress(map[string]json.RawMessage{"messages": json.RawMessage(`"not an array"`)})
}
