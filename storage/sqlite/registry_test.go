package sqlite

import (
	"path/filepath"
	"testing"
)

func TestSharedSkillRegistryCRUD(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	reg := NewSharedSkillRegistry(db)

	// Publish a skill
	entry := &SharedEntry{
		Type:        "skill",
		Name:        "test-skill",
		Description: "A test skill",
		Author:      "user1",
		Tags:        "test,example",
		SourcePath:  "/tmp/skills/test-skill",
		Sharing:     "public",
	}
	if err := reg.Publish(entry); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if entry.ID == 0 {
		t.Fatal("expected non-zero ID after publish")
	}

	// ListShared should return the published entry
	entries, err := reg.ListShared("skill", 10, 0)
	if err != nil {
		t.Fatalf("list shared: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "test-skill" {
		t.Errorf("expected name 'test-skill', got %q", entries[0].Name)
	}

	// SearchShared should find it
	results, err := reg.SearchShared("test", "skill", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}

	// GetByID
	got, err := reg.GetByID(entry.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got == nil || got.Name != "test-skill" {
		t.Errorf("expected test-skill, got %v", got)
	}

	// GetByTypeAndName
	got, err = reg.GetByTypeAndName("skill", "test-skill")
	if err != nil {
		t.Fatalf("get by type+name: %v", err)
	}
	if got == nil || got.ID != entry.ID {
		t.Errorf("expected ID %d, got %v", entry.ID, got)
	}

	// Unpublish
	if err := reg.Unpublish(entry.ID, "user1"); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	entries, err = reg.ListShared("skill", 10, 0)
	if err != nil {
		t.Fatalf("list after unpublish: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after unpublish, got %d", len(entries))
	}
}

func TestSharedSkillRegistryListByAuthor(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	reg := NewSharedSkillRegistry(db)

	// Publish two entries from same author
	for i := 0; i < 2; i++ {
		if err := reg.Publish(&SharedEntry{
			Type:        "skill",
			Name:        "skill-" + string(rune('a'+i)),
			Description: "desc",
			Author:      "user1",
			SourcePath:  "/tmp/s",
			Sharing:     "public",
		}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	entries, err := reg.ListByAuthor("user1")
	if err != nil {
		t.Fatalf("list by author: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestSharedSkillRegistryUnpublishWrongAuthor(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	reg := NewSharedSkillRegistry(db)

	if err := reg.Publish(&SharedEntry{
		Type:        "skill",
		Name:        "mine",
		Description: "desc",
		Author:      "user1",
		SourcePath:  "/tmp/s",
		Sharing:     "public",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// user2 tries to unpublish user1's entry
	entries, _ := reg.ListShared("", 10, 0)
	if len(entries) == 0 {
		t.Fatal("expected at least one entry")
	}
	err = reg.Unpublish(entries[0].ID, "user2")
	if err == nil {
		t.Error("expected error when unpublishing other user's entry")
	}
}
