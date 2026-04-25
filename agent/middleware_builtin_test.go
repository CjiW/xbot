package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xbot/memory"
)

// --- Mocks specific to this file ---

// mockSettingsReader implements SettingsReader for testing LanguageMiddleware.
type mockSettingsReader struct {
	vals map[string]string
	err  error
}

func (m *mockSettingsReader) GetSettings(_, _ string) (map[string]string, error) {
	return m.vals, m.err
}

// mockMemoryRecaller is a local mock for MemoryProvider used in additional
// MemoryMiddleware tests. The existing mockMemoryProvider in middleware_test.go
// covers basic cases; this allows extra scenarios.
type mockMemoryRecaller struct {
	recallResult string
	recallErr    error
}

func (m *mockMemoryRecaller) Recall(_ context.Context, _ string) (string, error) {
	return m.recallResult, m.recallErr
}

func (m *mockMemoryRecaller) Memorize(_ context.Context, _ memory.MemorizeInput) (memory.MemorizeResult, error) {
	return memory.MemorizeResult{}, nil
}

func (m *mockMemoryRecaller) Close() error { return nil }

// newMC creates a minimal MessageContext for testing.
func newMC() *MessageContext {
	return &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Extra:       make(map[string]any),
	}
}

// =====================================================================
// TestSenderInfoMiddleware_Extended
// Additional tests beyond the basic ones in middleware_test.go.
// =====================================================================

func TestSenderInfoMiddleware_Extended(t *testing.T) {
	t.Run("name_and_priority", func(t *testing.T) {
		m := NewSenderInfoMiddleware()
		if m.Name() != "sender_info" {
			t.Errorf("Name() = %q, want %q", m.Name(), "sender_info")
		}
		if m.Priority() != 130 {
			t.Errorf("Priority() = %d, want %d", m.Priority(), 130)
		}
	})

	t.Run("system_part_format", func(t *testing.T) {
		mc := newMC()
		mc.SenderName = "alice"
		m := NewSenderInfoMiddleware()
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got := mc.SystemParts["30_sender"]
		want := "\n## Current Sender\nName: alice\n"
		if got != want {
			t.Errorf("SystemParts[30_sender] = %q, want %q", got, want)
		}
	})
}

// =====================================================================
// TestLanguageMiddleware
// =====================================================================

func TestLanguageMiddleware(t *testing.T) {
	t.Run("nil_settings_svc_no_op", func(t *testing.T) {
		m := NewLanguageMiddleware(nil)
		mc := newMC()
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["32_language"]; ok {
			t.Error("expected no 32_language system part when settingsSvc is nil")
		}
	})

	t.Run("settings_returns_language_zh", func(t *testing.T) {
		m := NewLanguageMiddleware(&mockSettingsReader{
			vals: map[string]string{"language": "zh"},
		})
		mc := newMC()
		mc.Channel = "feishu"
		mc.SenderID = "user123"
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got, ok := mc.SystemParts["32_language"]
		if !ok {
			t.Fatal("expected 32_language system part to be set")
		}
		want := LanguageInstruction("zh")
		if got != want {
			t.Errorf("SystemParts[32_language] = %q, want %q", got, want)
		}
	})

	t.Run("settings_returns_language_en", func(t *testing.T) {
		m := NewLanguageMiddleware(&mockSettingsReader{
			vals: map[string]string{"language": "en"},
		})
		mc := newMC()
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got := mc.SystemParts["32_language"]
		want := LanguageInstruction("en")
		if got != want {
			t.Errorf("SystemParts[32_language] = %q, want %q", got, want)
		}
	})

	t.Run("settings_returns_language_ja", func(t *testing.T) {
		m := NewLanguageMiddleware(&mockSettingsReader{
			vals: map[string]string{"language": "ja"},
		})
		mc := newMC()
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got := mc.SystemParts["32_language"]
		want := LanguageInstruction("ja")
		if got != want {
			t.Errorf("SystemParts[32_language] = %q, want %q", got, want)
		}
	})

	t.Run("settings_returns_unknown_language", func(t *testing.T) {
		m := NewLanguageMiddleware(&mockSettingsReader{
			vals: map[string]string{"language": "fr"},
		})
		mc := newMC()
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got := mc.SystemParts["32_language"]
		want := LanguageInstruction("fr")
		if got != want {
			t.Errorf("SystemParts[32_language] = %q, want %q", got, want)
		}
	})

	t.Run("settings_returns_empty_language", func(t *testing.T) {
		m := NewLanguageMiddleware(&mockSettingsReader{
			vals: map[string]string{"language": ""},
		})
		mc := newMC()
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["32_language"]; ok {
			t.Error("expected no 32_language system part when language is empty")
		}
	})

	t.Run("settings_returns_no_language_key", func(t *testing.T) {
		m := NewLanguageMiddleware(&mockSettingsReader{
			vals: map[string]string{"theme": "dark"},
		})
		mc := newMC()
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["32_language"]; ok {
			t.Error("expected no 32_language system part when language key is missing")
		}
	})

	t.Run("settings_error_no_op", func(t *testing.T) {
		m := NewLanguageMiddleware(&mockSettingsReader{
			err: errors.New("db error"),
		})
		mc := newMC()
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["32_language"]; ok {
			t.Error("expected no 32_language system part when settings returns error")
		}
	})

	t.Run("name_and_priority", func(t *testing.T) {
		m := NewLanguageMiddleware(nil)
		if m.Name() != "language" {
			t.Errorf("Name() = %q, want %q", m.Name(), "language")
		}
		if m.Priority() != 135 {
			t.Errorf("Priority() = %d, want %d", m.Priority(), 135)
		}
	})
}

// =====================================================================
// TestLanguageInstruction
// =====================================================================

func TestLanguageInstruction(t *testing.T) {
	tests := []struct {
		lang string
		want string
	}{
		{"en", "## Language\n\nAlways respond in English."},
		{"zh", "## Language\n\n始终使用中文回复。"},
		{"ja", "## Language\n\n常に日本語で返答してください。"},
		{"fr", "## Language\n\nAlways respond in fr."},
		{"de", "## Language\n\nAlways respond in de."},
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			got := LanguageInstruction(tt.lang)
			if got != tt.want {
				t.Errorf("LanguageInstruction(%q) = %q, want %q", tt.lang, got, tt.want)
			}
		})
	}
}

// =====================================================================
// TestProjectContextMiddleware
// =====================================================================

func TestProjectContextMiddleware(t *testing.T) {
	t.Run("name_and_priority", func(t *testing.T) {
		m := NewProjectContextMiddleware()
		if m.Name() != "project_context" {
			t.Errorf("Name() = %q, want %q", m.Name(), "project_context")
		}
		if m.Priority() != 5 {
			t.Errorf("Priority() = %d, want %d", m.Priority(), 5)
		}
	})

	t.Run("empty_cwd_and_workdir_no_op", func(t *testing.T) {
		m := NewProjectContextMiddleware()
		mc := newMC()
		mc.CWD = ""
		mc.WorkDir = ""
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["05_project_context"]; ok {
			t.Error("expected no 05_project_context when CWD and WorkDir are empty")
		}
	})

	t.Run("cwd_with_context_file", func(t *testing.T) {
		dir := t.TempDir()
		content := "# Test Project\nThis is a test project."
		if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write AGENT.md: %v", err)
		}

		m := NewProjectContextMiddleware()
		mc := newMC()
		mc.CWD = dir
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got, ok := mc.SystemParts["05_project_context"]
		if !ok {
			t.Fatal("expected 05_project_context system part to be set")
		}
		if !strings.Contains(got, content) {
			t.Errorf("system part should contain %q, got %q", content, got)
		}
		if !strings.Contains(got, "Project Context") {
			t.Error("system part should contain 'Project Context' heading")
		}
	})

	t.Run("workdir_fallback_when_cwd_empty", func(t *testing.T) {
		dir := t.TempDir()
		content := "# Fallback Project"
		if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write AGENT.md: %v", err)
		}

		m := NewProjectContextMiddleware()
		mc := newMC()
		mc.CWD = ""
		mc.WorkDir = dir
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got, ok := mc.SystemParts["05_project_context"]
		if !ok {
			t.Fatal("expected 05_project_context system part to be set via WorkDir")
		}
		if !strings.Contains(got, content) {
			t.Errorf("system part should contain %q, got %q", content, got)
		}
	})

	t.Run("cwd_preferred_over_workdir", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		os.WriteFile(filepath.Join(dir1, "AGENT.md"), []byte("from CWD"), 0644)
		os.WriteFile(filepath.Join(dir2, "AGENT.md"), []byte("from WorkDir"), 0644)

		m := NewProjectContextMiddleware()
		mc := newMC()
		mc.CWD = dir1
		mc.WorkDir = dir2
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got := mc.SystemParts["05_project_context"]
		if !strings.Contains(got, "from CWD") {
			t.Errorf("expected CWD to take precedence, got %q", got)
		}
	})

	t.Run("directory_with_no_context_file", func(t *testing.T) {
		dir := t.TempDir()

		m := NewProjectContextMiddleware()
		mc := newMC()
		mc.CWD = dir
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["05_project_context"]; ok {
			t.Error("expected no system part when no context file exists")
		}
	})

	t.Run("priority_order_xbot_context_md_first", func(t *testing.T) {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, ".xbot"), 0755)
		os.WriteFile(filepath.Join(dir, ".xbot", "context.md"), []byte("xbot context"), 0644)
		os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("agent context"), 0644)

		m := NewProjectContextMiddleware()
		mc := newMC()
		mc.CWD = dir
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got := mc.SystemParts["05_project_context"]
		if !strings.Contains(got, "xbot context") {
			t.Errorf("expected .xbot/context.md to take priority, got %q", got)
		}
	})

	t.Run("empty_context_file_ignored", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("   \n  \n"), 0644)

		m := NewProjectContextMiddleware()
		mc := newMC()
		mc.CWD = dir
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["05_project_context"]; ok {
			t.Error("expected no system part when context file is whitespace-only")
		}
	})

	t.Run("cursorrules_loaded_third", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, ".cursorrules"), []byte("cursor rules content"), 0644)

		m := NewProjectContextMiddleware()
		mc := newMC()
		mc.CWD = dir
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got, ok := mc.SystemParts["05_project_context"]
		if !ok {
			t.Fatal("expected 05_project_context to be set from .cursorrules")
		}
		if !strings.Contains(got, "cursor rules content") {
			t.Errorf("should contain .cursorrules content, got %q", got)
		}
	})
}

// =====================================================================
// TestMemoryMiddleware_Extended
// Additional tests beyond the basic ones in middleware_test.go.
// =====================================================================

func TestMemoryMiddleware_Extended(t *testing.T) {
	t.Run("name_and_priority", func(t *testing.T) {
		m := NewMemoryMiddleware()
		if m.Name() != "memory" {
			t.Errorf("Name() = %q, want %q", m.Name(), "memory")
		}
		if m.Priority() != 120 {
			t.Errorf("Priority() = %d, want %d", m.Priority(), 120)
		}
	})

	t.Run("nil_memory_provider_typed_no_op", func(t *testing.T) {
		m := NewMemoryMiddleware()
		mc := newMC()
		mc.SetExtra(ExtraKeyMemoryProvider, nil)
		// GetExtraTyped does a type assertion, nil won't match memory.MemoryProvider
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["20_memory"]; ok {
			t.Error("expected no 20_memory system part when provider is nil")
		}
	})

	t.Run("recall_error_wrapped", func(t *testing.T) {
		m := NewMemoryMiddleware()
		mc := newMC()
		mem := &mockMemoryRecaller{
			recallErr: errors.New("recall failed"),
		}
		mc.SetExtra(ExtraKeyMemoryProvider, memory.MemoryProvider(mem))
		err := m.Process(mc)
		if err == nil {
			t.Fatal("expected error when Recall fails")
		}
		if !strings.Contains(err.Error(), "recall memory") {
			t.Errorf("error should wrap recall memory, got: %v", err)
		}
	})

	t.Run("memory_provider_with_content_exact_format", func(t *testing.T) {
		m := NewMemoryMiddleware()
		mc := newMC()
		mc.UserContent = "what did I say yesterday?"
		recallContent := "User previously asked about the database schema."
		mem := &mockMemoryRecaller{
			recallResult: recallContent,
		}
		mc.SetExtra(ExtraKeyMemoryProvider, memory.MemoryProvider(mem))
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got, ok := mc.SystemParts["20_memory"]
		if !ok {
			t.Fatal("expected 20_memory system part to be set")
		}
		want := "# Memory\n\n" + recallContent + "\n"
		if got != want {
			t.Errorf("SystemParts[20_memory] = %q, want %q", got, want)
		}
	})

	t.Run("nil_context_uses_todo", func(t *testing.T) {
		m := NewMemoryMiddleware()
		mc := newMC()
		mc.Ctx = nil
		mc.UserContent = "hello"
		mem := &mockMemoryRecaller{
			recallResult: "some memory",
		}
		mc.SetExtra(ExtraKeyMemoryProvider, memory.MemoryProvider(mem))
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["20_memory"]; !ok {
			t.Error("expected 20_memory to be set even with nil context")
		}
	})
}

// =====================================================================
// TestPermissionControlMiddleware
// =====================================================================

func TestPermissionControlMiddleware(t *testing.T) {
	t.Run("name_and_priority", func(t *testing.T) {
		m := NewPermissionControlMiddleware()
		if m.Name() != "permission_control" {
			t.Errorf("Name() = %q, want %q", m.Name(), "permission_control")
		}
		if m.Priority() != 115 {
			t.Errorf("Priority() = %d, want %d", m.Priority(), 115)
		}
	})

	t.Run("no_config_no_op", func(t *testing.T) {
		m := NewPermissionControlMiddleware()
		mc := newMC()
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["14_perm_control"]; ok {
			t.Error("expected no system part when no config in Extra")
		}
	})

	t.Run("disabled_config_no_op", func(t *testing.T) {
		m := NewPermissionControlMiddleware()
		mc := newMC()
		mc.SetExtra(ExtraKeyPermUsers, &PermUsersConfig{}) // both empty
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["14_perm_control"]; ok {
			t.Error("expected no system part when config has empty users")
		}
	})

	t.Run("default_user_only", func(t *testing.T) {
		m := NewPermissionControlMiddleware()
		mc := newMC()
		mc.SetExtra(ExtraKeyPermUsers, &PermUsersConfig{DefaultUser: "worker"})
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got, ok := mc.SystemParts["14_perm_control"]
		if !ok {
			t.Fatal("expected 14_perm_control system part to be set")
		}
		if !strings.Contains(got, "Execution User Control") {
			t.Error("system part should contain 'Execution User Control'")
		}
		if !strings.Contains(got, "worker") {
			t.Error("system part should contain 'worker'")
		}
		if strings.Contains(got, "**Yes**") {
			t.Error("should not contain privileged user approval row when only default user")
		}
	})

	t.Run("both_users", func(t *testing.T) {
		m := NewPermissionControlMiddleware()
		mc := newMC()
		mc.SetExtra(ExtraKeyPermUsers, &PermUsersConfig{
			DefaultUser:    "worker",
			PrivilegedUser: "admin",
		})
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got := mc.SystemParts["14_perm_control"]
		if !strings.Contains(got, "worker") {
			t.Error("system part should contain 'worker'")
		}
		if !strings.Contains(got, "admin") {
			t.Error("system part should contain 'admin'")
		}
		if !strings.Contains(got, "**Yes**") {
			t.Error("should contain privileged user approval row")
		}
	})

	t.Run("privileged_user_only", func(t *testing.T) {
		m := NewPermissionControlMiddleware()
		mc := newMC()
		mc.SetExtra(ExtraKeyPermUsers, &PermUsersConfig{
			PrivilegedUser: "root",
		})
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		got := mc.SystemParts["14_perm_control"]
		if !strings.Contains(got, "root") {
			t.Error("system part should contain 'root'")
		}
		if !strings.Contains(got, "**Yes**") {
			t.Error("should contain privileged user approval row")
		}
	})

	t.Run("wrong_type_ignored", func(t *testing.T) {
		m := NewPermissionControlMiddleware()
		mc := newMC()
		mc.SetExtra(ExtraKeyPermUsers, "not a config") // wrong type
		if err := m.Process(mc); err != nil {
			t.Fatalf("Process() error: %v", err)
		}
		if _, ok := mc.SystemParts["14_perm_control"]; ok {
			t.Error("expected no system part when config is wrong type")
		}
	})
}

// =====================================================================
// TestIsPermControlEnabled
// =====================================================================

func TestIsPermControlEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  *PermUsersConfig
		want bool
	}{
		{"nil config", nil, false},
		{"both empty", &PermUsersConfig{}, false},
		{"default only", &PermUsersConfig{DefaultUser: "worker"}, true},
		{"privileged only", &PermUsersConfig{PrivilegedUser: "admin"}, true},
		{"both set", &PermUsersConfig{DefaultUser: "worker", PrivilegedUser: "admin"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsPermControlEnabled(tt.cfg)
			if got != tt.want {
				t.Errorf("IsPermControlEnabled(%v) = %v, want %v", tt.cfg, got, tt.want)
			}
		})
	}
}

// =====================================================================
// TestFormatProjectContext
// =====================================================================

func TestFormatProjectContext(t *testing.T) {
	t.Run("short_content", func(t *testing.T) {
		got := formatProjectContext("hello world", "AGENT.md")
		if !strings.Contains(got, "hello world") {
			t.Error("should contain the content")
		}
		if !strings.Contains(got, "AGENT.md") {
			t.Error("should contain the file path")
		}
		if !strings.Contains(got, "Project Context") {
			t.Error("should contain 'Project Context'")
		}
	})

	t.Run("truncates_long_content", func(t *testing.T) {
		longContent := strings.Repeat("x", maxProjectContextChars+5000)
		got := formatProjectContext(longContent, "AGENT.md")
		if len(got) > maxProjectContextChars+600 { // allow for header/footer
			t.Errorf("expected truncation, got len=%d", len(got))
		}
		if !strings.Contains(got, "truncated") {
			t.Error("should contain truncation notice")
		}
	})
}

// =====================================================================
// TestLoadProjectContextFile
// =====================================================================

func TestLoadProjectContextFile(t *testing.T) {
	t.Run("empty_dir_returns_empty", func(t *testing.T) {
		dir := t.TempDir()
		got := LoadProjectContextFile(dir)
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("empty_dir_name_returns_empty", func(t *testing.T) {
		got := LoadProjectContextFile("")
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("loads_agent_md", func(t *testing.T) {
		dir := t.TempDir()
		content := "# My Project\nSome instructions."
		os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte(content), 0644)

		got := LoadProjectContextFile(dir)
		if !strings.Contains(got, content) {
			t.Errorf("expected content %q in result, got %q", content, got)
		}
	})

	t.Run("priority_xbot_context_over_agent_md", func(t *testing.T) {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, ".xbot"), 0755)
		os.WriteFile(filepath.Join(dir, ".xbot", "context.md"), []byte("xbot"), 0644)
		os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("agent"), 0644)

		got := LoadProjectContextFile(dir)
		if !strings.Contains(got, "xbot") {
			t.Errorf("expected .xbot/context.md to take priority, got %q", got)
		}
		if strings.Contains(got, "agent") {
			t.Error("should not contain AGENT.md content when .xbot/context.md exists")
		}
	})
}

// =====================================================================
// TestBuildSystemPrompt_Integration
// =====================================================================

func TestBuildSystemPrompt_Integration(t *testing.T) {
	t.Run("multiple_middlewares_ordered", func(t *testing.T) {
		mc := newMC()
		mc.SenderName = "alice"
		mc.UserContent = "hello"

		mc.SystemParts["00_base"] = "You are xbot."
		mc.SystemParts["20_memory"] = "# Memory\n\nSome memory.\n"
		mc.SystemParts["30_sender"] = fmt.Sprintf("\n## Current Sender\nName: %s\n", mc.SenderName)

		prompt := mc.BuildSystemPrompt()
		baseIdx := strings.Index(prompt, "You are xbot")
		memIdx := strings.Index(prompt, "# Memory")
		senderIdx := strings.Index(prompt, "## Current Sender")
		if baseIdx >= memIdx || memIdx >= senderIdx {
			t.Errorf("wrong ordering: base@%d, memory@%d, sender@%d", baseIdx, memIdx, senderIdx)
		}
	})
}
