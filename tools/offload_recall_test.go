package tools

import (
	"fmt"
	"strings"
	"testing"
)

// mockOffloadStore implements OffloadRecallStore for testing.
type mockOffloadStore struct {
	data map[string]map[string]string // sessionKey -> id -> content
}

func newMockOffloadStore() *mockOffloadStore {
	return &mockOffloadStore{
		data: make(map[string]map[string]string),
	}
}

func (m *mockOffloadStore) Recall(sessionKey, id string) (string, error) {
	if sessions, ok := m.data[sessionKey]; ok {
		if content, ok := sessions[id]; ok {
			return content, nil
		}
	}
	return "", fmt.Errorf("offload ID %s not found in session %s", id, sessionKey)
}

func (m *mockOffloadStore) store(sessionKey, id, content string) {
	if m.data[sessionKey] == nil {
		m.data[sessionKey] = make(map[string]string)
	}
	m.data[sessionKey][id] = content
}

func TestOffloadRecallTool_Name(t *testing.T) {
	tool := &OffloadRecallTool{}
	if tool.Name() != "offload_recall" {
		t.Errorf("expected name 'offload_recall', got %q", tool.Name())
	}
}

func TestOffloadRecallTool_Description(t *testing.T) {
	tool := &OffloadRecallTool{}
	desc := tool.Description()
	if desc == "" {
		t.Error("description should not be empty")
	}
	if !strings.Contains(desc, "offload") {
		t.Error("description should mention offload")
	}
}

func TestOffloadRecallTool_Parameters(t *testing.T) {
	tool := &OffloadRecallTool{}
	params := tool.Parameters()
	if len(params) != 1 {
		t.Fatalf("expected 1 parameter, got %d", len(params))
	}
	if params[0].Name != "id" {
		t.Errorf("expected param name 'id', got %q", params[0].Name)
	}
	if !params[0].Required {
		t.Error("id parameter should be required")
	}
}

func TestOffloadRecallTool_Execute(t *testing.T) {
	store := newMockOffloadStore()
	store.store("feishu:oc123", "ol_abc12345", "full content here: "+strings.Repeat("x", 100))

	tool := &OffloadRecallTool{Store: store}
	ctx := &ToolContext{
		Channel: "feishu",
		ChatID:  "oc123",
	}

	result, err := tool.Execute(ctx, `{"id":"ol_abc12345"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if !strings.Contains(result.Summary, "full content here") {
		t.Errorf("result should contain stored content, got: %s", result.Summary)
	}
}

func TestOffloadRecallTool_Execute_NilStore(t *testing.T) {
	tool := &OffloadRecallTool{Store: nil}
	ctx := &ToolContext{}

	_, err := tool.Execute(ctx, `{"id":"ol_abc12345"}`)
	if err == nil {
		t.Error("expected error when store is nil")
	}
}

func TestOffloadRecallTool_Execute_InvalidJSON(t *testing.T) {
	store := newMockOffloadStore()
	tool := &OffloadRecallTool{Store: store}
	ctx := &ToolContext{}

	_, err := tool.Execute(ctx, `not json`)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestOffloadRecallTool_Execute_MissingID(t *testing.T) {
	store := newMockOffloadStore()
	tool := &OffloadRecallTool{Store: store}
	ctx := &ToolContext{}

	_, err := tool.Execute(ctx, `{}`)
	if err == nil {
		t.Error("expected error when id is missing")
	}
}

func TestOffloadRecallTool_Execute_NotFound(t *testing.T) {
	store := newMockOffloadStore()
	tool := &OffloadRecallTool{Store: store}
	ctx := &ToolContext{
		Channel: "feishu",
		ChatID:  "oc123",
	}

	_, err := tool.Execute(ctx, `{"id":"ol_nonexistent"}`)
	if err == nil {
		t.Error("expected error for non-existent offload ID")
	}
}

func TestOffloadRecallTool_Execute_Truncation(t *testing.T) {
	store := newMockOffloadStore()
	// Content larger than 8000 chars
	largeContent := strings.Repeat("a", 10000)
	store.store("cli:direct", "ol_truncate", largeContent)

	tool := &OffloadRecallTool{Store: store}
	ctx := &ToolContext{
		Channel: "cli",
		ChatID:  "direct",
	}

	result, err := tool.Execute(ctx, `{"id":"ol_truncate"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(result.Summary) > 8100 { // 8000 + some overhead for truncation message
		t.Errorf("result should be truncated, got %d bytes", len(result.Summary))
	}
	if !strings.Contains(result.Summary, "truncated") {
		t.Error("result should indicate truncation")
	}
}

func TestOffloadRecallTool_Execute_SessionKeyFromContext(t *testing.T) {
	store := newMockOffloadStore()
	store.store("custom:12345", "ol_test1234", "content for custom session")

	tool := &OffloadRecallTool{Store: store}
	ctx := &ToolContext{
		Channel: "custom",
		ChatID:  "12345",
	}

	result, err := tool.Execute(ctx, `{"id":"ol_test1234"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result.Summary != "content for custom session" {
		t.Errorf("unexpected result: %s", result.Summary)
	}
}
