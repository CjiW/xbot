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

	result := toOpenAIMessages(messages, "")

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
	toolCalls1, ok := parsed1["tool_calls"].([]any)
	if !ok || len(toolCalls1) == 0 {
		t.Fatal("assistant message should contain tool_calls array")
	}
	firstToolCall := toolCalls1[0].(map[string]any)
	firstFunction := firstToolCall["function"].(map[string]any)
	args1, ok := firstFunction["arguments"].(string)
	if !ok {
		t.Fatalf("assistant tool call arguments should be a JSON string, got %T", firstFunction["arguments"])
	}
	if args1 != "{}" {
		t.Errorf("expected empty tool call arguments to be {}, got %q", args1)
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

	result := toOpenAIMessages(messages, "")
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

func TestToOpenAIMessages_ThinkingModeEmptyReasoning(t *testing.T) {
	// Thinking mode enabled but assistant has no reasoning_content (e.g. from compressed session)
	// DeepSeek requires reasoning_content field to be present even if empty
	messages := []ChatMessage{
		NewUserMessage("hello"),
		{
			Role:    "assistant",
			Content: "Hi there!",
		},
		NewUserMessage("what is 1+1?"),
		{
			Role:             "assistant",
			Content:          "The answer is 2.",
			ReasoningContent: "Simple addition.",
		},
	}

	result := toOpenAIMessages(messages, "enabled")
	if len(result) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result))
	}

	// First assistant (index 1) — no ReasoningContent but thinking enabled
	assistant1 := result[1].OfAssistant
	if assistant1 == nil {
		t.Fatal("expected assistant message at index 1")
	}
	jsonBytes1, _ := json.Marshal(assistant1)
	t.Logf("Thinking mode, empty reasoning JSON: %s", string(jsonBytes1))

	var parsed1 map[string]any
	json.Unmarshal(jsonBytes1, &parsed1)
	if rc, ok := parsed1["reasoning_content"]; !ok {
		t.Error("thinking mode: assistant message MUST have reasoning_content field")
	} else if rcStr, ok := rc.(string); !ok || rcStr != "" {
		t.Errorf("thinking mode: expected empty string for reasoning_content, got %v", rc)
	}

	// Second assistant (index 3) — has ReasoningContent
	assistant2 := result[3].OfAssistant
	if assistant2 == nil {
		t.Fatal("expected assistant message at index 3")
	}
	jsonBytes2, _ := json.Marshal(assistant2)
	t.Logf("Thinking mode, with reasoning JSON: %s", string(jsonBytes2))

	var parsed2 map[string]any
	json.Unmarshal(jsonBytes2, &parsed2)
	if rc, ok := parsed2["reasoning_content"]; !ok {
		t.Error("thinking mode: assistant message MUST have reasoning_content field")
	} else if rcStr, ok := rc.(string); !ok || rcStr != "Simple addition." {
		t.Errorf("thinking mode: expected 'Simple addition.' for reasoning_content, got %v", rc)
	}
}

func TestToOpenAIMessages_AutoModeWithReasoningHistoryKeepsEmptyReasoningField(t *testing.T) {
	// Auto mode should preserve reasoning_content shape once the conversation
	// already contains assistant reasoning. Some reasoning providers require
	// all assistant history messages to carry the field, even when empty.
	messages := []ChatMessage{
		NewUserMessage("hello"),
		{
			Role:    "assistant",
			Content: "Hi there!",
		},
		NewUserMessage("what is 1+1?"),
		{
			Role:             "assistant",
			Content:          "The answer is 2.",
			ReasoningContent: "Simple addition.",
		},
	}

	result := toOpenAIMessages(messages, "")
	if len(result) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result))
	}

	jsonBytes1, _ := json.Marshal(result[1].OfAssistant)
	var parsed1 map[string]any
	json.Unmarshal(jsonBytes1, &parsed1)
	if rc, ok := parsed1["reasoning_content"]; !ok {
		t.Error("auto mode with reasoning history: assistant message MUST have reasoning_content field")
	} else if rcStr, ok := rc.(string); !ok || rcStr != "" {
		t.Errorf("auto mode with reasoning history: expected empty string for reasoning_content, got %v", rc)
	}

	jsonBytes2, _ := json.Marshal(result[3].OfAssistant)
	var parsed2 map[string]any
	json.Unmarshal(jsonBytes2, &parsed2)
	if rc, ok := parsed2["reasoning_content"]; !ok {
		t.Error("auto mode with reasoning history: final assistant message MUST have reasoning_content field")
	} else if rcStr, ok := rc.(string); !ok || rcStr != "Simple addition." {
		t.Errorf("auto mode with reasoning history: expected preserved reasoning_content, got %v", rc)
	}
}

func TestToOpenAIMessages_ToolCallsArgumentsRemainJSONString(t *testing.T) {
	// When assistant has both reasoning_content and tool_calls,
	// DeepSeek/OpenAI-compatible APIs expect function.arguments to remain a JSON string.
	messages := []ChatMessage{
		NewUserMessage("read file"),
		{
			Role:             "assistant",
			Content:          "Let me read that file.",
			ReasoningContent: "User wants to read a file.",
			ToolCalls: []ToolCall{{
				ID:        "call_001",
				Name:      "read_file",
				Arguments: `{"path":"/tmp/test.go","max_lines":50}`,
			}},
		},
	}

	result := toOpenAIMessages(messages, "")
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	assistant := result[1].OfAssistant
	if assistant == nil {
		t.Fatal("expected assistant message")
	}
	jsonBytes, _ := json.Marshal(assistant)
	jsonStr := string(jsonBytes)
	t.Logf("Tool calls with reasoning JSON: %s", jsonStr)

	var parsed map[string]any
	json.Unmarshal(jsonBytes, &parsed)

	toolCalls, ok := parsed["tool_calls"].([]any)
	if !ok || len(toolCalls) == 0 {
		t.Fatal("missing tool_calls")
	}
	firstTC := toolCalls[0].(map[string]any)
	funcField := firstTC["function"].(map[string]any)
	args := funcField["arguments"]

	// Arguments should remain the raw JSON string, not be decoded into an object.
	argsStr, ok := args.(string)
	if !ok {
		t.Fatalf("arguments should be a JSON string, got %T: %#v", args, args)
	}
	if argsStr != `{"path":"/tmp/test.go","max_lines":50}` {
		t.Errorf("expected raw JSON string arguments, got %q", argsStr)
	}
}

func TestToOpenAIMessages_ThinkingDisabledNoReasoning(t *testing.T) {
	// thinkingMode="disabled" should behave same as "" — no reasoning_content field
	messages := []ChatMessage{
		NewUserMessage("hello"),
		{
			Role:    "assistant",
			Content: "Hi there!",
		},
	}

	result := toOpenAIMessages(messages, "disabled")
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	jsonBytes, _ := json.Marshal(result[1].OfAssistant)
	var parsed map[string]any
	json.Unmarshal(jsonBytes, &parsed)
	if _, ok := parsed["reasoning_content"]; ok {
		t.Error("disabled thinking mode should not include reasoning_content field")
	}
}
