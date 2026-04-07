package event

import (
	"strings"
	"testing"
	"time"
)

func TestRenderMessage_BasicTemplate(t *testing.T) {
	evt := Event{
		Type:      "webhook",
		Payload:   map[string]any{"action": "opened", "number": float64(42)},
		Headers:   map[string]string{"x-github-event": "pull_request"},
		Timestamp: time.Date(2026, 4, 7, 12, 0, 0, 0, time.UTC),
	}

	result := RenderMessage("PR {{.Payload.action}} #{{.Payload.number}}", evt)
	if result != "PR opened #42" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestRenderMessage_EventType(t *testing.T) {
	evt := Event{Type: "github", Timestamp: time.Now()}
	result := RenderMessage("Event: {{.EventType}}", evt)
	if result != "Event: github" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestRenderMessage_EmptyTemplate(t *testing.T) {
	evt := Event{
		Type:      "webhook",
		Payload:   map[string]any{"key": "value"},
		Timestamp: time.Now(),
	}
	result := RenderMessage("", evt)
	if !strings.Contains(result, "[Event: webhook]") {
		t.Errorf("expected default format, got: %q", result)
	}
	if !strings.Contains(result, "key") {
		t.Errorf("expected payload in default, got: %q", result)
	}
}

func TestRenderMessage_InvalidTemplate(t *testing.T) {
	evt := Event{Type: "test", Payload: map[string]any{"a": "b"}, Timestamp: time.Now()}
	result := RenderMessage("{{.Invalid", evt)
	if !strings.Contains(result, "[Event: test]") {
		t.Errorf("expected fallback on bad template, got: %q", result)
	}
}

func TestRenderMessage_MissingKey(t *testing.T) {
	evt := Event{Type: "test", Payload: map[string]any{}, Timestamp: time.Now()}
	result := RenderMessage("val={{.Payload.nonexistent}}", evt)
	if result != "val=<no value>" {
		t.Errorf("expected zero value for missing key, got: %q", result)
	}
}

func TestRenderMessage_NestedPayload(t *testing.T) {
	evt := Event{
		Type: "webhook",
		Payload: map[string]any{
			"pull_request": map[string]any{
				"title": "Fix login bug",
			},
		},
		Timestamp: time.Now(),
	}
	result := RenderMessage("PR: {{dig .Payload \"pull_request\" \"title\"}}", evt)
	if result != "PR: Fix login bug" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestRenderMessage_EmptyPayload(t *testing.T) {
	evt := Event{Type: "test", Payload: nil, Timestamp: time.Now()}
	result := RenderMessage("", evt)
	if !strings.Contains(result, "(empty payload)") {
		t.Errorf("expected empty payload msg, got: %q", result)
	}
}

func TestSummarizePayload_Truncation(t *testing.T) {
	large := make(map[string]any)
	for i := 0; i < 100; i++ {
		large[strings.Repeat("k", 10)] = strings.Repeat("v", 10)
	}
	s := summarizePayload(large, 50)
	if len(s) > 54 { // 50 + "..."
		t.Errorf("expected truncated payload, got len=%d", len(s))
	}
}
