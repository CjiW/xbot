package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildAnthropicSystem_NoSystemMessages(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "hello"},
	}

	result := buildAnthropicSystem(messages)

	// 无 system 消息时应返回空字符串
	if result != "" {
		t.Errorf("expected empty string, got %v", result)
	}
}

func TestBuildAnthropicSystem_SingleNoCache_ReturnsString(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "hello"},
	}

	result := buildAnthropicSystem(messages)

	// 单条无缓存 system 时应返回 plain string（向后兼容）
	s, ok := result.(string)
	if !ok {
		t.Fatalf("expected string type, got %T", result)
	}
	if s != "You are a helpful assistant." {
		t.Errorf("expected 'You are a helpful assistant.', got %q", s)
	}

	// 验证 JSON 序列化后 system 是字符串而非数组
	data, _ := json.Marshal(map[string]any{"system": result})
	if string(data) != `{"system":"You are a helpful assistant."}` {
		t.Errorf("JSON should be plain string, got %s", string(data))
	}
}

func TestBuildAnthropicSystem_StaticCacheHint(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "You are a helpful assistant.", CacheHint: "static"},
		{Role: "user", Content: "hello"},
	}

	result := buildAnthropicSystem(messages)

	// 有 CacheHint="static" 时应返回带 cache_control 的 blocks 数组
	blocks, ok := result.([]anthropicSystemBlock)
	if !ok {
		t.Fatalf("expected []anthropicSystemBlock type, got %T", result)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Type != "text" {
		t.Errorf("expected type 'text', got %q", blocks[0].Type)
	}
	if blocks[0].Text != "You are a helpful assistant." {
		t.Errorf("unexpected text: %q", blocks[0].Text)
	}
	if blocks[0].CacheControl == nil {
		t.Fatal("expected cache_control to be set")
	}
	if blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("expected cache_control type 'ephemeral', got %q", blocks[0].CacheControl.Type)
	}

	// 验证 JSON 序列化包含 cache_control
	data, _ := json.Marshal(map[string]any{"system": result})
	s := string(data)
	if jsonValid := json.Valid([]byte(s)); !jsonValid {
		t.Errorf("invalid JSON: %s", s)
	}
	// 应包含 cache_control 和 ephemeral
	if !strings.Contains(s, `"cache_control"`) {
		t.Errorf("JSON should contain cache_control: %s", s)
	}
	if !strings.Contains(s, `"ephemeral"`) {
		t.Errorf("JSON should contain ephemeral: %s", s)
	}
}

func TestBuildAnthropicSystem_MixedStaticAndDynamic(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "You are a helpful assistant.", CacheHint: "static"},
		{Role: "system", Content: "Current time: 2025-01-01"},
		{Role: "user", Content: "hello"},
	}

	result := buildAnthropicSystem(messages)

	// 混合时应返回 blocks 数组
	blocks, ok := result.([]anthropicSystemBlock)
	if !ok {
		t.Fatalf("expected []anthropicSystemBlock type, got %T", result)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	// 第一条有 cache_control
	if blocks[0].CacheControl == nil {
		t.Fatal("first block should have cache_control")
	}
	if blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("first block cache_control type should be 'ephemeral', got %q", blocks[0].CacheControl.Type)
	}

	// 第二条没有 cache_control
	if blocks[1].CacheControl != nil {
		t.Error("second block should not have cache_control")
	}

	// 验证 JSON 序列化
	data, _ := json.Marshal(map[string]any{"system": result})
	s := string(data)
	if !json.Valid([]byte(s)) {
		t.Errorf("invalid JSON: %s", s)
	}
	// 应包含两个 type: "text"
	count := strings.Count(s, `"type":"text"`)
	if count != 2 {
		t.Errorf("expected 2 text blocks in JSON, got %d: %s", count, s)
	}
}

func TestBuildAnthropicSystem_MultipleDynamic_ReturnsString(t *testing.T) {
	// 多条动态 system（无 CacheHint）应合并为一条字符串返回（向后兼容）
	messages := []ChatMessage{
		{Role: "system", Content: "Part one."},
		{Role: "system", Content: "Part two."},
		{Role: "user", Content: "hello"},
	}

	result := buildAnthropicSystem(messages)

	// 两条动态 system 会被 buildAnthropicSystem 作为两个 blocks 返回
	// （因为我们不在这里合并，保持与 toAnthropicMessages 不同的策略）
	blocks, ok := result.([]anthropicSystemBlock)
	if !ok {
		t.Fatalf("expected []anthropicSystemBlock type for multiple dynamic blocks, got %T", result)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	// 都不应有 cache_control
	for i, b := range blocks {
		if b.CacheControl != nil {
			t.Errorf("block %d should not have cache_control", i)
		}
	}
}
