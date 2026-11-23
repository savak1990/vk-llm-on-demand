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

func TestStripProgressCRLF(t *testing.T) {
	body := map[string]json.RawMessage{
		"messages": json.RawMessage(`[{"role":"assistant","content":"a","reasoning_content":"[vk-llm] A\r\n\r\nreal thought\r\n"}]`),
	}
	stripProgress(body)
	var msgs []map[string]any
	_ = json.Unmarshal(body["messages"], &msgs)
	if got := msgs[0]["reasoning_content"]; got != "real thought\r\n" {
		t.Fatalf("reasoning_content = %q, want %q", got, "real thought\r\n")
	}
}

func TestStripProgressPreservesUnchangedMessages(t *testing.T) {
	raw := json.RawMessage(`[{"role":"user","content":"q","extra":"z"},{"role":"assistant","reasoning_content":"well [vk-llm] mid-line"}]`)
	body := map[string]json.RawMessage{"messages": raw}
	stripProgress(body)
	if string(body["messages"]) != string(raw) {
		t.Fatalf("messages rewritten: got %s, want %s", body["messages"], raw)
	}
	var msgs []map[string]any
	_ = json.Unmarshal(body["messages"], &msgs)
	if msgs[0]["extra"] != "z" {
		t.Fatalf("message 1 corrupted: %v", msgs[0])
	}
}

func TestStripProgressPreservesOtherMessagesWhenOneChanged(t *testing.T) {
	body := map[string]json.RawMessage{
		"messages": json.RawMessage(`[{"role":"user","content":"q","extra":"z"},{"role":"assistant","reasoning_content":"[vk-llm] Ready.\n\n"}]`),
	}
	stripProgress(body)
	var msgs []map[string]any
	_ = json.Unmarshal(body["messages"], &msgs)
	if msgs[0]["extra"] != "z" {
		t.Fatalf("message 1 corrupted: expected extra=z, got %v", msgs[0])
	}
	if _, ok := msgs[1]["reasoning_content"]; ok {
		t.Fatalf("message 2 reasoning_content should be dropped when empty")
	}
}
