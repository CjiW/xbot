package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xbot/storage/vectordb"
)

// mockRecallTimeRangeFunc returns a testable RecallTimeRangeFunc
// that records the arguments and returns specified entries.
func mockRecallTimeRangeFunc(entries []vectordb.RecallEntry) vectordb.RecallTimeRangeFunc {
	return func(tenantID int64, query string, start, end time.Time, limit int) ([]vectordb.RecallEntry, error) {
		// Simple filtering for tests
		var results []vectordb.RecallEntry
		for _, e := range entries {
			matchQuery := query == "" || strings.Contains(e.Entry, query)
			matchStart := start.IsZero() || !e.CreatedAt.Before(start)
			matchEnd := end.IsZero() || !e.CreatedAt.After(end)
			if matchQuery && matchStart && matchEnd {
				results = append(results, e)
			}
			if len(results) >= limit && limit > 0 {
				break
			}
		}
		return results, nil
	}
}

func TestRecallMemorySearchTool_Name(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	if tool.Name() != "recall_memory_search" {
		t.Errorf("expected name 'recall_memory_search', got '%s'", tool.Name())
	}
}

func TestRecallMemorySearchTool_Parameters(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	params := tool.Parameters()

	names := make(map[string]bool)
	for _, p := range params {
		names[p.Name] = true
	}
	for _, expected := range []string{"query", "start_date", "end_date", "limit"} {
		if !names[expected] {
			t.Errorf("missing parameter: %s", expected)
		}
	}
}

func TestRecallMemorySearchTool_NoParams(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(nil),
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "At least one") {
		t.Errorf("expected validation error, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_NotAvailable(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: nil, // not available
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{Query: "Go"})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "not available") {
		t.Errorf("expected 'not available' message, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_QueryOnly(t *testing.T) {
	entries := []vectordb.RecallEntry{
		{Entry: "Discussed Go generics", CreatedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)},
		{Entry: "Go error handling patterns", CreatedAt: time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC)},
		{Entry: "Rust ownership model", CreatedAt: time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC)},
	}

	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(entries),
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{Query: "Go"})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "Go generics") {
		t.Errorf("expected 'Go generics' in results, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "Go error handling") {
		t.Errorf("expected 'Go error handling' in results, got: %s", result.Summary)
	}
	if strings.Contains(result.Summary, "Rust") {
		t.Errorf("should not contain 'Rust' in results, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_DateRange(t *testing.T) {
	entries := []vectordb.RecallEntry{
		{Entry: "Old message", CreatedAt: time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC)},
		{Entry: "Match message", CreatedAt: time.Date(2026, 3, 1, 14, 0, 0, 0, time.UTC)},
		{Entry: "Future message", CreatedAt: time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)},
	}

	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(entries),
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{
		StartDate: "2026-03-01",
		EndDate:   "2026-03-02",
	})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "Match message") {
		t.Errorf("expected 'Match message' in results, got: %s", result.Summary)
	}
	if strings.Contains(result.Summary, "Old message") || strings.Contains(result.Summary, "Future message") {
		t.Errorf("should not contain out-of-range messages, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_InvalidDateFormat(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(nil),
		TenantID:        1,
	}

	// Bad start_date
	input, _ := json.Marshal(recallSearchArgs{StartDate: "03-01-2026"})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "Invalid start_date") {
		t.Errorf("expected date format error, got: %s", result.Summary)
	}

	// Bad end_date
	input, _ = json.Marshal(recallSearchArgs{EndDate: "not-a-date"})
	result, err = tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "Invalid end_date") {
		t.Errorf("expected date format error, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_NoResults(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(nil),
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{Query: "nonexistent"})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "No conversation history") {
		t.Errorf("expected no results message, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_ResultFormatting(t *testing.T) {
	entries := []vectordb.RecallEntry{
		{Entry: "First entry", CreatedAt: time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC)},
		{Entry: "Second entry", CreatedAt: time.Date(2026, 3, 2, 15, 45, 0, 0, time.UTC)},
	}

	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(entries),
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{Query: "entry"})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Check formatting: header + numbered entries with timestamps
	if !strings.Contains(result.Summary, "## Recall Memory Results (2 entries)") {
		t.Errorf("expected header with count, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "1. [2026-03-01 10:30]") {
		t.Errorf("expected formatted timestamp, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "2. [2026-03-02 15:45]") {
		t.Errorf("expected second entry timestamp, got: %s", result.Summary)
	}
}

func TestLettaMemoryTools_IncludesRecall(t *testing.T) {
	tools := LettaMemoryTools()
	found := false
	for _, tool := range tools {
		if tool.Name() == "recall_memory_search" {
			found = true
			break
		}
	}
	if !found {
		t.Error("LettaMemoryTools should include recall_memory_search")
	}
	// Should have 6 tools total
	if len(tools) != 6 {
		t.Errorf("expected 6 Letta tools, got %d", len(tools))
	}
}
