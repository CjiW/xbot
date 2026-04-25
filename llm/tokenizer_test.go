package llm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

// mockToolDefinition is a test helper implementing ToolDefinition for use in tests.
type mockToolDefinition struct {
	name        string
	description string
	parameters  []ToolParam
}

func (m mockToolDefinition) Name() string            { return m.name }
func (m mockToolDefinition) Description() string     { return m.description }
func (m mockToolDefinition) Parameters() []ToolParam { return m.parameters }

// ---------------------------------------------------------------------------
// TestGetEncodingForModel
// ---------------------------------------------------------------------------

func TestGetEncodingForModel(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		wantEncoding tokenizer.Model
	}{
		// --- Claude models ---
		{"claude-3-opus maps to GPT4", "claude-3-opus", tokenizer.GPT4},
		{"claude-3-sonnet maps to GPT4", "claude-3-sonnet", tokenizer.GPT4},
		{"claude-3-haiku maps to GPT4", "claude-3-haiku", tokenizer.GPT4},
		{"claude-3-5-sonnet maps to GPT4", "claude-3-5-sonnet", tokenizer.GPT4},
		{"claude-3-5-sonnet-20241022 maps to GPT4", "claude-3-5-sonnet-20241022", tokenizer.GPT4},
		{"claude-3-5-haiku maps to GPT4", "claude-3-5-haiku", tokenizer.GPT4},
		{"claude-2 maps to GPT4", "claude-2", tokenizer.GPT4},
		{"claude-sonnet-4-20250514 maps to GPT4", "claude-sonnet-4-20250514", tokenizer.GPT4},
		{"claude-opus-4-20250115 maps to GPT4", "claude-opus-4-20250115", tokenizer.GPT4},

		// --- GPT-4 series ---
		{"gpt-4 maps to GPT4", "gpt-4", tokenizer.GPT4},
		{"gpt-4-turbo maps to GPT4", "gpt-4-turbo", tokenizer.GPT4},
		{"gpt-4o maps to GPT4o", "gpt-4o", tokenizer.GPT4o},
		{"gpt-4o-mini maps to GPT4o", "gpt-4o-mini", tokenizer.GPT4o},

		// --- GPT-3.5 series ---
		{"gpt-3.5-turbo maps to GPT35Turbo", "gpt-3.5-turbo", tokenizer.GPT35Turbo},

		// --- Prefix matching ---
		{"gpt-4o-2024-11-20 prefix-matches gpt-4o", "gpt-4o-2024-11-20", tokenizer.GPT4o},
		{"gpt-4-0123-preview prefix-matches gpt-4", "gpt-4-0123-preview", tokenizer.GPT4},
		{"gpt-3.5-turbo-16k prefix-matches gpt-3.5-turbo", "gpt-3.5-turbo-16k", tokenizer.GPT35Turbo},

		// --- Case insensitivity ---
		{"GPT-4O uppercased still matches", "GPT-4O", tokenizer.GPT4o},
		{"Claude-3-Opus mixed case matches", "Claude-3-Opus", tokenizer.GPT4},

		// --- Unknown model → default (GPT4) ---
		{"unknown model returns default GPT4", "some-unknown-model-xyz", tokenizer.GPT4},

		// --- Empty string → default (GPT4) ---
		{"empty string returns default GPT4", "", tokenizer.GPT4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getEncodingForModel(tt.model)
			if got != tt.wantEncoding {
				t.Errorf("getEncodingForModel(%q) = %v, want %v", tt.model, got, tt.wantEncoding)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestSerializeToolsToJSON
// ---------------------------------------------------------------------------

func TestSerializeToolsToJSON(t *testing.T) {
	t.Run("empty slice returns empty JSON array", func(t *testing.T) {
		result, err := serializeToolsToJSON([]ToolDefinition{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "[]" {
			t.Errorf("got %q, want %q", result, "[]")
		}
	})

	t.Run("nil slice returns empty JSON array", func(t *testing.T) {
		result, err := serializeToolsToJSON(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "[]" {
			t.Errorf("got %q, want %q", result, "[]")
		}
	})

	t.Run("single tool with name and description", func(t *testing.T) {
		tool := mockToolDefinition{
			name:        "get_weather",
			description: "Get the current weather for a location",
			parameters: []ToolParam{
				{Name: "city", Type: "string", Description: "City name", Required: true},
			},
		}

		result, err := serializeToolsToJSON([]ToolDefinition{tool})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify the result is valid JSON
		var parsed []map[string]any
		if err := json.Unmarshal([]byte(result), &parsed); err != nil {
			t.Fatalf("result is not valid JSON: %v\nGot: %s", err, result)
		}

		if len(parsed) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(parsed))
		}

		// Verify name and description are present
		fn := parsed[0]["function"].(map[string]any)
		if fn["name"] != "get_weather" {
			t.Errorf("expected name %q, got %q", "get_weather", fn["name"])
		}
		if !strings.Contains(fn["description"].(string), "current weather") {
			t.Errorf("description should contain 'current weather', got %q", fn["description"])
		}
	})

	t.Run("multiple tools produce valid JSON array", func(t *testing.T) {
		tools := []ToolDefinition{
			mockToolDefinition{name: "tool_a", description: "Tool A", parameters: nil},
			mockToolDefinition{name: "tool_b", description: "Tool B", parameters: nil},
		}

		result, err := serializeToolsToJSON(tools)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var parsed []map[string]any
		if err := json.Unmarshal([]byte(result), &parsed); err != nil {
			t.Fatalf("result is not valid JSON: %v\nGot: %s", err, result)
		}
		if len(parsed) != 2 {
			t.Errorf("expected 2 tools, got %d", len(parsed))
		}
	})

	t.Run("required parameters appear in JSON", func(t *testing.T) {
		tool := mockToolDefinition{
			name:        "search",
			description: "Search for items",
			parameters: []ToolParam{
				{Name: "query", Type: "string", Description: "Search query", Required: true},
				{Name: "limit", Type: "integer", Description: "Max results", Required: false},
			},
		}

		result, err := serializeToolsToJSON([]ToolDefinition{tool})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var parsed []map[string]any
		if err := json.Unmarshal([]byte(result), &parsed); err != nil {
			t.Fatalf("result is not valid JSON: %v\nGot: %s", err, result)
		}

		fn := parsed[0]["function"].(map[string]any)
		params := fn["parameters"].(map[string]any)
		requiredRaw := params["required"]
		if requiredRaw == nil {
			t.Fatal("expected 'required' field in parameters")
		}
		required := requiredRaw.([]any)
		if len(required) != 1 || required[0] != "query" {
			t.Errorf("expected required = [\"query\"], got %v", required)
		}
	})
}

// ---------------------------------------------------------------------------
// TestEstimateToolsTokens
// ---------------------------------------------------------------------------

func TestEstimateToolsTokens(t *testing.T) {
	t.Run("empty slice returns 0", func(t *testing.T) {
		got := estimateToolsTokens([]ToolDefinition{})
		if got != 0 {
			t.Errorf("estimateToolsTokens([]) = %d, want 0", got)
		}
	})

	t.Run("nil slice returns 0", func(t *testing.T) {
		got := estimateToolsTokens(nil)
		if got != 0 {
			t.Errorf("estimateToolsTokens(nil) = %d, want 0", got)
		}
	})

	t.Run("single tool returns positive estimate", func(t *testing.T) {
		tool := mockToolDefinition{
			name:        "read_file",
			description: "Read the contents of a file from disk",
			parameters: []ToolParam{
				{Name: "path", Type: "string", Description: "File path", Required: true},
			},
		}

		got := estimateToolsTokens([]ToolDefinition{tool})
		if got <= 0 {
			t.Errorf("estimateToolsTokens with one tool = %d, want > 0", got)
		}
	})

	t.Run("more tools yield higher estimate", func(t *testing.T) {
		singleTool := []ToolDefinition{
			mockToolDefinition{name: "a", description: "desc a", parameters: nil},
		}
		doubleTools := []ToolDefinition{
			mockToolDefinition{name: "a", description: "desc a", parameters: nil},
			mockToolDefinition{name: "b", description: "desc b", parameters: nil},
		}

		single := estimateToolsTokens(singleTool)
		double := estimateToolsTokens(doubleTools)
		if double <= single {
			t.Errorf("two tools (%d) should estimate more tokens than one (%d)", double, single)
		}
	})
}
