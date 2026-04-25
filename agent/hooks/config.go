package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// Config types
// ---------------------------------------------------------------------------

// HookConfig holds the merged hooks configuration loaded from JSON files.
type HookConfig struct {
	// Hooks maps event names to a list of EventGroups.
	Hooks map[string][]EventGroup `json:"hooks"`
	// EnableCommandHooks controls whether command-type hooks are allowed.
	// Defaults to false for safety.
	EnableCommandHooks bool `json:"enable_command_hooks,omitempty"`
}

// ConfigLayer records a single configuration file that was loaded.
type ConfigLayer struct {
	// Path is the filesystem path of the configuration file.
	Path string
	// Config is the parsed configuration from that file.
	Config HookConfig
}

// ---------------------------------------------------------------------------
// LoadHooksConfig — three-layer merge
// ---------------------------------------------------------------------------

// LoadHooksConfig reads and merges hooks configuration from up to three layers:
//
//  1. User layer:   ~/.xbot/hooks.json
//  2. Project layer: <projectDir>/.xbot/hooks.json
//  3. Local layer:   <projectDir>/.xbot/hooks.local.json
//
// Later layers override earlier ones.  Within the same event + matcher,
// hooks are appended (never replaced).
//
// An empty projectDir skips project and local layers.
// Missing files are silently ignored.
func LoadHooksConfig(userHome string, projectDir string) ([]*ConfigLayer, *HookConfig, error) {
	layers := make([]*ConfigLayer, 0, 3)
	merged := &HookConfig{Hooks: make(map[string][]EventGroup)}

	// 1. User layer
	userPath := filepath.Join(userHome, ".xbot", "hooks.json")
	if cfg, err := loadConfigFile(userPath); err != nil {
		return nil, nil, fmt.Errorf("user hooks config %s: %w", userPath, err)
	} else if cfg != nil {
		layers = append(layers, &ConfigLayer{Path: userPath, Config: *cfg})
		merged = mergeConfigs(merged, cfg)
	}

	// 2. Project & local layers — only when projectDir is non-empty
	if projectDir != "" {
		projectPath := filepath.Join(projectDir, ".xbot", "hooks.json")
		if cfg, err := loadConfigFile(projectPath); err != nil {
			return nil, nil, fmt.Errorf("project hooks config %s: %w", projectPath, err)
		} else if cfg != nil {
			layers = append(layers, &ConfigLayer{Path: projectPath, Config: *cfg})
			merged = mergeConfigs(merged, cfg)
		}

		localPath := filepath.Join(projectDir, ".xbot", "hooks.local.json")
		if cfg, err := loadConfigFile(localPath); err != nil {
			return nil, nil, fmt.Errorf("local hooks config %s: %w", localPath, err)
		} else if cfg != nil {
			layers = append(layers, &ConfigLayer{Path: localPath, Config: *cfg})
			merged = mergeConfigs(merged, cfg)
		}
	}

	return layers, merged, nil
}

// ---------------------------------------------------------------------------
// loadConfigFile
// ---------------------------------------------------------------------------

// loadConfigFile reads and parses a single hooks.json file.
// Returns (nil, nil) when the file does not exist.
func loadConfigFile(path string) (*HookConfig, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is constructed by caller
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg HookConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Ensure hooks map is never nil.
	if cfg.Hooks == nil {
		cfg.Hooks = make(map[string][]EventGroup)
	}

	return &cfg, nil
}

// ---------------------------------------------------------------------------
// mergeConfigs
// ---------------------------------------------------------------------------

// mergeConfigs merges overlay into base and returns the result.
// The base value is not mutated; a new HookConfig is returned.
//
// Merge rules:
//   - Different events: simple union.
//   - Same event, different matcher: append the overlay groups.
//   - Same event, same matcher: overlay hooks are appended to the existing group's hooks.
//   - EnableCommandHooks: overlay value wins if overlay explicitly sets it.
func mergeConfigs(base, overlay *HookConfig) *HookConfig {
	result := &HookConfig{
		Hooks:              make(map[string][]EventGroup, len(base.Hooks)),
		EnableCommandHooks: base.EnableCommandHooks,
	}

	// Copy base hooks.
	for evt, groups := range base.Hooks {
		result.Hooks[evt] = copyGroups(groups)
	}

	// Merge overlay hooks.
	for evt, overlayGroups := range overlay.Hooks {
		existingGroups, ok := result.Hooks[evt]
		if !ok {
			// New event — just copy.
			result.Hooks[evt] = copyGroups(overlayGroups)
			continue
		}

		// Merge groups for the same event.
		for _, og := range overlayGroups {
			idx := findGroupByMatcher(existingGroups, og.Matcher)
			if idx >= 0 {
				// Same matcher — append hooks.
				existingGroups[idx].Hooks = append(existingGroups[idx].Hooks, og.Hooks...)
			} else {
				// Different matcher — append as new group.
				existingGroups = append(existingGroups, EventGroup{
					Matcher: og.Matcher,
					Hooks:   append([]HookDef{}, og.Hooks...),
				})
			}
		}
		result.Hooks[evt] = existingGroups
	}

	// EnableCommandHooks: overlay wins if set.
	if overlay.EnableCommandHooks {
		result.EnableCommandHooks = true
	}

	return result
}

// findGroupByMatcher returns the index of the first EventGroup with the given
// matcher, or -1 if not found.
func findGroupByMatcher(groups []EventGroup, matcher string) int {
	for i, g := range groups {
		if g.Matcher == matcher {
			return i
		}
	}
	return -1
}

// copyGroups returns a deep copy of the event group slice.
func copyGroups(groups []EventGroup) []EventGroup {
	out := make([]EventGroup, len(groups))
	for i, g := range groups {
		out[i] = EventGroup{
			Matcher: g.Matcher,
			Hooks:   append([]HookDef{}, g.Hooks...),
		}
	}
	return out
}
