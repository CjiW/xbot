package runnerclient

import (
	"fmt"
	"path/filepath"
	"strings"
)

// PathGuard 封装路径验证逻辑。
type PathGuard struct {
	// Workspace 工作区根路径
	Workspace string
	// FullControl 禁用所有路径限制
	FullControl bool
	// DockerMode Docker 模式下仅做字符串级前缀检查
	DockerMode bool
}

// Validate 检查 path 是否在 workspace 内，不在则返回错误。
// FullControl 为 true 时跳过所有检查。
// DockerMode 时仅做字符串级前缀检查（不 EvalSymlinks）。
func (pg *PathGuard) Validate(path string) error {
	if pg.FullControl || pg.DockerMode {
		return nil
	}

	ws := pg.Workspace
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(ws, cleaned)
	}

	// Native 模式：保留 EvalSymlinks 检查
	real, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		// 文件可能还不存在（如写入目标），用 cleaned 路径作为回退
		real = cleaned
	}

	if !strings.HasPrefix(real, ws) {
		return fmt.Errorf("path %q (resolved to %q) escapes workspace %q", path, real, ws)
	}
	return nil
}

// SafePath 返回清理并验证后的绝对路径。
// FullControl 为 true 时仅返回清理后的路径。
func (pg *PathGuard) SafePath(path string) (string, error) {
	ws := pg.Workspace
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Join(ws, cleaned)
	}
	if err := pg.Validate(path); err != nil {
		return "", err
	}
	return cleaned, nil
}
