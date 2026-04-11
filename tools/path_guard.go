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
	// Remote sandbox: the runner handles its own path enforcement.
	// The server doesn't have the runner's filesystem, so skip checks.
	if ctx.Sandbox != nil && ctx.Sandbox.Name() == "remote" {
		return ""
	}
	if ctx.Sandbox != nil && ctx.Sandbox.Name() != "none" {
		return ctx.Sandbox.Workspace(ctx.OriginUserID)
	}
	if ctx.WorkspaceRoot != "" {
		return ctx.WorkspaceRoot
	}
	return ctx.WorkingDir
}

func resolveScopedBase(ctx *ToolContext) (string, error) {
	root := defaultWorkspaceRoot(ctx)
	if root == "" {
		cwd, err := effectiveCWD(ctx)
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

// effectiveCWD returns the current working directory for path resolution.
// Priority: ctx.CurrentDir (set by Cd tool) > effectiveCWD(ctx) (process CWD).
//
// Bug fix: In unrestricted mode (none/remote sandbox), all path resolution
// functions used os.Getwd() which returns the OS process CWD, not the
// virtual CWD set by the Cd tool. The Cd tool only sets ctx.CurrentDir
// (session-persisted), it does NOT call os.Chdir(). This caused relative
// paths in Read/FileCreate/FileReplace/Grep/Glob to resolve against the
// wrong directory after a Cd.
func effectiveCWD(ctx *ToolContext) (string, error) {
	if ctx != nil && ctx.CurrentDir != "" {
		return ctx.CurrentDir, nil
	}
	return os.Getwd()
}

// isUnrestricted returns true when path restrictions should be skipped.
// None sandbox: user has full filesystem access.
// Remote sandbox: the runner handles its own path enforcement.
func isUnrestricted(ctx *ToolContext) bool {
	if ctx == nil || ctx.Sandbox == nil {
		return false
	}
	name := ctx.Sandbox.Name()
	return name == "none" || name == "remote"
}

// ResolveWritePath 将 inputPath 解析为绝对路径，并校验其在 workspace 写入范围内。
//
// 相对路径解析优先级：CurrentDir（Cd 设置）> WorkspaceRoot/WorkingDir。
// 绝对路径直接校验，不受 CurrentDir 影响。
func ResolveWritePath(ctx *ToolContext, inputPath string) (string, error) {
	if inputPath == "" {
		return "", fmt.Errorf("path is required")
	}

	// Unrestricted mode (none/remote sandbox): skip path checks.
	if isUnrestricted(ctx) {
		if filepath.IsAbs(inputPath) {
			return cleanAbsPath(inputPath)
		}
		cwd, err := effectiveCWD(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		return cleanAbsPath(filepath.Join(cwd, inputPath))
	}

	if ctx == nil || (ctx.WorkspaceRoot == "" && ctx.WorkingDir == "" && len(ctx.ReadOnlyRoots) == 0 && !ctx.SandboxEnabled) {
		if filepath.IsAbs(inputPath) {
			return cleanAbsPath(inputPath)
		}
		cwd, err := effectiveCWD(ctx)
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
		// Fallback: compare without EvalSymlinks (handles Windows short paths
		// where intermediate directories don't exist yet)
		if isWithinRoot(candidate, root) {
			return candidate, nil
		}
		return "", fmt.Errorf("write path escapes workspace: %s", inputPath)
	}
	return candidate, nil
}

// ResolveReadPath 将 inputPath 解析为绝对路径，并校验其在允许的读取范围内。
//
// 相对路径解析优先级：CurrentDir（Cd 设置）> WorkspaceRoot/WorkingDir。
// 绝对路径直接校验，不受 CurrentDir 影响。
// 允许读取范围包括 workspace root 及 ReadOnlyRoots 中列出的目录。
func ResolveReadPath(ctx *ToolContext, inputPath string) (string, error) {
	if inputPath == "" {
		return "", fmt.Errorf("path is required")
	}

	// Unrestricted mode (none/remote sandbox): skip path checks.
	if isUnrestricted(ctx) {
		if filepath.IsAbs(inputPath) {
			return cleanAbsPath(inputPath)
		}
		cwd, err := effectiveCWD(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		return cleanAbsPath(filepath.Join(cwd, inputPath))
	}

	if ctx == nil || (ctx.WorkspaceRoot == "" && ctx.WorkingDir == "" && len(ctx.ReadOnlyRoots) == 0 && !ctx.SandboxEnabled) {
		if filepath.IsAbs(inputPath) {
			return cleanAbsPath(inputPath)
		}
		cwd, err := effectiveCWD(ctx)
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
		// Fallback: compare without EvalSymlinks (handles Windows short/long
		// path name mismatches when file doesn't exist yet)
		if isWithinRoot(candidate, allowed) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("read path is outside allowed roots: %s", inputPath)
}

// sandboxBaseDir 返回沙箱内的工作目录前缀。
// 返回 Sandbox.Workspace(userID)（docker 模式下通常为 "/workspace"，remote 模式为 runner workspace）。
// 返回空字符串表示无沙箱路径约束（none 模式），调用方应跳过路径校验。
func sandboxBaseDir(ctx *ToolContext) string {
	if ctx != nil && ctx.Sandbox != nil && ctx.Sandbox.Name() != "none" {
		return ctx.Sandbox.Workspace(ctx.OriginUserID)
	}
	return ""
}

// ShouldUseSandbox 判断是否应使用 Sandbox 访问文件系统。
// 仅在 Sandbox 可用且非 none 模式时返回 true。
func ShouldUseSandbox(ctx *ToolContext) bool {
	return ctx != nil && ctx.Sandbox != nil && ctx.Sandbox.Name() != "none"
}

// shouldUseSandbox is the unexported alias used within the tools package.
func shouldUseSandbox(ctx *ToolContext) bool {
	return ShouldUseSandbox(ctx)
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
	// Normalize separators for cross-platform comparison
	currentDir := filepath.ToSlash(ctx.CurrentDir)
	sandboxBaseSlash := filepath.ToSlash(sandboxBase)
	if currentDir == sandboxBaseSlash || strings.HasPrefix(currentDir, sandboxBaseSlash+"/") {
		return ctx.CurrentDir
	}
	if ctx.WorkspaceRoot != "" {
		wsRoot := filepath.ToSlash(ctx.WorkspaceRoot)
		if strings.HasPrefix(currentDir, wsRoot) {
			rel, err := filepath.Rel(ctx.WorkspaceRoot, ctx.CurrentDir)
			if err == nil {
				if rel == "." {
					return sandboxBase
				}
				return filepath.Join(sandboxBase, rel)
			}
		}
	}
	return ""
}

// shellEscape 对字符串进行 shell 单引号转义，防止命令注入。
// 将字符串中的单引号替换为 '\”（结束单引号、转义单引号、开始新单引号）。
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
