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
	// 沙箱模式下，xbot 运行在容器内，应以容器内可见的 SandboxWorkDir 为校验根
	if ctx.SandboxEnabled && ctx.SandboxWorkDir != "" {
		return ctx.SandboxWorkDir
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

	if ctx == nil || (ctx.WorkspaceRoot == "" && ctx.WorkingDir == "" && len(ctx.ReadOnlyRoots) == 0 && !ctx.SandboxEnabled) {
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
		// 优先使用 CurrentDir（Cd 设置的当前目录），否则 fallback 到 root
		if ctx != nil && ctx.CurrentDir != "" {
			candidate = filepath.Join(ctx.CurrentDir, candidate)
		} else {
			candidate = filepath.Join(root, candidate)
		}
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

	if ctx == nil || (ctx.WorkspaceRoot == "" && ctx.WorkingDir == "" && len(ctx.ReadOnlyRoots) == 0 && !ctx.SandboxEnabled) {
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
		// 优先使用 CurrentDir（Cd 设置的当前目录），否则 fallback 到 root
		if ctx != nil && ctx.CurrentDir != "" {
			candidate = filepath.Join(ctx.CurrentDir, candidate)
		} else {
			candidate = filepath.Join(root, candidate)
		}
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
// 返回 ctx.SandboxWorkDir（docker 模式下通常为 "/workspace"）。
// 返回空字符串表示无沙箱路径约束（none 模式），调用方应跳过路径校验。
func sandboxBaseDir(ctx *ToolContext) string {
	if ctx != nil {
		return ctx.SandboxWorkDir
	}
	return ""
}

// resolveSandboxCWD 将 CurrentDir 解析为沙箱内的绝对路径。
// 支持两种格式：
//   - 沙箱路径（如 /workspace/src）→ 直接返回
//   - 宿主机路径（如 /data/users/ou_xxx/workspace/src）→ 转换为沙箱路径
//
// 返回空字符串表示无法解析（CurrentDir 为空或不在已知根目录下）。
func resolveSandboxCWD(ctx *ToolContext, sandboxBase string) string {
	if ctx == nil || ctx.CurrentDir == "" {
		return ""
	}
	if ctx.CurrentDir == sandboxBase || strings.HasPrefix(ctx.CurrentDir, sandboxBase+"/") {
		return ctx.CurrentDir
	}
	if ctx.WorkspaceRoot != "" && strings.HasPrefix(ctx.CurrentDir, ctx.WorkspaceRoot) {
		rel, err := filepath.Rel(ctx.WorkspaceRoot, ctx.CurrentDir)
		if err == nil {
			if rel == "." {
				return sandboxBase
			}
			return filepath.Join(sandboxBase, rel)
		}
	}
	return ""
}

// shellEscape 对字符串进行 shell 单引号转义，防止命令注入。
// 将字符串中的单引号替换为 '\”（结束单引号、转义单引号、开始新单引号）。
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
