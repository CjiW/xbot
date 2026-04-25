package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// writeJSON creates dir/file and writes v as pretty-printed JSON.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// setupTempDirs creates a temp directory layout for testing.
// Returns (userHome, projectDir, cleanup).
func setupTempDirs(t *testing.T) (string, string, func()) {
	t.Helper()
	tmp := t.TempDir()
	userHome := filepath.Join(tmp, "home")
	projectDir := filepath.Join(tmp, "project")
	_ = os.MkdirAll(filepath.Join(userHome, ".xbot"), 0o755)
	_ = os.MkdirAll(filepath.Join(projectDir, ".xbot"), 0o755)
	return userHome, projectDir, func() {}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestLoadHooksConfig_NoFiles(t *testing.T) {
	userHome, projectDir, _ := setupTempDirs(t)

	layers, cfg, err := LoadHooksConfig(userHome, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) != 0 {
		t.Fatalf("expected 0 layers, got %d", len(layers))
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Hooks) != 0 {
		t.Fatalf("expected empty hooks map, got %d entries", len(cfg.Hooks))
	}
	if cfg.EnableCommandHooks {
		t.Fatal("expected EnableCommandHooks to be false by default")
	}
}

func TestLoadHooksConfig_UserOnly(t *testing.T) {
	userHome, projectDir, _ := setupTempDirs(t)

	userPath := filepath.Join(userHome, ".xbot", "hooks.json")
	writeJSON(t, userPath, &HookConfig{
		Hooks: map[string][]EventGroup{
			"PreToolUse": {
				{Matcher: "", Hooks: []HookDef{
					{Type: "command", Command: "echo user-hook"},
				}},
			},
		},
	})

	layers, cfg, err := LoadHooksConfig(userHome, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(layers))
	}
	if layers[0].Path != userPath {
		t.Fatalf("expected layer path %s, got %s", userPath, layers[0].Path)
	}

	groups := cfg.Hooks["PreToolUse"]
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[0].Hooks) != 1 || groups[0].Hooks[0].Command != "echo user-hook" {
		t.Fatalf("unexpected hooks: %+v", groups[0].Hooks)
	}
}

func TestLoadHooksConfig_ProjectOnly(t *testing.T) {
	userHome, projectDir, _ := setupTempDirs(t)

	projectPath := filepath.Join(projectDir, ".xbot", "hooks.json")
	writeJSON(t, projectPath, &HookConfig{
		Hooks: map[string][]EventGroup{
			"PostToolUse": {
				{Matcher: "Read", Hooks: []HookDef{
					{Type: "http", URL: "http://localhost:9999/hook"},
				}},
			},
		},
	})

	layers, cfg, err := LoadHooksConfig(userHome, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(layers))
	}

	groups := cfg.Hooks["PostToolUse"]
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Matcher != "Read" {
		t.Fatalf("expected matcher 'Read', got %q", groups[0].Matcher)
	}
}

func TestLoadHooksConfig_AllLayers(t *testing.T) {
	userHome, projectDir, _ := setupTempDirs(t)

	// User layer
	writeJSON(t, filepath.Join(userHome, ".xbot", "hooks.json"), &HookConfig{
		Hooks: map[string][]EventGroup{
			"SessionStart": {
				{Matcher: "", Hooks: []HookDef{
					{Type: "command", Command: "echo user-start"},
				}},
			},
		},
	})

	// Project layer
	writeJSON(t, filepath.Join(projectDir, ".xbot", "hooks.json"), &HookConfig{
		Hooks: map[string][]EventGroup{
			"SessionStart": {
				{Matcher: "", Hooks: []HookDef{
					{Type: "http", URL: "http://project/start"},
				}},
			},
			"PreToolUse": {
				{Matcher: "Shell", Hooks: []HookDef{
					{Type: "command", Command: "check-shell"},
				}},
			},
		},
	})

	// Local layer
	writeJSON(t, filepath.Join(projectDir, ".xbot", "hooks.local.json"), &HookConfig{
		EnableCommandHooks: true,
		Hooks: map[string][]EventGroup{
			"PreToolUse": {
				{Matcher: "Shell", Hooks: []HookDef{
					{Type: "command", Command: "local-override"},
				}},
			},
		},
	})

	layers, cfg, err := LoadHooksConfig(userHome, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) != 3 {
		t.Fatalf("expected 3 layers, got %d", len(layers))
	}

	// SessionStart: user + project both have matcher="" → hooks appended
	ssGroups := cfg.Hooks["SessionStart"]
	if len(ssGroups) != 1 {
		t.Fatalf("expected 1 SessionStart group, got %d", len(ssGroups))
	}
	if len(ssGroups[0].Hooks) != 2 {
		t.Fatalf("expected 2 SessionStart hooks, got %d", len(ssGroups[0].Hooks))
	}
	if ssGroups[0].Hooks[0].Command != "echo user-start" {
		t.Fatalf("expected first hook 'echo user-start', got %q", ssGroups[0].Hooks[0].Command)
	}
	if ssGroups[0].Hooks[1].URL != "http://project/start" {
		t.Fatalf("expected second hook URL 'http://project/start', got %q", ssGroups[0].Hooks[1].URL)
	}

	// PreToolUse: project + local both have matcher="Shell" → hooks appended
	ptuGroups := cfg.Hooks["PreToolUse"]
	if len(ptuGroups) != 1 {
		t.Fatalf("expected 1 PreToolUse group, got %d", len(ptuGroups))
	}
	if len(ptuGroups[0].Hooks) != 2 {
		t.Fatalf("expected 2 PreToolUse hooks, got %d", len(ptuGroups[0].Hooks))
	}

	// EnableCommandHooks from local layer
	if !cfg.EnableCommandHooks {
		t.Fatal("expected EnableCommandHooks=true from local layer")
	}
}

func TestLoadHooksConfig_MergeSameEventSameMatcher(t *testing.T) {
	userHome, projectDir, _ := setupTempDirs(t)

	// User layer: PreToolUse with matcher="Shell", 1 hook
	writeJSON(t, filepath.Join(userHome, ".xbot", "hooks.json"), &HookConfig{
		Hooks: map[string][]EventGroup{
			"PreToolUse": {
				{Matcher: "Shell", Hooks: []HookDef{
					{Type: "command", Command: "base-check"},
				}},
			},
		},
	})

	// Project layer: same event, same matcher, 1 hook
	writeJSON(t, filepath.Join(projectDir, ".xbot", "hooks.json"), &HookConfig{
		Hooks: map[string][]EventGroup{
			"PreToolUse": {
				{Matcher: "Shell", Hooks: []HookDef{
					{Type: "command", Command: "project-check"},
				}},
			},
		},
	})

	_, cfg, err := LoadHooksConfig(userHome, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	groups := cfg.Hooks["PreToolUse"]
	if len(groups) != 1 {
		t.Fatalf("expected 1 group (merged), got %d", len(groups))
	}
	if len(groups[0].Hooks) != 2 {
		t.Fatalf("expected 2 hooks (appended), got %d", len(groups[0].Hooks))
	}
	if groups[0].Hooks[0].Command != "base-check" {
		t.Fatalf("expected first hook 'base-check', got %q", groups[0].Hooks[0].Command)
	}
	if groups[0].Hooks[1].Command != "project-check" {
		t.Fatalf("expected second hook 'project-check', got %q", groups[0].Hooks[1].Command)
	}
}

func TestLoadHooksConfig_InvalidJSON(t *testing.T) {
	userHome, _, _ := setupTempDirs(t)

	userPath := filepath.Join(userHome, ".xbot", "hooks.json")
	if err := os.WriteFile(userPath, []byte("{invalid json}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, _, err := LoadHooksConfig(userHome, "")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestLoadHooksConfig_EnableCommandHooks(t *testing.T) {
	userHome, projectDir, _ := setupTempDirs(t)

	// User layer: no enable_command_hooks (defaults to false)
	writeJSON(t, filepath.Join(userHome, ".xbot", "hooks.json"), &HookConfig{
		Hooks: map[string][]EventGroup{},
	})

	// Project layer: explicitly sets enable_command_hooks=true
	writeJSON(t, filepath.Join(projectDir, ".xbot", "hooks.json"), &HookConfig{
		Hooks:              map[string][]EventGroup{},
		EnableCommandHooks: true,
	})

	_, cfg, err := LoadHooksConfig(userHome, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.EnableCommandHooks {
		t.Fatal("expected EnableCommandHooks=true after overlay")
	}

	// Also verify that default (no file) is false
	emptyHome := filepath.Join(t.TempDir(), "home2")
	_ = os.MkdirAll(filepath.Join(emptyHome, ".xbot"), 0o755)
	_, cfg2, err := LoadHooksConfig(emptyHome, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg2.EnableCommandHooks {
		t.Fatal("expected EnableCommandHooks=false when no config file exists")
	}
}

func TestLoadHooksConfig_EmptyProjectDir(t *testing.T) {
	userHome, _, _ := setupTempDirs(t)

	// Only user layer exists
	writeJSON(t, filepath.Join(userHome, ".xbot", "hooks.json"), &HookConfig{
		Hooks: map[string][]EventGroup{
			"SessionStart": {
				{Matcher: "", Hooks: []HookDef{
					{Type: "command", Command: "hello"},
				}},
			},
		},
	})

	layers, cfg, err := LoadHooksConfig(userHome, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(layers) != 1 {
		t.Fatalf("expected 1 layer, got %d", len(layers))
	}
	if _, ok := cfg.Hooks["SessionStart"]; !ok {
		t.Fatal("expected SessionStart in hooks")
	}
}

func TestLoadHooksConfig_MergeSameEventDifferentMatcher(t *testing.T) {
	userHome, projectDir, _ := setupTempDirs(t)

	// User: PreToolUse matcher="Shell"
	writeJSON(t, filepath.Join(userHome, ".xbot", "hooks.json"), &HookConfig{
		Hooks: map[string][]EventGroup{
			"PreToolUse": {
				{Matcher: "Shell", Hooks: []HookDef{
					{Type: "command", Command: "shell-check"},
				}},
			},
		},
	})

	// Project: PreToolUse matcher="Write" (different matcher)
	writeJSON(t, filepath.Join(projectDir, ".xbot", "hooks.json"), &HookConfig{
		Hooks: map[string][]EventGroup{
			"PreToolUse": {
				{Matcher: "Write", Hooks: []HookDef{
					{Type: "command", Command: "write-check"},
				}},
			},
		},
	})

	_, cfg, err := LoadHooksConfig(userHome, projectDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	groups := cfg.Hooks["PreToolUse"]
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups (different matchers), got %d", len(groups))
	}
}
