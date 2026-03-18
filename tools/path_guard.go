package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func defaultWorkspaceRoot(ctx *ToolContext) string {
	if ctx == nil {
		return ""
	}
	if ctx.WorkspaceRoot != "" {
		return ctx.WorkspaceRoot
	}
	return ctx.WorkingDir
}

func resolveScopedBase(ctx *ToolContext) (string, error) {
	root := defaultWorkspaceRoot(ctx)
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		root = cwd
	}
	absRoot, err := cleanAbsPath(root)
	if err != nil {
		return "", fmt.Errorf("invalid workspace root: %w", err)
	}
	return absRoot, nil
}

func ResolveWritePath(ctx *ToolContext, inputPath string) (string, error) {
	if inputPath == "" {
		return "", fmt.Errorf("path is required")
	}

	if ctx == nil || (ctx.WorkspaceRoot == "" && ctx.WorkingDir == "" && len(ctx.ReadOnlyRoots) == 0) {
		if filepath.IsAbs(inputPath) {
			return cleanAbsPath(inputPath)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		return cleanAbsPath(filepath.Join(cwd, inputPath))
	}

	root, err := resolveScopedBase(ctx)
	if err != nil {
		return "", err
	}

	candidate := inputPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err = cleanAbsPath(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	// 检查目标或父目录（处理符号链接）
	checkPath := candidate
	if _, err := os.Stat(candidate); err != nil {
		checkPath = filepath.Dir(candidate)
	}
	realCheckPath, err := filepath.EvalSymlinks(checkPath)
	if err == nil {
		checkPath = realCheckPath
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}

	if !isWithinRoot(checkPath, realRoot) {
		return "", fmt.Errorf("write path escapes workspace: %s", inputPath)
	}
	return candidate, nil
}

func ResolveReadPath(ctx *ToolContext, inputPath string) (string, error) {
	if inputPath == "" {
		return "", fmt.Errorf("path is required")
	}

	if ctx == nil || (ctx.WorkspaceRoot == "" && ctx.WorkingDir == "" && len(ctx.ReadOnlyRoots) == 0) {
		if filepath.IsAbs(inputPath) {
			return cleanAbsPath(inputPath)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		return cleanAbsPath(filepath.Join(cwd, inputPath))
	}

	root, err := resolveScopedBase(ctx)
	if err != nil {
		return "", err
	}

	candidate := inputPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err = cleanAbsPath(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err == nil {
		candidate = realCandidate
	}

	allowedRoots := []string{root}
	allowedRoots = append(allowedRoots, ctx.ReadOnlyRoots...)

	for _, allowed := range allowedRoots {
		if allowed == "" {
			continue
		}
		absAllowed, err := cleanAbsPath(allowed)
		if err != nil {
			continue
		}
		realAllowed, err := filepath.EvalSymlinks(absAllowed)
		if err == nil {
			absAllowed = realAllowed
		}
		if isWithinRoot(candidate, absAllowed) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("read path is outside allowed roots: %s", inputPath)
}

// SandboxToHostPath 将沙箱路径转换为宿主机路径（输入方向：LLM → 宿主机）
// 例如 /workspace/foo.go → /data/.xbot/users/xxx/workspace/foo.go
func SandboxToHostPath(ctx *ToolContext, sandboxPath string) string {
	if ctx == nil || !ctx.SandboxEnabled || ctx.SandboxWorkDir == "" || ctx.WorkspaceRoot == "" {
		return sandboxPath
	}
	if ctx.SandboxWorkDir == ctx.WorkspaceRoot {
		return sandboxPath
	}
	if !strings.HasPrefix(sandboxPath, ctx.SandboxWorkDir) {
		return sandboxPath
	}
	rel := strings.TrimPrefix(sandboxPath, ctx.SandboxWorkDir)
	rel = strings.TrimPrefix(rel, string(filepath.Separator))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return ctx.WorkspaceRoot
	}
	return filepath.Join(ctx.WorkspaceRoot, rel)
}

// HostToSandboxPath 将宿主机路径转换为沙箱路径（输出方向：宿主机 → LLM）
// 例如 /data/.xbot/users/xxx/workspace/foo.go → /workspace/foo.go
func HostToSandboxPath(ctx *ToolContext, hostPath string) string {
	if ctx == nil || !ctx.SandboxEnabled || ctx.SandboxWorkDir == "" || ctx.WorkspaceRoot == "" {
		return hostPath
	}
	if ctx.SandboxWorkDir == ctx.WorkspaceRoot {
		return hostPath
	}
	if !strings.HasPrefix(hostPath, ctx.WorkspaceRoot) {
		return hostPath
	}
	rel := strings.TrimPrefix(hostPath, ctx.WorkspaceRoot)
	rel = strings.TrimPrefix(rel, string(filepath.Separator))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return ctx.SandboxWorkDir
	}
	return filepath.Join(ctx.SandboxWorkDir, rel)
}

// sandboxBaseDir 返回沙箱内的工作目录前缀。
// 优先使用 ctx.SandboxWorkDir，兜底为 "/workspace"。
func sandboxBaseDir(ctx *ToolContext) string {
	if ctx != nil && ctx.SandboxWorkDir != "" {
		return ctx.SandboxWorkDir
	}
	return "/workspace"
}

// shellEscape 对字符串进行 shell 单引号转义，防止命令注入。
// 将字符串中的单引号替换为 '\”（结束单引号、转义单引号、开始新单引号）。
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
