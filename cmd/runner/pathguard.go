package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// validatePath checks that path is within workspace and returns a cleaned absolute path.
// It resolves symlinks (filepath.EvalSymlinks) to prevent symlink-based path traversal.
func validatePath(path, workspace string) error {
	// Resolve the path to its real location (following symlinks).
	// filepath.EvalSymlinks resolves all symbolic links and returns the absolute path.
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		// File may not exist yet (e.g., write target). Fall back to filepath.Abs + Clean.
		real = filepath.Clean(path)
		if !filepath.IsAbs(real) {
			real = filepath.Join(workspace, real)
		}
	}

	if !strings.HasPrefix(real, workspace) {
		return fmt.Errorf("path %q (resolved to %q) escapes workspace %q", path, real, workspace)
	}
	if real == workspace {
		return fmt.Errorf("path cannot be the workspace root itself")
	}
	return nil
}

// safePath returns a cleaned, validated path.
func safePath(path, workspace string) (string, error) {
	if err := validatePath(path, workspace); err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}
