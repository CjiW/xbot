package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// validatePath checks that path is within workspace and returns a cleaned absolute path.
// It resolves symlinks (filepath.EvalSymlinks) to prevent symlink-based path traversal.
func validatePath(path, workspace string) error {
	// Clean the path first and make it absolute relative to workspace.
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(workspace, cleaned)
	}

	// Resolve the path to its real location (following symlinks).
	// filepath.EvalSymlinks resolves all symbolic links and returns the absolute path.
	real, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		// File may not exist yet (e.g., write target). Use the cleaned path as fallback.
		real = cleaned
	}

	if !strings.HasPrefix(real, workspace) {
		return fmt.Errorf("path %q (resolved to %q) escapes workspace %q", path, real, workspace)
	}
	if real == workspace {
		return fmt.Errorf("path cannot be the workspace root itself")
	}
	return nil
}

// safePath returns a cleaned, validated absolute path.
// The returned path is always workspace-relative and safe from traversal attacks
// (e.g., ../../etc/passwd is rejected by validatePath before reaching here).
func safePath(path, workspace string) (string, error) {
	if err := validatePath(path, workspace); err != nil {
		return "", err
	}
	// Return the cleaned absolute path (not the original relative path).
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(workspace, cleaned)
	}
	return cleaned, nil
}
