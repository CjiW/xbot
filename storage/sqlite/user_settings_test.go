package sqlite

import (
	"path/filepath"
	"testing"
)

func TestUserSettingsCRUD(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	svc := NewUserSettingsService(db)

	// Get empty
	settings, err := svc.Get("feishu", "user1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(settings) != 0 {
		t.Errorf("expected empty settings, got %v", settings)
	}

	// Set a value
	if err := svc.Set("feishu", "user1", "reply_style", "concise"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Get should return it
	settings, err = svc.Get("feishu", "user1")
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if settings["reply_style"] != "concise" {
		t.Errorf("expected 'concise', got %q", settings["reply_style"])
	}

	// Update (upsert)
	if err := svc.Set("feishu", "user1", "reply_style", "detailed"); err != nil {
		t.Fatalf("set update: %v", err)
	}
	settings, _ = svc.Get("feishu", "user1")
	if settings["reply_style"] != "detailed" {
		t.Errorf("expected 'detailed', got %q", settings["reply_style"])
	}

	// Set another key
	if err := svc.Set("feishu", "user1", "language", "zh"); err != nil {
		t.Fatalf("set language: %v", err)
	}
	settings, _ = svc.Get("feishu", "user1")
	if len(settings) != 2 {
		t.Errorf("expected 2 settings, got %d", len(settings))
	}

	// Delete
	if err := svc.Delete("feishu", "user1", "reply_style"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	settings, _ = svc.Get("feishu", "user1")
	if _, ok := settings["reply_style"]; ok {
		t.Error("expected reply_style to be deleted")
	}
	if settings["language"] != "zh" {
		t.Error("language should still exist")
	}

	// Different channel should have separate settings
	settings2, _ := svc.Get("cli", "user1")
	if len(settings2) != 0 {
		t.Errorf("expected empty settings for different channel, got %v", settings2)
	}
}

func TestUserSettingsDifferentSenders(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	svc := NewUserSettingsService(db)

	if err := svc.Set("feishu", "user1", "style", "a"); err != nil {
		t.Fatalf("set user1: %v", err)
	}
	if err := svc.Set("feishu", "user2", "style", "b"); err != nil {
		t.Fatalf("set user2: %v", err)
	}

	s1, _ := svc.Get("feishu", "user1")
	s2, _ := svc.Get("feishu", "user2")
	if s1["style"] != "a" || s2["style"] != "b" {
		t.Errorf("settings should be per-user: user1=%q user2=%q", s1["style"], s2["style"])
	}
}
