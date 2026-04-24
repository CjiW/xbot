package llm

import (
	"encoding/json"
	"testing"
)

func TestToOpenAIMessages_ReasoningContentPassedBack(t *testing.T) {
	messages := []ChatMessage{
		NewUserMessage("hello"),
		{
			Role:             "assistant",
			Content:          "I need to check the weather.",
			ReasoningContent: "The user is asking about weather, I should call the weather tool.",
			ToolCalls: []ToolCall{{
				ID:   "call_001",
				Name: "get_weather",
			}},
		},
		{
			Role:       "tool",
			Content:    "Sunny, 25°C",
			ToolCallID: "call_001",
		},
		{
			Role:             "assistant",
			Content:          "The weather is sunny and 25°C.",
			ReasoningContent: "Got the weather result, let me share it.",
		},
		NewUserMessage("thanks"),
	}

	result := toOpenAIMessages(messages)

	// Verify we have 5 messages
	if len(result) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(result))
	}

	// Verify first assistant message (index 1) has reasoning_content and tool_calls
	assistant1 := result[1].OfAssistant
	if assistant1 == nil {
		t.Fatal("expected assistant message at index 1")
	}
	// Serialize to JSON and check for reasoning_content
	jsonBytes, err := json.Marshal(assistant1)
	if err != nil {
		t.Fatalf("failed to marshal assistant1: %v", err)
	}
	jsonStr := string(jsonBytes)
	if jsonStr == "" {
		t.Fatal("empty JSON for assistant1")
	}
	t.Logf("Assistant 1 JSON: %s", jsonStr)

	var parsed1 map[string]any
	if err := json.Unmarshal(jsonBytes, &parsed1); err != nil {
		t.Fatalf("failed to parse assistant1 JSON: %v", err)
	}
	if _, ok := parsed1["reasoning_content"]; !ok {
		t.Error("assistant message with tool_calls is missing reasoning_content")
	}
	if _, ok := parsed1["tool_calls"]; !ok {
		t.Error("assistant message is missing tool_calls")
	}

	// Verify second assistant message (index 3) has reasoning_content but no tool_calls
	assistant2 := result[3].OfAssistant
	if assistant2 == nil {
		t.Fatal("expected assistant message at index 3")
	}
	jsonBytes2, err := json.Marshal(assistant2)
	if err != nil {
		t.Fatalf("failed to marshal assistant2: %v", err)
	}
	t.Logf("Assistant 2 JSON: %s", string(jsonBytes2))

	var parsed2 map[string]any
	if err := json.Unmarshal(jsonBytes2, &parsed2); err != nil {
		t.Fatalf("failed to parse assistant2 JSON: %v", err)
	}
	if _, ok := parsed2["reasoning_content"]; !ok {
		t.Error("final assistant message is missing reasoning_content")
	}
	if _, ok := parsed2["tool_calls"]; ok {
		t.Error("final assistant message should not have tool_calls")
	}
}

func TestToOpenAIMessages_AssistantWithoutReasoningContent(t *testing.T) {
	// Non-thinking mode: assistant message without reasoning_content
	messages := []ChatMessage{
		NewUserMessage("hello"),
		{
			Role:    "assistant",
			Content: "Hi there!",
		},
	}

	result := toOpenAIMessages(messages)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	// Should NOT use Override (no reasoning_content)
	assistant := result[1].OfAssistant
	if assistant == nil {
		t.Fatal("expected assistant message")
	}
	jsonBytes, _ := json.Marshal(assistant)
	t.Logf("Non-thinking assistant JSON: %s", string(jsonBytes))

	var parsed map[string]any
	json.Unmarshal(jsonBytes, &parsed)
	// reasoning_content should NOT be present
	if _, ok := parsed["reasoning_content"]; ok {
		t.Error("non-thinking assistant message should not have reasoning_content")
	}
}
