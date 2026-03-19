package agent

import (
	"context"
	"fmt"
	"strings"

	"xbot/channel"
	"xbot/storage/sqlite"
)

// SettingsService provides user settings management.
type SettingsService struct {
	store *sqlite.UserSettingsService
}

// NewSettingsService creates a new SettingsService.
func NewSettingsService(store *sqlite.UserSettingsService) *SettingsService {
	return &SettingsService{store: store}
}

// GetSettings retrieves all settings for a user on a specific channel.
func (s *SettingsService) GetSettings(channelName, senderID string) (map[string]string, error) {
	return s.store.Get(channelName, senderID)
}

// SetSetting sets a single setting value.
func (s *SettingsService) SetSetting(channelName, senderID, key, value string) error {
	return s.store.Set(channelName, senderID, key, value)
}

// GetSettingsUI renders the settings UI for a channel.
// If the channel implements UIBuilder, it uses the interactive card UI.
// Otherwise, falls back to text-based settings list.
func (s *SettingsService) GetSettingsUI(ch channel.Channel, senderID string) (string, error) {
	settings, err := s.store.Get(ch.Name(), senderID)
	if err != nil {
		return "", err
	}

	// Check if channel provides SettingsCapability
	schema := []channel.SettingDefinition{}
	if sc, ok := ch.(channel.SettingsCapability); ok {
		schema = sc.SettingsSchema()
	}

	if len(schema) == 0 {
		return "当前渠道没有可配置的设置项。", nil
	}

	// Check if channel implements UIBuilder for interactive UI
	if builder, ok := ch.(channel.UIBuilder); ok {
		return builder.BuildSettingsUI(context.Background(), schema, settings), nil
	}

	// Fallback to text UI
	return channel.BuildTextSettingsUI(schema, settings), nil
}

// SubmitSettings processes a settings submission.
// For channels with SettingsCapability, it delegates to HandleSettingSubmit.
// For text mode, it parses "key=value" format.
func (s *SettingsService) SubmitSettings(ch channel.Channel, channelName, senderID, rawInput string) error {
	// Check if channel provides SettingsCapability with interactive submit
	if sc, ok := ch.(channel.SettingsCapability); ok {
		values, err := sc.HandleSettingSubmit(context.Background(), rawInput)
		if err != nil {
			return err
		}
		for key, value := range values {
			if err := s.store.Set(channelName, senderID, key, value); err != nil {
				return fmt.Errorf("save setting %s: %w", key, err)
			}
		}
		return nil
	}

	// Text mode: parse "key=value" format
	// Supports multiple key=value pairs separated by newlines
	for _, line := range strings.Split(rawInput, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid setting format: %q (expected key=value)", line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}

		if err := s.store.Set(channelName, senderID, key, value); err != nil {
			return fmt.Errorf("save setting %s: %w", key, err)
		}
	}

	return nil
}
