package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var nonSafeSegment = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// SanitizeWorkspaceKey 清理用户维度标识，避免路径注入。
func SanitizeWorkspaceKey(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "anonymous"
	}
	sanitized := nonSafeSegment.ReplaceAllString(trimmed, "_")
	sanitized = strings.Trim(sanitized, "._-")
	if sanitized == "" {
		return "anonymous"
	}
	return sanitized
}

// UserRoot 返回用户根目录：{workDir}/.xbot/users/{sender}
func UserRoot(workDir, senderID string) string {
	return filepath.Join(workDir, ".xbot", "users", SanitizeWorkspaceKey(senderID))
}

// UserWorkspaceRoot 返回用户工作区目录：{workDir}/.xbot/users/{sender}/workspace
func UserWorkspaceRoot(workDir, senderID string) string {
	return filepath.Join(UserRoot(workDir, senderID), "workspace")
}

// UserSkillsRoot 返回用户私有 skill 目录：{workDir}/.xbot/users/{sender}/workspace/skills
func UserSkillsRoot(workDir, senderID string) string {
	return filepath.Join(UserWorkspaceRoot(workDir, senderID), "skills")
}

// UserMCPConfigPath 返回用户 MCP 配置路径：{workDir}/.xbot/users/{sender}/mcp.json
func UserMCPConfigPath(workDir, senderID string) string {
	return filepath.Join(UserRoot(workDir, senderID), "mcp.json")
}

// UserAgentsRoot returns the user-private agents directory.
func UserAgentsRoot(workDir, senderID string) string {
	return filepath.Join(UserWorkspaceRoot(workDir, senderID), "agents")
}

func cleanAbsPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	return abs, nil
}
