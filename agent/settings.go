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
	store         *sqlite.UserSettingsService
	channelFinder func(string) (channel.Channel, bool)
}

// NewSettingsService creates a new SettingsService.
func NewSettingsService(store *sqlite.UserSettingsService) *SettingsService {
	return &SettingsService{store: store}
}

// SetChannelFinder sets the channel finder callback (injected from Agent or main.go).
func (s *SettingsService) SetChannelFinder(fn func(string) (channel.Channel, bool)) {
	s.channelFinder = fn
}

// GetSettings retrieves all settings for a user on a specific channel.
func (s *SettingsService) GetSettings(channelName, senderID string) (map[string]string, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("settings service not initialized")
	}
	return s.store.Get(channelName, senderID)
}

// DeleteSetting removes a single setting key for a user on a specific channel.
func (s *SettingsService) DeleteSetting(channelName, senderID, key string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("settings service not initialized")
	}
	return s.store.Delete(channelName, senderID, key)
}

// GetPermUsers retrieves the permission control user configuration for a user.
// Returns a PermUsersConfig with DefaultUser and PrivilegedUser from user settings.
func (s *SettingsService) GetPermUsers(channelName, senderID string) *PermUsersConfig {
	if s == nil || s.store == nil {
		return nil
	}
	settings, err := s.store.Get(channelName, senderID)
	if err != nil {
		return nil
	}
	config := &PermUsersConfig{
		DefaultUser:    settings["default_user"],
		PrivilegedUser: settings["privileged_user"],
	}
	if config.DefaultUser == "" && config.PrivilegedUser == "" {
		return nil // feature disabled
	}
	return config
}

// SetSetting sets a single setting value.
func (s *SettingsService) SetSetting(channelName, senderID, key, value string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("settings service not initialized")
	}
	return s.store.Set(channelName, senderID, key, value)
}

// GetSettingsSchema retrieves the settings schema for a channel.
// Returns nil if the channel is not found or doesn't implement SettingsCapability.
func (s *SettingsService) GetSettingsSchema(channelName string) []channel.SettingDefinition {
	if s.channelFinder == nil {
		return nil
	}
	ch, ok := s.channelFinder(channelName)
	if !ok {
		return nil
	}
	sc, ok := ch.(channel.SettingsCapability)
	if !ok {
		return nil
	}
	return sc.SettingsSchema()
}

// GetSettingsUI renders the settings UI for a channel.
// It uses channelFinder internally to look up the channel instance.
// If the channel is not found or has no schema, falls back to text UI.
func (s *SettingsService) GetSettingsUI(channelName, senderID string) (string, error) {
	settings, err := s.store.Get(channelName, senderID)
	if err != nil {
		return "", err
	}

	// Try to find the channel and get its schema
	var ch channel.Channel
	schema := []channel.SettingDefinition{}
	if s.channelFinder != nil {
		if found, ok := s.channelFinder(channelName); ok {
			ch = found
			if sc, ok := found.(channel.SettingsCapability); ok {
				schema = sc.SettingsSchema()
			}
		}
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
// It uses channelFinder internally to look up the channel instance.
// For channels with SettingsCapability, it delegates to HandleSettingSubmit.
// For text mode (no channel or no capability), it parses "key=value" format.
func (s *SettingsService) SubmitSettings(channelName, senderID, rawInput string) error {
	// Try to find the channel for interactive submit
	if s.channelFinder != nil {
		if ch, ok := s.channelFinder(channelName); ok {
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
		}
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
