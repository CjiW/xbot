package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xbot/tools"
)

func writeSkill(t *testing.T, rootDir, folder, name, desc string) string {
	t.Helper()
	dir := filepath.Join(rootDir, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	content := "---\n" +
		"name: " + name + "\n" +
		"description: " + desc + "\n" +
		"---\n\n" +
		"# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return filepath.Join(dir, "SKILL.md")
}

func TestSkillStore_GlobalAndPrivateCatalog(t *testing.T) {
	workDir := t.TempDir()
	globalDir := filepath.Join(workDir, ".claude", "skills")
	privateDir := tools.UserSkillsRoot(workDir, "user-1")

	writeSkill(t, globalDir, "global-tool", "global-tool", "global skill")
	writeSkill(t, privateDir, "private-tool", "private-tool", "private skill")

	store := NewSkillStore(workDir, []string{globalDir}, nil)
	catalog := store.GetSkillsCatalog(context.Background(), "user-1")

	if !strings.Contains(catalog, "<name>global-tool</name>") {
		t.Fatalf("expected global skill in catalog, got: %s", catalog)
	}
	if !strings.Contains(catalog, "<name>private-tool</name>") {
		t.Fatalf("expected private skill in catalog, got: %s", catalog)
	}
	// Catalog must NOT contain host filesystem paths
	if strings.Contains(catalog, "<location>") {
		t.Fatalf("catalog must not contain <location> tags (path leakage), got: %s", catalog)
	}
}

func TestSkillStore_PrivateOverrideGlobal(t *testing.T) {
	workDir := t.TempDir()
	globalDir := filepath.Join(workDir, ".claude", "skills")
	privateDir := tools.UserSkillsRoot(workDir, "user-1")

	writeSkill(t, globalDir, "dup", "dup", "global dup")
	writeSkill(t, privateDir, "dup", "dup", "private dup")

	store := NewSkillStore(workDir, []string{globalDir}, nil)
	catalog := store.GetSkillsCatalog(context.Background(), "user-1")

	if strings.Count(catalog, "<name>dup</name>") != 1 {
		t.Fatalf("expected deduped skill entry, got: %s", catalog)
	}
	if !strings.Contains(catalog, "private dup") {
		t.Fatalf("expected private dup to override global dup, got: %s", catalog)
	}
}
