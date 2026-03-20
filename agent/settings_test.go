package agent

import (
	"path/filepath"
	"testing"

	"xbot/storage/sqlite"
)

func TestSettingsServiceGetSettings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := sqlite.NewUserSettingsService(db)
	svc := NewSettingsService(store)

	// Set some values
	if err := svc.SetSetting("feishu", "user1", "context_mode", "phase2"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Get should return them
	settings, err := svc.GetSettings("feishu", "user1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if settings["context_mode"] != "phase2" {
		t.Errorf("expected 'phase2', got %q", settings["context_mode"])
	}
}

func TestSettingsServiceGetSettingsUI(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := sqlite.NewUserSettingsService(db)
	svc := NewSettingsService(store)

	// No channelFinder set — should return "no settings" fallback
	ui, err := svc.GetSettingsUI("test", "user1")
	if err != nil {
		t.Fatalf("get settings ui: %v", err)
	}
	if ui != "当前渠道没有可配置的设置项。" {
		t.Errorf("expected no settings message, got %q", ui)
	}
}

func TestSettingsServiceSubmitSettingsTextMode(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := sqlite.NewUserSettingsService(db)
	svc := NewSettingsService(store)

	// No channelFinder set — text mode fallback
	err = svc.SubmitSettings("cli", "user1", "key1=value1\nkey2=value2")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	settings, _ := svc.GetSettings("cli", "user1")
	if settings["key1"] != "value1" {
		t.Errorf("expected key1=value1, got %q", settings["key1"])
	}
	if settings["key2"] != "value2" {
		t.Errorf("expected key2=value2, got %q", settings["key2"])
	}

	// Test error on invalid format
	err = svc.SubmitSettings("cli", "user1", "invalid_line_no_equals")
	if err == nil {
		t.Error("expected error for invalid format")
	}
}
