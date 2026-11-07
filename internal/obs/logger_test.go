package obs

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactionInlineAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	logger.Info("request", "authorization", "Bearer sk-vkllm-secret", "route", "chat_completions")
	out := buf.String()
	if strings.Contains(out, "sk-vkllm-secret") {
		t.Fatalf("log line leaked a secret: %s", out)
	}
	if !strings.Contains(out, `"route":"chat_completions"`) {
		t.Fatalf("log line dropped a non-denied field: %s", out)
	}
	if !strings.Contains(out, `"ts":`) {
		t.Fatalf("log line missing the \"ts\" key: %s", out)
	}
}

func TestRedactionWithAttrsChain(t *testing.T) {
	for _, field := range []string{"authorization", "prompt", "messages", "content"} {
		var buf bytes.Buffer
		logger := NewLogger(&buf, slog.LevelInfo)
		logger.With(field, "super secret payload").Info("x")
		if strings.Contains(buf.String(), "super secret payload") {
			t.Fatalf("Logger.With(%q) leaked its value: %s", field, buf.String())
		}
	}
}

func TestRedactionInsideGroup(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	logger.Info("x", slog.Group("req", "authorization", "Bearer secretvalue"))
	if strings.Contains(buf.String(), "secretvalue") {
		t.Fatalf("a denied field inside a group leaked: %s", buf.String())
	}
}

func TestNonDeniedFieldsSurviveWithAttrsChain(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	logger.With("key_id", "abcd1234").Info("x")
	if !strings.Contains(buf.String(), `"key_id":"abcd1234"`) {
		t.Fatalf("a non-denied field via Logger.With was lost: %s", buf.String())
	}
}
