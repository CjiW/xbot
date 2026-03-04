package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveSafePath 解析路径并确保不越界
// 如果 path 是绝对路径，直接返回（不限制）
// 如果是相对路径，基于 workingDir 解析，并检查不会逃逸
func ResolveSafePath(workingDir, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}

	// 绝对路径：直接返回（向后兼容，主 Agent 需要访问任意路径）
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}

	if workingDir == "" {
		return "", fmt.Errorf("working directory not set")
	}

	resolved := filepath.Join(workingDir, path)
	resolved = filepath.Clean(resolved)

	// 确保解析后的路径在 workingDir 内
	if !strings.HasPrefix(resolved, filepath.Clean(workingDir)+string(filepath.Separator)) &&
		resolved != filepath.Clean(workingDir) {
		return "", fmt.Errorf("path %q escapes working directory", path)
	}

	return resolved, nil
}

// EnsureDir 确保目录存在
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}
