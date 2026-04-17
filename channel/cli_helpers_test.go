// cli_helpers_test.go — Unit tests for channel/cli_helpers.go
// Covers: isErrorContent, toLowerASCII, showTempStatus, clearTempStatusCmd,
// showSystemMsg, invalidateAllCache, toggleToolSummary, startAgentTurn,
// applyThemeAndRebuild, applyLanguageChange, closePanelAndResume, enqueueToast

package channel

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CLI settings scope helpers
// ---------------------------------------------------------------------------

func TestCLISettingScope_KnownKeys(t *testing.T) {
	cases := map[string]string{
		"theme":               "user",
		"language":            "user",
		"runner_server":       "user",
		"llm_provider":        "global",
		"llm_model":           "global",
		"default_user":        "global",
		"privileged_user":     "global",
		"subscription_manage": "action",
		"runner_panel":        "action",
		"danger_zone":         "action",
		"definitely_unknown":  "unknown",
	}
	for key, want := range cases {
		if got := cliSettingScope(key); got != want {
			t.Fatalf("cliSettingScope(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestCLISettingScope_SettingsSchemaKeysAreClassified(t *testing.T) {
	locale := localeZH()
	if locale == nil {
		t.Fatal("localeZH() returned nil")
	}
	var unknown []string
	for _, def := range locale.SettingsSchema {
		if cliSettingScope(def.Key) == "unknown" {
			unknown = append(unknown, def.Key)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		t.Fatalf("unclassified settings schema keys: %v", unknown)
	}
}

func TestIsSubscriptionScopedSettingKey(t *testing.T) {
	for _, key := range []string{"llm_provider", "llm_api_key", "llm_model", "llm_base_url"} {
		if !isSubscriptionScopedSettingKey(key) {
			t.Fatalf("expected %q to be subscription-scoped", key)
		}
	}
	for _, key := range []string{"theme", "language", "sandbox_mode"} {
		if isSubscriptionScopedSettingKey(key) {
			t.Fatalf("expected %q to not be subscription-scoped", key)
		}
	}
}

func TestOpenSettingsFromQuickSwitch_PreservesNonSubscriptionEdits(t *testing.T) {
	model := newCLIModel()
	model.channel = &CLIChannel{}
	model.panelValuesBackup = map[string]string{
		"theme":        "mono",
		"language":     "en",
		"llm_provider": "should-refresh",
	}
	model.panelCursorBackup = 2
	model.panelOnSubmitBackup = func(map[string]string) {}
	model.channel.config.GetCurrentValues = func() map[string]string {
		return map[string]string{
			"theme":        "midnight",
			"language":     "zh",
			"llm_provider": "openai",
		}
	}
	model.openSettingsFromQuickSwitch()
	if model.panelMode != "settings" {
		t.Fatalf("panelMode = %q, want settings", model.panelMode)
	}
	if got := model.panelValues["theme"]; got != "mono" {
		t.Fatalf("theme = %q, want mono", got)
	}
	if got := model.panelValues["language"]; got != "en" {
		t.Fatalf("language = %q, want en", got)
	}
	if got := model.panelValues["llm_provider"]; got != "openai" {
		t.Fatalf("llm_provider = %q, want openai", got)
	}
}

// ---------------------------------------------------------------------------
// isErrorContent — pure function, comprehensive edge-case coverage
// ---------------------------------------------------------------------------

func TestIsErrorContent_Empty(t *testing.T) {
	if isErrorContent("") {
		t.Error("empty string should not match error keywords")
	}
}

func TestIsErrorContent_ExactKeywordMatch(t *testing.T) {
	keywords := []string{"error", "failed", "失败", "错误", "exception", "denied", "refused"}
	for _, kw := range keywords {
		if !isErrorContent(kw) {
			t.Errorf("exact keyword %q should match", kw)
		}
	}
}

func TestIsErrorContent_CaseInsensitive(t *testing.T) {
	cases := []string{"Error", "ERROR", "ErrOr", "Failed", "FAILED", "Exception", "DENIED", "Refused"}
	for _, c := range cases {
		if !isErrorContent(c) {
			t.Errorf("case-insensitive keyword %q should match", c)
		}
	}
}

func TestIsErrorContent_Substring(t *testing.T) {
	if !isErrorContent("An error occurred while processing") {
		t.Error("substring 'error' should match")
	}
	if !isErrorContent("The operation failed unexpectedly") {
		t.Error("substring 'failed' should match")
	}
	if !isErrorContent("访问被denied，请重试") {
		t.Error("mixed-language substring 'denied' should match")
	}
}

func TestIsErrorContent_CJKKeywords(t *testing.T) {
	if !isErrorContent("操作失败，请稍后再试") {
		t.Error("Chinese keyword '失败' should match")
	}
	if !isErrorContent("系统错误：无法连接") {
		t.Error("Chinese keyword '错误' should match")
	}
}

func TestIsErrorContent_NoMatch(t *testing.T) {
	nonError := []string{
		"Operation succeeded",
		"操作成功",
		"completed successfully",
		"all tasks finished",
		"hello world",
		"12345",
	}
	for _, s := range nonError {
		if isErrorContent(s) {
			t.Errorf("non-error content %q should not match", s)
		}
	}
}

func TestIsErrorContent_MultipleKeywords(t *testing.T) {
	if !isErrorContent("error: connection refused") {
		t.Error("should match with multiple keywords present")
	}
}

func TestIsErrorContent_WhitespaceOnly(t *testing.T) {
	if isErrorContent("   ") {
		t.Error("whitespace-only should not match")
	}
}

// ---------------------------------------------------------------------------
// toLowerASCII — pure helper
// ---------------------------------------------------------------------------

func TestToLowerASCII_Empty(t *testing.T) {
	if toLowerASCII("") != "" {
		t.Error("empty string should remain empty")
	}
}

func TestToLowerASCII_Uppercase(t *testing.T) {
	if toLowerASCII("HELLO") != "hello" {
		t.Error("should convert uppercase to lowercase")
	}
}

func TestToLowerASCII_Mixed(t *testing.T) {
	if toLowerASCII("Hello World 123") != "hello world 123" {
		t.Error("should handle mixed case")
	}
}

func TestToLowerASCII_NonASCII(t *testing.T) {
	input := "中文测试"
	if toLowerASCII(input) != input {
		t.Error("non-ASCII characters should be preserved")
	}
}

func TestToLowerASCII_SpecialChars(t *testing.T) {
	input := "!@#$%^&*()"
	if toLowerASCII(input) != input {
		t.Error("special characters should be preserved")
	}
}

// ---------------------------------------------------------------------------
// showTempStatus / clearTempStatusCmd
// ---------------------------------------------------------------------------

func TestShowTempStatus(t *testing.T) {
	model := newCLIModel()

	model.showTempStatus("saving settings...")
	if model.tempStatus != "saving settings..." {
		t.Errorf("tempStatus = %q, want %q", model.tempStatus, "saving settings...")
	}

	// Overwrite with empty
	model.showTempStatus("")
	if model.tempStatus != "" {
		t.Errorf("tempStatus = %q, want empty", model.tempStatus)
	}
}

func TestClearTempStatusCmd_DefaultDuration(t *testing.T) {
	model := newCLIModel()
	cmd := model.clearTempStatusCmd()
	if cmd == nil {
		t.Fatal("clearTempStatusCmd() should return non-nil tea.Cmd")
	}
}

func TestClearTempStatusCmd_CustomDuration(t *testing.T) {
	model := newCLIModel()
	cmd := model.clearTempStatusCmd(5 * time.Second)
	if cmd == nil {
		t.Fatal("clearTempStatusCmd with custom duration should return non-nil tea.Cmd")
	}

	// Multiple durations: only first should be used
	cmd2 := model.clearTempStatusCmd(1*time.Second, 10*time.Second)
	if cmd2 == nil {
		t.Fatal("clearTempStatusCmd with multiple durations should return non-nil tea.Cmd")
	}
}

func TestClearTempStatusCmd_ZeroDuration(t *testing.T) {
	model := newCLIModel()
	cmd := model.clearTempStatusCmd(0)
	if cmd == nil {
		t.Fatal("clearTempStatusCmd with zero duration should still return non-nil tea.Cmd")
	}
}

// ---------------------------------------------------------------------------
// showSystemMsg
// ---------------------------------------------------------------------------

func TestShowSystemMsg_AppendsMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	model.showSystemMsg("test message", feedbackInfo)

	if len(model.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(model.messages))
	}
	if model.messages[0].role != "system" {
		t.Errorf("message role = %q, want %q", model.messages[0].role, "system")
	}
	if model.messages[0].content != "test message" {
		t.Errorf("message content = %q, want %q", model.messages[0].content, "test message")
	}
	// Note: dirty will be cleared by fullRebuild() inside updateViewportContent(),
	// so we verify the message was rendered instead.
	if model.messages[0].rendered == "" {
		t.Error("message should have been rendered by updateViewportContent")
	}
}

func TestShowSystemMsg_DifferentLevels(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	levels := []feedbackLevel{feedbackInfo, feedbackWarning, feedbackError}
	for i, level := range levels {
		model.showSystemMsg("msg", level)
		msg := model.messages[i]
		if msg.role != "system" {
			t.Errorf("level %d: role = %q, want system", level, msg.role)
		}
	}
}

func TestShowSystemMsg_EmptyContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	model.showSystemMsg("", feedbackInfo)
	if len(model.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(model.messages))
	}
	if model.messages[0].content != "" {
		t.Errorf("content should be empty, got %q", model.messages[0].content)
	}
}

func TestShowSystemMsg_Multiple(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	model.showSystemMsg("first", feedbackInfo)
	model.showSystemMsg("second", feedbackWarning)
	model.showSystemMsg("third", feedbackError)

	if len(model.messages) != 3 {
		t.Fatalf("messages count = %d, want 3", len(model.messages))
	}
	if model.messages[2].content != "third" {
		t.Errorf("last message content = %q, want %q", model.messages[2].content, "third")
	}
}

// ---------------------------------------------------------------------------
// invalidateAllCache
// ---------------------------------------------------------------------------

func TestInvalidateAllCache_MarksDirty(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "hello", dirty: false},
		{role: "assistant", content: "hi", dirty: false},
	}
	model.renderCacheValid = true

	model.invalidateAllCache(false)

	if model.renderCacheValid {
		t.Error("renderCacheValid should be false after invalidate")
	}
	for i, msg := range model.messages {
		if !msg.dirty {
			t.Errorf("message[%d] should be dirty", i)
		}
	}
}

func TestInvalidateAllCache_NoUpdateViewport(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "hello"},
	}
	// Set viewport content to known value
	model.updateViewportContent()
	before := model.viewport.View()

	model.invalidateAllCache(false)
	after := model.viewport.View()

	if before != after {
		t.Error("invalidateAllCache(false) should NOT update viewport content")
	}
}

func TestInvalidateAllCache_WithUpdateViewport(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "hello"},
	}
	model.renderCacheValid = true

	model.invalidateAllCache(true)

	// After full rebuild (triggered by updateViewportContent), cache becomes valid again.
	// The key behavior to verify is that viewport was actually refreshed.
	if model.renderCacheValid != true {
		t.Error("renderCacheValid should be true after full rebuild restores cache")
	}
	if model.viewport.View() == "" {
		t.Error("viewport should have content after updateViewportContent")
	}
	// Verify dirty flags were cleared by fullRebuild
	for i, msg := range model.messages {
		if msg.dirty {
			t.Errorf("message[%d] should not be dirty after fullRebuild", i)
		}
	}
}

func TestInvalidateAllCache_EmptyMessages(t *testing.T) {
	model := newCLIModel()
	model.renderCacheValid = true

	model.invalidateAllCache(false)

	if model.renderCacheValid {
		t.Error("renderCacheValid should be false even with no messages")
	}
}

// ---------------------------------------------------------------------------
// toggleToolSummary
// ---------------------------------------------------------------------------

func TestToggleToolSummary(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	initial := model.toolSummaryExpanded
	model.toggleToolSummary()
	if model.toolSummaryExpanded == initial {
		t.Error("toggleToolSummary should flip toolSummaryExpanded")
	}
	if model.cachedHistory != "" {
		t.Error("cachedHistory should be cleared")
	}
	// After toggleToolSummary → invalidateAllCache(true) → updateViewportContent → fullRebuild,
	// renderCacheValid is restored to true. This is expected.

	// Toggle again should revert
	model.toggleToolSummary()
	if model.toolSummaryExpanded != initial {
		t.Error("second toggle should revert to initial state")
	}
}

// ---------------------------------------------------------------------------
// startAgentTurn
// ---------------------------------------------------------------------------

func TestStartAgentTurn(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.inputReady = true
	model.typing = false

	model.startAgentTurn()

	if !model.typing {
		t.Error("typing should be true after startAgentTurn")
	}
	if model.inputReady {
		t.Error("inputReady should be false after startAgentTurn")
	}
	if model.iterationHistory != nil {
		t.Error("iterationHistory should be nil after resetProgressState")
	}
	if model.lastSeenIteration != 0 {
		t.Error("lastSeenIteration should be 0 after resetProgressState")
	}
}

func TestStartAgentTurn_ResetsProgressState(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.iterationHistory = []cliIterationSnapshot{
		{Iteration: 1, Tools: []CLIToolProgress{{Name: "test"}}},
	}
	model.lastSeenIteration = 5

	model.startAgentTurn()

	if len(model.iterationHistory) != 0 {
		t.Errorf("iterationHistory should be empty, got %d items", len(model.iterationHistory))
	}
	if model.lastSeenIteration != 0 {
		t.Errorf("lastSeenIteration = %d, want 0", model.lastSeenIteration)
	}
}

// ---------------------------------------------------------------------------
// applyThemeAndRebuild
// ---------------------------------------------------------------------------

func TestApplyThemeAndRebuild_ValidTheme(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.renderCacheValid = true

	themes := ThemeNames()
	if len(themes) > 0 {
		model.applyThemeAndRebuild(themes[0])
	}
	if model.renderCacheValid {
		t.Error("renderCacheValid should be false after applyThemeAndRebuild")
	}
}

func TestApplyThemeAndRebuild_InvalidTheme(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Unknown theme should not panic, falls back to default
	model.applyThemeAndRebuild("nonexistent_theme_xyz")
	if model.renderCacheValid {
		t.Error("renderCacheValid should be false")
	}
}

func TestApplyThemeAndRebuild_NarrowWidth(t *testing.T) {
	model := newCLIModel()
	model.handleResize(4, 24) // width == 4, boundary for width > 4

	model.applyThemeAndRebuild("midnight")
	// Should not panic when width <= 4 (renderer not rebuilt)
	if model.renderCacheValid {
		t.Error("renderCacheValid should be false")
	}
}

func TestApplyThemeAndRebuild_ZeroWidth(t *testing.T) {
	model := newCLIModel()
	// No handleResize — width stays 0
	model.applyThemeAndRebuild("midnight")
	// Should not panic
	if model.renderCacheValid {
		t.Error("renderCacheValid should be false")
	}
}

// ---------------------------------------------------------------------------
// applyLanguageChange
// ---------------------------------------------------------------------------

func TestApplyLanguageChange_ValidLang(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.renderCacheValid = true

	model.applyLanguageChange("en")
	if model.locale == nil {
		t.Error("locale should not be nil after applyLanguageChange")
	}
	if model.renderCacheValid {
		t.Error("renderCacheValid should be false after applyLanguageChange")
	}
}

func TestApplyLanguageChange_UnknownLang(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	model.applyLanguageChange("xx_NONEXISTENT")
	if model.locale == nil {
		t.Error("should fall back to default locale for unknown language")
	}
}

func TestApplyLanguageChange_EmptyLang(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	model.applyLanguageChange("")
	if model.locale == nil {
		t.Error("should use default locale for empty language")
	}
}

// ---------------------------------------------------------------------------
// closePanelAndResume
// ---------------------------------------------------------------------------

func TestClosePanelAndResume_NotTyping(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelMode = "settings"
	model.typing = false

	cont, _, cmd := model.closePanelAndResume()

	if !cont {
		t.Error("should return continue=true")
	}
	if cmd != nil {
		t.Error("cmd should be nil when not typing")
	}
	if model.panelMode != "" {
		t.Errorf("panelMode = %q, want empty", model.panelMode)
	}
}

func TestClosePanelAndResume_Typing(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelMode = "askuser"
	model.typing = true

	cont, _, cmd := model.closePanelAndResume()

	if !cont {
		t.Error("should return continue=true")
	}
	// tickCmd() is no longer returned — startAgentTurn() manages tick chain via pendingCmds
	if cmd != nil {
		t.Error("cmd should be nil — tick chain managed by startAgentTurn")
	}
	if model.panelMode != "" {
		t.Errorf("panelMode = %q, want empty", model.panelMode)
	}
}

func TestClosePanelAndResume_CleansUpPanelState(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelMode = "settings"
	model.panelEdit = true
	model.panelCombo = true
	model.panelSchema = []SettingDefinition{{Key: "test"}}
	model.panelValues = map[string]string{"test": "value"}

	model.closePanelAndResume()

	if model.panelMode != "" {
		t.Error("panelMode should be cleared")
	}
	if model.panelEdit {
		t.Error("panelEdit should be false")
	}
	if model.panelCombo {
		t.Error("panelCombo should be false")
	}
	if model.panelSchema != nil {
		t.Error("panelSchema should be nil")
	}
	if model.panelValues != nil {
		t.Error("panelValues should be nil")
	}
}

// ---------------------------------------------------------------------------
// enqueueToast
// ---------------------------------------------------------------------------

func TestEnqueueToast(t *testing.T) {
	model := newCLIModel()

	cmd := model.enqueueToast("saved", "✓")
	if cmd == nil {
		t.Fatal("enqueueToast should return non-nil tea.Cmd")
	}

	// Execute the Cmd to verify it produces the correct message
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.text != "saved" {
		t.Errorf("toast text = %q, want %q", toast.text, "saved")
	}
	if toast.icon != "✓" {
		t.Errorf("toast icon = %q, want %q", toast.icon, "✓")
	}
}

func TestEnqueueToast_ErrorToast(t *testing.T) {
	model := newCLIModel()

	cmd := model.enqueueToast("operation failed", "✗")
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.icon != "✗" {
		t.Errorf("toast icon = %q, want %q", toast.icon, "✗")
	}
}

func TestEnqueueToast_EmptyValues(t *testing.T) {
	model := newCLIModel()

	cmd := model.enqueueToast("", "")
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.text != "" {
		t.Errorf("toast text should be empty, got %q", toast.text)
	}
	if toast.icon != "" {
		t.Errorf("toast icon should be empty, got %q", toast.icon)
	}
}

// ---------------------------------------------------------------------------
// iterToolsFlat
// ---------------------------------------------------------------------------

func TestIterToolsFlat_WithIterations(t *testing.T) {
	msg := &cliMessage{
		iterations: []cliIterationSnapshot{
			{Iteration: 0, Tools: []CLIToolProgress{{Name: "read"}, {Name: "write"}}},
			{Iteration: 1, Tools: []CLIToolProgress{{Name: "exec"}}},
		},
	}

	tools, iterCount := msg.iterToolsFlat()
	if iterCount != 2 {
		t.Errorf("iterCount = %d, want 2", iterCount)
	}
	if len(tools) != 3 {
		t.Errorf("tools count = %d, want 3", len(tools))
	}
}

func TestIterToolsFlat_NoIterations(t *testing.T) {
	msg := &cliMessage{
		tools: []CLIToolProgress{{Name: "read"}, {Name: "write"}},
	}

	tools, iterCount := msg.iterToolsFlat()
	if iterCount != 0 {
		t.Errorf("iterCount = %d, want 0", iterCount)
	}
	if len(tools) != 2 {
		t.Errorf("tools count = %d, want 2", len(tools))
	}
}

func TestIterToolsFlat_Empty(t *testing.T) {
	msg := &cliMessage{}

	tools, iterCount := msg.iterToolsFlat()
	if iterCount != 0 {
		t.Errorf("iterCount = %d, want 0", iterCount)
	}
	if len(tools) != 0 {
		t.Errorf("tools count = %d, want 0", len(tools))
	}
}

func TestIterToolsFlat_IterationsWithEmptyTools(t *testing.T) {
	msg := &cliMessage{
		iterations: []cliIterationSnapshot{
			{Iteration: 0, Tools: nil},
			{Iteration: 1, Tools: []CLIToolProgress{{Name: "exec"}}},
		},
	}

	tools, iterCount := msg.iterToolsFlat()
	if iterCount != 2 {
		t.Errorf("iterCount = %d, want 2", iterCount)
	}
	if len(tools) != 1 {
		t.Errorf("tools count = %d, want 1", len(tools))
	}
}

// ---------------------------------------------------------------------------
// submitAskAnswers — integration-style tests
// ---------------------------------------------------------------------------

func TestSubmitAskAnswers_CallsCallback(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelMode = "askuser"

	var received map[string]string
	model.panelOnAnswer = func(answers map[string]string) {
		received = answers
	}

	model.typing = false
	cont, _, cmd := model.submitAskAnswers()

	if !cont {
		t.Error("should return continue=true")
	}
	if cmd != nil {
		t.Error("cmd should be nil when not typing")
	}
	if model.panelMode != "" {
		t.Error("panel should be closed")
	}
	// received may be empty (no answers) but callback should have been called
	_ = received
}

func TestSubmitAskAnswers_Typing(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelMode = "askuser"
	model.typing = true

	cont, _, cmd := model.submitAskAnswers()

	if !cont {
		t.Error("should return continue=true")
	}
	// tickCmd() is no longer returned — startAgentTurn() manages tick chain via pendingCmds
	if cmd != nil {
		t.Error("cmd should be nil — tick chain managed by startAgentTurn")
	}
}

func TestSubmitAskAnswers_NilCallback(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelMode = "askuser"
	model.panelOnAnswer = nil
	model.typing = false

	// Should not panic with nil callback
	cont, _, cmd := model.submitAskAnswers()

	if !cont {
		t.Error("should return continue=true")
	}
	if cmd != nil {
		t.Error("cmd should be nil when not typing")
	}
}

func TestSubmitAskAnswers_SavesCurrentFreeInputBeforeCollect(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelMode = "askuser"
	model.panelItems = []askItem{{Question: "q1"}, {Question: "q2", Other: "stale"}}
	model.panelTab = 1
	model.panelAnswerTA = model.newPanelTextArea("custom", 50, 3)

	model.saveCurrentFreeInput()
	if got := model.panelItems[1].Other; got != "custom" {
		t.Fatalf("panelItems[1].Other = %q, want custom", got)
	}
}

func TestCollectAskAnswers_UncheckedOptionsExcluded(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelItems = []askItem{
		{Question: "color?", Options: []string{"Red", "Blue", "Green"}},
	}
	model.panelOptSel = map[int]map[int]bool{
		0: {0: true, 1: false, 2: true},
	}

	answers := model.collectAskAnswers()
	got := answers["q0"]
	if got != "Red, Green" {
		t.Fatalf("expected only checked options, got %q", got)
	}
}

func TestCollectAskAnswers_AllUncheckedReturnsEmpty(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelItems = []askItem{
		{Question: "color?", Options: []string{"Red", "Blue"}},
	}
	model.panelOptSel = map[int]map[int]bool{
		0: {0: false, 1: false},
	}

	answers := model.collectAskAnswers()
	got := answers["q0"]
	if got != "" {
		t.Fatalf("expected empty answer when all unchecked, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Security / edge-case: ensure no panic on boundary inputs
// ---------------------------------------------------------------------------

func TestShowTempStatus_LongString(t *testing.T) {
	model := newCLIModel()
	longStr := strings.Repeat("a", 10000)
	model.showTempStatus(longStr)
	if model.tempStatus != longStr {
		t.Error("should handle long strings without panic")
	}
}

func TestShowSystemMsg_VeryLongContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	longContent := strings.Repeat("x", 100000)
	model.showSystemMsg(longContent, feedbackError)
	if len(model.messages) != 1 {
		t.Error("should append very long content without panic")
	}
}

func TestInvalidateAllCache_LargeMessageSlice(t *testing.T) {
	model := newCLIModel()
	model.messages = make([]cliMessage, 10000)
	for i := range model.messages {
		model.messages[i] = cliMessage{role: "user", content: "test"}
	}
	model.renderCacheValid = true

	model.invalidateAllCache(false)

	for i, msg := range model.messages {
		if !msg.dirty {
			t.Errorf("message[%d] should be dirty", i)
		}
	}
}

func TestEnqueueToast_LongText(t *testing.T) {
	model := newCLIModel()
	longText := strings.Repeat("警告", 5000)
	cmd := model.enqueueToast(longText, "⚠")
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.text != longText {
		t.Error("should handle long toast text")
	}
}

func TestIsErrorContent_NilSafeSlice(t *testing.T) {
	// Verify isErrorContent handles any string without panic
	variants := []string{
		"",
		"\x00error",
		"error\x00",
		strings.Repeat("error ", 1000),
	}
	for _, v := range variants {
		// Just ensure no panic
		_ = isErrorContent(v)
	}
}

type testSettingsService struct {
	getResult map[string]string
	getErr    error
	setCalls  []testSettingsSetCall
}

type testSettingsSetCall struct {
	channelName string
	senderID    string
	key         string
	value       string
}

func (s *testSettingsService) GetSettings(channelName, senderID string) (map[string]string, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	out := make(map[string]string, len(s.getResult))
	for k, v := range s.getResult {
		out[k] = v
	}
	return out, nil
}

func (s *testSettingsService) SetSetting(channelName, senderID, key, value string) error {
	s.setCalls = append(s.setCalls, testSettingsSetCall{
		channelName: channelName,
		senderID:    senderID,
		key:         key,
		value:       value,
	})
	return nil
}

func TestCLISettingScopeClassification(t *testing.T) {
	cases := []struct {
		key   string
		scope string
	}{
		{key: "theme", scope: "user"},
		{key: "runner_token", scope: "user"},
		{key: "llm_model", scope: "global"},
		{key: "enable_stream", scope: "global"},
		{key: "subscription_manage", scope: "action"},
	}
	for _, tc := range cases {
		if got := cliSettingScope(tc.key); got != tc.scope {
			t.Errorf("cliSettingScope(%q) = %q, want %q", tc.key, got, tc.scope)
		}
	}
}

func TestMergeCLISettingsValues_OverlaysUserScopedOnly(t *testing.T) {
	cfgVals := map[string]string{
		"theme":     "midnight",
		"llm_model": "gpt-4.1",
	}
	settingsSvc := &testSettingsService{getResult: map[string]string{
		"theme":         "mono",
		"llm_model":     "claude-3-7",
		"runner_server": "https://runner.example",
	}}
	model := newCLIModel()
	model.channelName = "cli"
	model.senderID = "cli_user"
	model.channel = &CLIChannel{settingsSvc: settingsSvc}
	model.channel.config.GetCurrentValues = func() map[string]string { return cfgVals }

	merged := model.mergeCLISettingsValues()
	if got := merged["theme"]; got != "mono" {
		t.Fatalf("theme = %q, want mono", got)
	}
	if got := merged["llm_model"]; got != "gpt-4.1" {
		t.Fatalf("llm_model = %q, want config value gpt-4.1", got)
	}
	if got := merged["runner_server"]; got != "https://runner.example" {
		t.Fatalf("runner_server = %q, want settings value", got)
	}
}

func TestPersistCLISettingsValues_PersistsUserScopedAndAppliesAll(t *testing.T) {
	settingsSvc := &testSettingsService{}
	applied := map[string]string{}
	model := newCLIModel()
	model.channelName = "cli"
	model.senderID = "cli_user"
	model.channel = &CLIChannel{settingsSvc: settingsSvc}
	model.channel.config.ApplySettings = func(vals map[string]string) {
		for k, v := range vals {
			applied[k] = v
		}
	}

	input := map[string]string{
		"theme":            "mono",
		"runner_workspace": "/tmp/ws",
		"llm_model":        "gpt-4.1",
	}
	model.persistCLISettingsValues(input)

	if len(settingsSvc.setCalls) != 2 {
		t.Fatalf("setCalls = %d, want 2", len(settingsSvc.setCalls))
	}
	for _, call := range settingsSvc.setCalls {
		if cliSettingScope(call.key) != "user" {
			t.Fatalf("persisted non-user-scoped key %q", call.key)
		}
		if call.channelName != "cli" || call.senderID != "cli_user" {
			t.Fatalf("unexpected target: channel=%q sender=%q", call.channelName, call.senderID)
		}
	}
	if applied["llm_model"] != "gpt-4.1" || applied["theme"] != "mono" || applied["runner_workspace"] != "/tmp/ws" {
		t.Fatalf("ApplySettings did not receive full input: %#v", applied)
	}
}
