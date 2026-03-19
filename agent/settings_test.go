package agent

import (
	"path/filepath"
	"testing"

	"xbot/bus"
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
	if err := svc.SetSetting("feishu", "user1", "reply_style", "concise"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Get should return them
	settings, err := svc.GetSettings("feishu", "user1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if settings["reply_style"] != "concise" {
		t.Errorf("expected 'concise', got %q", settings["reply_style"])
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

	// Use a mock channel that doesn't implement SettingsCapability
	ui, err := svc.GetSettingsUI(&noCapabilityChannel{}, "user1")
	if err != nil {
		t.Fatalf("get settings ui: %v", err)
	}
	// Should return "no settings" since channel has no schema
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

	ch := &noCapabilityChannel{}

	// Submit key=value pairs
	err = svc.SubmitSettings(ch, "cli", "user1", "key1=value1\nkey2=value2")
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
	err = svc.SubmitSettings(ch, "cli", "user1", "invalid_line_no_equals")
	if err == nil {
		t.Error("expected error for invalid format")
	}
}

// noCapabilityChannel is a minimal channel that doesn't implement SettingsCapability
type noCapabilityChannel struct{}

func (c *noCapabilityChannel) Name() string { return "test" }
func (c *noCapabilityChannel) Start() error { return nil }
func (c *noCapabilityChannel) Stop()        {}
func (c *noCapabilityChannel) Send(msg bus.OutboundMessage) (string, error) {
	return "", nil
}
