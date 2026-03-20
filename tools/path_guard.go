package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolvePath 解析路径为绝对路径，不做任何权限/范围检查。
func resolvePath(ctx *ToolContext, inputPath string) (string, error) {
	if inputPath == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(inputPath) {
		return cleanAbsPath(inputPath)
	}
	// 解析相对路径：优先使用 WorkspaceRoot 作为基准目录
	base := ""
	if ctx != nil && ctx.WorkspaceRoot != "" {
		base = ctx.WorkspaceRoot
	} else if ctx != nil && ctx.WorkingDir != "" {
		base = ctx.WorkingDir
	}
	if base == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		base = cwd
	}
	return cleanAbsPath(filepath.Join(base, inputPath))
}

// ResolveWritePath 解析写入路径为绝对路径（仅做路径解析，不做权限检查）。
// 保留函数签名以兼容已有调用方。
func ResolveWritePath(ctx *ToolContext, inputPath string) (string, error) {
	return resolvePath(ctx, inputPath)
}

// ResolveReadPath 解析读取路径为绝对路径（仅做路径解析，不做权限检查）。
// 保留函数签名以兼容已有调用方。
func ResolveReadPath(ctx *ToolContext, inputPath string) (string, error) {
	return resolvePath(ctx, inputPath)
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
// 将字符串中的单引号替换为 '\''（结束单引号、转义单引号、开始新单引号）。
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
