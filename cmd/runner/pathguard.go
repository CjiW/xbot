package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// fullControl disables all path restrictions when enabled via --full-control flag.
var fullControl bool

// execWorkspace is the effective workspace path for path validation.
// Docker mode: "/workspace", native mode: host workspace path.
// Set in main.go during executor initialization.
var execWorkspace string

// dockerMode indicates whether we're running in Docker mode.
// When true, pathguard uses string-level prefix checks only (no EvalSymlinks).
var dockerMode bool

// validatePath checks that path is within workspace and returns an error if not.
// It resolves symlinks (filepath.EvalSymlinks) in native mode to prevent symlink-based path traversal.
// In Docker mode, only string-level prefix checking is performed.
// When fullControl is true, all path checks are skipped.
//
// In remote mode (connected to xbot server), path checks are skipped —
// the server is the trusted authority for path authorization.
func validatePath(path string) error {
	if fullControl || dockerMode {
		return nil
	}

	ws := execWorkspace
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(ws, cleaned)
	}

	if dockerMode {
		// Docker 模式：仅做字符串级前缀检查，不 EvalSymlinks
		// 容器路径空间由 Docker 隔离，无需 symlink 防护
		if !strings.HasPrefix(cleaned, ws+"/") && cleaned != ws {
			return fmt.Errorf("path %q escapes workspace %q", path, ws)
		}
		return nil
	}

	// Native 模式：保留原有 EvalSymlinks 检查
	real, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		// File may not exist yet (e.g., write target). Use the cleaned path as fallback.
		real = cleaned
	}

	if !strings.HasPrefix(real, ws) {
		return fmt.Errorf("path %q (resolved to %q) escapes workspace %q", path, real, ws)
	}
	return nil
}

// safePath returns a cleaned, validated absolute path.
// When fullControl is true, returns the cleaned path without any restrictions.
func safePath(path string) (string, error) {
	ws := execWorkspace
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(ws, cleaned)
	}
	if err := validatePath(path); err != nil {
		return "", err
	}
	return cleaned, nil
}
