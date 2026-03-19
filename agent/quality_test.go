package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

// ----------------------------------------------------------------
// ExtractFingerprint tests
// ----------------------------------------------------------------

func TestExtractFingerprint_WithFilePaths(t *testing.T) {
	messages := []llm.ChatMessage{
		llm.NewUserMessage("Please read the file /workspace/xbot/agent/compress.go and fix the bug."),
		llm.NewAssistantMessage("I've read ./compress.go, let me also check ../context_manager.go."),
	}

	fp := ExtractFingerprint(messages)

	// Should contain file paths
	found := false
	for _, p := range fp.FilePaths {
		if strings.Contains(p, "compress.go") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find compress.go in FilePaths, got %v", fp.FilePaths)
	}
}

func TestExtractFingerprint_WithErrors(t *testing.T) {
	messages := []llm.ChatMessage{
		llm.NewAssistantMessage("I encountered an error: nil pointer dereference in handleCompress.\npanic: runtime error"),
	}

	fp := ExtractFingerprint(messages)
	if len(fp.Errors) == 0 {
		t.Error("expected to extract error messages, got none")
	}
}

func TestExtractFingerprint_WithDecisions(t *testing.T) {
	messages := []llm.ChatMessage{
		llm.NewAssistantMessage("I decided to use a singleton pattern for the context manager.\nWe will use Redis for caching."),
	}

	fp := ExtractFingerprint(messages)
	if len(fp.Decisions) == 0 {
		t.Error("expected to extract decisions, got none")
	}

	// Should find at least one decision
	found := false
	for _, d := range fp.Decisions {
		if strings.Contains(strings.ToLower(d), "singleton") || strings.Contains(strings.ToLower(d), "redis") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find decision about singleton or redis, got %v", fp.Decisions)
	}
}

func TestExtractFingerprint_WithIdentifiers(t *testing.T) {
	messages := []llm.ChatMessage{
		llm.NewUserMessage("Fix the handleCompress function and SmartCompressor interface."),
	}

	fp := ExtractFingerprint(messages)
	// Should find code identifiers (not common words)
	foundHandleCompress := false
	foundSmartCompressor := false
	for _, id := range fp.Identifiers {
		if id == "handleCompress" {
			foundHandleCompress = true
		}
		if id == "SmartCompressor" {
			foundSmartCompressor = true
		}
	}
	if !foundHandleCompress {
		t.Errorf("expected handleCompress in identifiers, got %v", fp.Identifiers)
	}
	if !foundSmartCompressor {
		t.Errorf("expected SmartCompressor in identifiers, got %v", fp.Identifiers)
	}
}

func TestExtractFingerprint_EmptyMessages(t *testing.T) {
	fp := ExtractFingerprint(nil)
	if len(fp.FilePaths) != 0 || len(fp.Identifiers) != 0 || len(fp.Errors) != 0 || len(fp.Decisions) != 0 {
		t.Errorf("expected empty fingerprint for nil messages, got %+v", fp)
	}
}

// ----------------------------------------------------------------
// ValidateCompression tests
// ----------------------------------------------------------------

func TestValidateCompression_FullRetention(t *testing.T) {
	original := []llm.ChatMessage{
		llm.NewUserMessage("Read /workspace/xbot/agent/compress.go for me."),
		llm.NewAssistantMessage("@file:/workspace/xbot/agent/compress.go has been reviewed."),
	}

	fp := ExtractFingerprint(original)
	rate, lost := ValidateCompression(original, original, fp)

	if rate != 1.0 {
		t.Errorf("expected retention rate 1.0 for identical messages, got %.2f", rate)
	}
	if len(lost) > 0 {
		t.Errorf("expected no lost items, got %v", lost)
	}
}

func TestValidateCompression_PartialLoss(t *testing.T) {
	original := []llm.ChatMessage{
		llm.NewUserMessage("Read /workspace/xbot/agent/compress.go and /workspace/xbot/agent/engine.go"),
	}
	compressed := []llm.ChatMessage{
		llm.NewUserMessage("Compressed: reviewed compress.go"),
	}

	fp := ExtractFingerprint(original)
	rate, lost := ValidateCompression(original, compressed, fp)

	if rate >= 1.0 {
		t.Errorf("expected retention rate < 1.0 when file path was lost, got %.2f", rate)
	}
	if len(lost) == 0 {
		t.Error("expected some lost items")
	}
}

func TestValidateCompression_EmptyFingerprint(t *testing.T) {
	// Use text that produces no file paths, errors, or decisions.
	// Note: identifiers may still be extracted from non-common words, so we
	// test the zero-key-info case by providing an empty fingerprint directly.
	original := []llm.ChatMessage{
		llm.NewUserMessage("ok"),
	}
	compressed := []llm.ChatMessage{
		llm.NewUserMessage("ok"),
	}

	fp := KeyInfoFingerprint{} // explicitly empty
	rate, lost := ValidateCompression(original, compressed, fp)

	if rate != 1.0 {
		t.Errorf("expected 1.0 retention for empty fingerprint, got %.2f", rate)
	}
	if len(lost) != 0 {
		t.Errorf("expected no lost items for empty fingerprint, got %v", lost)
	}
}

// ----------------------------------------------------------------
// EvaluateQuality tests
// ----------------------------------------------------------------

func TestEvaluateQuality_HighQuality(t *testing.T) {
	// High quality: good compression ratio + structured markers + key info retained
	fp := KeyInfoFingerprint{
		FilePaths:   []string{"/workspace/xbot/agent/compress.go"},
		Identifiers: []string{"handleCompress", "SmartCompressor"},
		Errors:      []string{"nil pointer"},
		Decisions:   []string{"use singleton pattern"},
	}
	compressed := `@file:/workspace/xbot/agent/compress.go @func:handleCompress @type:SmartCompressor @error:nil pointer @decision:use singleton pattern`

	score := EvaluateQuality(1000, 300, fp, compressed)
	if score < 0.5 {
		t.Errorf("expected high quality score (>=0.5), got %.3f", score)
	}
}

func TestEvaluateQuality_LowQuality(t *testing.T) {
	// Low quality: bad compression ratio, no markers, no key info
	fp := KeyInfoFingerprint{
		FilePaths:   []string{"/workspace/xbot/agent/compress.go"},
		Identifiers: []string{"handleCompress"},
		Errors:      []string{"nil pointer"},
		Decisions:   []string{"use singleton pattern"},
	}
	compressed := `The conversation was about some code changes.`

	score := EvaluateQuality(1000, 900, fp, compressed)
	if score >= 0.8 {
		t.Errorf("expected low quality score (<0.8) for poor compression, got %.3f", score)
	}
}

func TestEvaluateQuality_ZeroOriginalTokens(t *testing.T) {
	score := EvaluateQuality(0, 0, KeyInfoFingerprint{}, "")
	if score != 1.0 {
		t.Errorf("expected 1.0 for zero original tokens, got %.3f", score)
	}
}

// ----------------------------------------------------------------
// containsSemanticMatch tests
// ----------------------------------------------------------------

func TestContainsSemanticMatch_ExactSubstring(t *testing.T) {
	if !containsSemanticMatch("the file compress.go was read", "compress.go") {
		t.Error("expected match for exact substring")
	}
}

func TestContainsSemanticMatch_CaseInsensitive(t *testing.T) {
	if !containsSemanticMatch("The File Compress.Go Was Read", "compress.go") {
		t.Error("expected case-insensitive match")
	}
}

func TestContainsSemanticMatch_KeywordOverlap(t *testing.T) {
	// "nil pointer dereference" → split to words like ["nil", "pointer", "dereference"]
	// "pointer was nil in function" contains "nil" and "pointer" → 2/3 = 0.67 >= 0.6
	if !containsSemanticMatch("pointer was nil in function", "nil pointer dereference") {
		t.Error("expected match via keyword overlap")
	}
}

func TestContainsSemanticMatch_NoMatch(t *testing.T) {
	if containsSemanticMatch("hello world", "quantum physics") {
		t.Error("expected no match for unrelated text")
	}
}

func TestContainsSemanticMatch_EmptyInput(t *testing.T) {
	if containsSemanticMatch("", "target") {
		t.Error("expected no match for empty text")
	}
	if containsSemanticMatch("text", "") {
		t.Error("expected no match for empty target")
	}
}

// ----------------------------------------------------------------
// extractFilePaths tests
// ----------------------------------------------------------------

func TestExtractFilePaths_AbsolutePaths(t *testing.T) {
	text := "Read the files /workspace/xbot/agent/compress.go and /usr/local/bin/app"
	paths := extractFilePaths(text)

	found := false
	for _, p := range paths {
		if strings.Contains(p, "compress.go") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find compress.go, got %v", paths)
	}
}

func TestExtractFilePaths_RelativePaths(t *testing.T) {
	text := "Check ./relative/path.txt and ../parent/file.go"
	paths := extractFilePaths(text)

	if len(paths) < 2 {
		t.Errorf("expected at least 2 paths, got %v", paths)
	}
}

func TestExtractFilePaths_TildePaths(t *testing.T) {
	text := "Edit ~/config/settings.json"
	paths := extractFilePaths(text)

	found := false
	for _, p := range paths {
		if strings.Contains(p, "settings.json") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find settings.json, got %v", paths)
	}
}

func TestExtractFilePaths_NoPaths(t *testing.T) {
	text := "Hello, how are you today?"
	paths := extractFilePaths(text)
	if len(paths) > 0 {
		t.Errorf("expected no paths, got %v", paths)
	}
}

func TestExtractFilePaths_Deduplication(t *testing.T) {
	text := "/path/to/file.go and /path/to/file.go"
	paths := extractFilePaths(text)
	if len(paths) > 1 {
		t.Errorf("expected deduplicated paths, got %v", paths)
	}
}

// ----------------------------------------------------------------
// splitToWords tests
// ----------------------------------------------------------------

func TestSplitToWords_BasicSplitting(t *testing.T) {
	words := splitToWords("the quick brown fox jumps over lazy dog")
	// "the", "over" are stop words, should be removed
	for _, w := range words {
		if w == "the" || w == "over" {
			t.Errorf("stop word should be removed: %s", w)
		}
	}
	if len(words) == 0 {
		t.Error("expected non-empty result after stop word removal")
	}
}

func TestSplitToWords_CodeText(t *testing.T) {
	words := splitToWords("handleCompress encountered nil pointer error")
	// Stop words removed: "nil" is not a stop word, "error" is not
	found := false
	for _, w := range words {
		if strings.EqualFold(w, "handleCompress") || strings.EqualFold(w, "nil") || strings.EqualFold(w, "error") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find meaningful words, got %v", words)
	}
}

func TestSplitToWords_EmptyString(t *testing.T) {
	words := splitToWords("")
	if len(words) != 0 {
		t.Errorf("expected empty result for empty string, got %v", words)
	}
}

func TestSplitToWords_SingleCharFiltered(t *testing.T) {
	words := splitToWords("a b c d e")
	// Single letters and "a" (stop word) should all be removed
	for _, w := range words {
		if len(w) <= 1 {
			t.Errorf("single char should be filtered: %s", w)
		}
	}
}

// ----------------------------------------------------------------
// extractCodeIdentifiers tests
// ----------------------------------------------------------------

func TestExtractCodeIdentifiers_CamelCase(t *testing.T) {
	ids := extractCodeIdentifiers("Call handleCompress function and SmartCompressor interface")
	found := false
	for _, id := range ids {
		if id == "handleCompress" || id == "SmartCompressor" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected camelCase identifiers, got %v", ids)
	}
}

func TestExtractCodeIdentifiers_SnakeCase(t *testing.T) {
	ids := extractCodeIdentifiers("Use extract_dialogue_from_tail function")
	found := false
	for _, id := range ids {
		if strings.Contains(id, "extract") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected snake_case identifier, got %v", ids)
	}
}

func TestExtractCodeIdentifiers_CommonWordsFiltered(t *testing.T) {
	ids := extractCodeIdentifiers("This is a simple sentence about nothing")
	// All common words should be filtered; any non-common words with length > 3 might pass
	for _, id := range ids {
		if isCommonWord(id) {
			t.Errorf("common word should be filtered: %s", id)
		}
	}
}

// ----------------------------------------------------------------
// extractErrorMessages tests
// ----------------------------------------------------------------

func TestExtractErrorMessages_Found(t *testing.T) {
	text := "Build failed: undefined reference to `compressMessages`.\npanic: runtime error: nil pointer dereference"
	errs := extractErrorMessages(text)
	if len(errs) == 0 {
		t.Error("expected to extract error messages")
	}
}

func TestExtractErrorMessages_None(t *testing.T) {
	text := "The build succeeded and all tests passed."
	errs := extractErrorMessages(text)
	if len(errs) > 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

// ----------------------------------------------------------------
// extractDecisions tests
// ----------------------------------------------------------------

func TestExtractDecisions_MultipleFormats(t *testing.T) {
	text := "We decided to use Redis for caching.\n@decision: migrate to PostgreSQL"
	decisions := extractDecisions(text)
	if len(decisions) < 2 {
		t.Errorf("expected at least 2 decisions, got %v", decisions)
	}
}

func TestExtractDecisions_None(t *testing.T) {
	text := "The weather is nice today."
	decisions := extractDecisions(text)
	if len(decisions) > 0 {
		t.Errorf("expected no decisions, got %v", decisions)
	}
}

// ----------------------------------------------------------------
// countStructuredMarkers tests
// ----------------------------------------------------------------

func TestCountStructuredMarkers(t *testing.T) {
	text := "@file:compress.go @func:handleCompress @error:nil pointer @decision:use cache"
	count := countStructuredMarkers(text)
	if count != 4 {
		t.Errorf("expected 4 markers, got %d", count)
	}
}

func TestCountStructuredMarkers_None(t *testing.T) {
	text := "No markers here at all."
	count := countStructuredMarkers(text)
	if count != 0 {
		t.Errorf("expected 0 markers, got %d", count)
	}
}

// ----------------------------------------------------------------
// isErrorContext tests
// ----------------------------------------------------------------

func TestIsErrorContext_True(t *testing.T) {
	tests := []string{
		"error: cannot find module",
		"build failed: undefined reference",
		"panic: runtime error",
		"timeout waiting for response",
	}
	for _, text := range tests {
		if !isErrorContext(text) {
			t.Errorf("expected isErrorContext(%q) = true", text)
		}
	}
}

func TestIsErrorContext_False(t *testing.T) {
	tests := []string{
		"the build was successful",
		"all tests passed",
		"no issues found",
	}
	for _, text := range tests {
		if isErrorContext(text) {
			t.Errorf("expected isErrorContext(%q) = false", text)
		}
	}
}

// ----------------------------------------------------------------
// Integration: extractDialogueFromTail with offload
// ----------------------------------------------------------------

func TestExtractDialogueFromTail_OffloadMarker(t *testing.T) {
	offloadContent := "📂 [offload:summary of 50 messages about implementing context compression]"
	tail := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		makeAssistantWithToolCalls("reading", llm.ToolCall{ID: "1", Name: "Read", Arguments: "{}"}),
		llm.NewToolMessage("Read", "1", "{}", offloadContent),
		llm.NewAssistantMessage("done"),
	}

	result := extractDialogueFromTail(tail)

	// Offload content should be preserved intact (not truncated)
	var foundFull bool
	for _, msg := range result {
		if strings.Contains(msg.Content, offloadContent) {
			foundFull = true
			break
		}
	}
	if !foundFull {
		t.Errorf("offload content should be preserved intact, got results: %v", result)
	}
}

func TestExtractDialogueFromTail_RegularToolTruncated(t *testing.T) {
	longContent := strings.Repeat("x", 500)
	tail := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		makeAssistantWithToolCalls("reading", llm.ToolCall{ID: "1", Name: "Read", Arguments: "{}"}),
		llm.NewToolMessage("Read", "1", "{}", longContent),
		llm.NewAssistantMessage("done"),
	}

	result := extractDialogueFromTail(tail)

	// Long tool content should be truncated (not contain the full 500 chars)
	for _, msg := range result {
		if strings.Contains(msg.Content, longContent) {
			t.Error("long tool content should have been truncated")
			break
		}
	}
}
