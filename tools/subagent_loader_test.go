package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFrontmatter_WithCapabilities(t *testing.T) {
	fm := `name: test-agent
description: "A test agent"
tools:
  - Shell
  - Read
capabilities:
  memory: true
  send_message: true`

	name, desc, _, tools, caps, err := parseFrontmatter(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "test-agent" {
		t.Errorf("name = %q, want %q", name, "test-agent")
	}
	if desc != "A test agent" {
		t.Errorf("description = %q", desc)
	}
	if len(tools) != 2 || tools[0] != "Shell" || tools[1] != "Read" {
		t.Errorf("tools = %v, want [Shell Read]", tools)
	}
	if !caps.Memory {
		t.Error("expected Memory=true")
	}
	if !caps.SendMessage {
		t.Error("expected SendMessage=true")
	}
	// SpawnAgent defaults to true when not explicitly set in frontmatter
	if !caps.SpawnAgent {
		t.Error("expected SpawnAgent=true (default)")
	}
}

func TestParseFrontmatter_NoCapabilities(t *testing.T) {
	fm := `name: simple
description: "Simple agent"
tools:
  - Shell`

	_, _, _, _, caps, err := parseFrontmatter(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if caps.Memory || caps.SendMessage {
		t.Error("expected memory and send_message false when not specified")
	}
	// SpawnAgent defaults to true
	if !caps.SpawnAgent {
		t.Error("expected SpawnAgent=true (default) when not specified")
	}
}

func TestParseFrontmatter_AllCapabilities(t *testing.T) {
	fm := `name: powerful
description: "Powerful agent"
capabilities:
  memory: true
  send_message: true
  spawn_agent: true`

	_, _, _, _, caps, err := parseFrontmatter(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !caps.Memory || !caps.SendMessage || !caps.SpawnAgent {
		t.Errorf("expected all capabilities true, got %+v", caps)
	}
}

func TestLoadAgentRole_WithCapabilities(t *testing.T) {
	dir := t.TempDir()
	content := `---
name: cap-agent
description: "Agent with capabilities"
tools:
  - Shell
capabilities:
  memory: true
  spawn_agent: true
---

You are a capable agent.
`
	if err := os.WriteFile(filepath.Join(dir, "cap-agent.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	roles, err := LoadAgentRoles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("expected 1 role, got %d", len(roles))
	}

	role := roles[0]
	if role.Name != "cap-agent" {
		t.Errorf("name = %q", role.Name)
	}
	if !role.Capabilities.Memory {
		t.Error("expected Memory=true")
	}
	if !role.Capabilities.SpawnAgent {
		t.Error("expected SpawnAgent=true")
	}
	if role.Capabilities.SendMessage {
		t.Error("expected SendMessage=false")
	}
	if role.SystemPrompt != "You are a capable agent." {
		t.Errorf("SystemPrompt = %q", role.SystemPrompt)
	}
}

func TestParseFrontmatter_ChineseName(t *testing.T) {
	fm := `name: 中书省
description: "规划决策中枢"
capabilities:
  memory: true
  spawn_agent: true`

	name, desc, _, _, caps, err := parseFrontmatter(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "中书省" {
		t.Errorf("name = %q, want %q", name, "中书省")
	}
	if desc != "规划决策中枢" {
		t.Errorf("description = %q", desc)
	}
	if !caps.Memory || !caps.SpawnAgent {
		t.Errorf("expected Memory=true, SpawnAgent=true, got %+v", caps)
	}
}

func TestParseFrontmatter_MixedName(t *testing.T) {
	fm := `name: 工部-dev_01
description: "Mixed CJK and ASCII name"`

	name, _, _, _, _, err := parseFrontmatter(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "工部-dev_01" {
		t.Errorf("name = %q, want %q", name, "工部-dev_01")
	}
}

func TestParseFrontmatter_InvalidNameWithSpaces(t *testing.T) {
	fm := `name: 中书 省`

	_, _, _, _, _, err := parseFrontmatter(fm)
	if err == nil {
		t.Fatal("expected error for name with spaces, got nil")
	}
}

func TestParseFrontmatter_ModelField(t *testing.T) {
	fm := `---
name: test-role
description: A test role
model: claude-sonnet-4-20250514
tools:
  - Read
  - Grep
---
System prompt here.`

	name, desc, model, tools, caps, err := parseFrontmatter(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "test-role" {
		t.Errorf("name = %q, want %q", name, "test-role")
	}
	if desc != "A test role" {
		t.Errorf("desc = %q, want %q", desc, "A test role")
	}
	if model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", model, "claude-sonnet-4-20250514")
	}
	if len(tools) != 2 || tools[0] != "Read" || tools[1] != "Grep" {
		t.Errorf("tools = %v, want [Read Grep]", tools)
	}
	if !caps.SpawnAgent {
		t.Error("caps.SpawnAgent should be true by default")
	}
}

func TestParseFrontmatter_NoModelField(t *testing.T) {
	fm := `---
name: test-role
tools:
  - Read
---
System prompt.`

	name, _, model, _, _, err := parseFrontmatter(fm)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "test-role" {
		t.Errorf("name = %q", name)
	}
	if model != "" {
		t.Errorf("model = %q, want empty string", model)
	}
}
