package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// validatePath checks that path is within workspace and returns a cleaned absolute path.
func validatePath(path, workspace string) error {
	cleaned := filepath.Clean(path)
	if !strings.HasPrefix(cleaned, workspace) {
		return fmt.Errorf("path %q escapes workspace %q", cleaned, workspace)
	}
	if cleaned == workspace {
		return fmt.Errorf("path cannot be the workspace root itself")
	}
	return nil
}

// safePath returns a cleaned path after validation.
func safePath(path, workspace string) (string, error) {
	if err := validatePath(path, workspace); err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}
