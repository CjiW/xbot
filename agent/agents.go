package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	log "xbot/logger"
	"xbot/tools"
)

// AgentStore scans agent directories and generates a catalog for the system prompt.
type AgentStore struct {
	globalDir string
	workDir   string
	sandbox   tools.Sandbox
}

// NewAgentStore creates an AgentStore
func NewAgentStore(workDir string, globalDir string, sandbox tools.Sandbox) *AgentStore {
	return &AgentStore{workDir: workDir, globalDir: globalDir, sandbox: sandbox}
}

// userAgentsDir 返回用户 agent 目录路径（沙箱感知）
func (s *AgentStore) userAgentsDir(senderID string) string {
	if s.sandbox != nil && s.sandbox.Name() != "none" {
		return filepath.Join(s.sandbox.Workspace(senderID), "agents")
	}
	return tools.UserAgentsRoot(s.workDir, senderID)
}

// GetAgentsCatalog returns a formatted catalog of all available agents for the system prompt.
// Scans embedded agents first, then global agents, then user-private agents;
// same-name agents are overridden by later sources (user > global > embedded).
func (s *AgentStore) GetAgentsCatalog(ctx context.Context, senderID string) string {
	type agentInfo struct {
		name string
		role tools.SubAgentRole
	}

	merged := make(map[string]agentInfo)
	var orderedNames []string

	// 1. 扫描内置嵌入的 agents（优先级最低，外部同名 agent 会覆盖）
	for _, name := range tools.ListEmbeddedAgents() {
		data, err := tools.ReadEmbeddedAgentFile(name)
		if err != nil {
			continue
		}
		role, err := tools.ParseAgentFileContent(data, name)
		if err != nil || role.Name == "" {
			continue
		}
		orderedNames = append(orderedNames, role.Name)
		merged[role.Name] = agentInfo{name: role.Name, role: role}
	}

	// 2. 扫描全局目录 + 用户目录
	sources := []string{s.globalDir}
	if senderID != "" {
		sources = append(sources, s.userAgentsDir(senderID))
	}

	for i, dir := range sources {
		// Sandbox-aware existence check
		if i == 0 || (s.sandbox == nil || s.sandbox.Name() == "none") {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				continue
			}
		} else {
			if _, err := s.sandbox.Stat(ctx, dir, senderID); err != nil {
				continue
			}
		}

		var roles []tools.SubAgentRole
		var err error
		if i == 0 || (s.sandbox == nil || s.sandbox.Name() == "none") {
			roles, err = tools.LoadAgentRoles(dir)
		} else {
			roles, err = tools.LoadAgentRolesSandbox(ctx, dir, s.sandbox, senderID)
		}
		if err != nil {
			log.WithError(err).Warn("Failed to load agent roles for catalog")
			continue
		}

		for _, r := range roles {
			if _, exists := merged[r.Name]; !exists {
				orderedNames = append(orderedNames, r.Name)
			}
			merged[r.Name] = agentInfo{
				name: r.Name,
				role: r,
			}
		}
	}

	if len(merged) == 0 {
		return ""
	}

	sort.Strings(orderedNames)

	var sb strings.Builder
	sb.WriteString("# Available Agents (SubAgents)\n\n")
	sb.WriteString("SubAgent 是拥有独立工具集和上下文的子代理，可委托专门任务并行处理。用 `SubAgent` 工具调用。\n\n")

	// 注入实际目录路径，供 agent-creator 等参考
	if s.globalDir != "" {
		fmt.Fprintf(&sb, "**Agents 存储目录**: %s\n\n", s.globalDir)
	}

	sb.WriteString("<available_agents>\n")
	for _, name := range orderedNames {
		info := merged[name]
		toolsInfo := ""
		if len(info.role.AllowedTools) > 0 {
			toolsInfo = strings.Join(info.role.AllowedTools, ", ")
		}
		fmt.Fprintf(&sb, "  <agent>\n    <name>%s</name>\n    <description>%s</description>\n    <tools>%s</tools>\n  </agent>\n",
			info.role.Name, info.role.Description, toolsInfo)
	}
	sb.WriteString("</available_agents>\n")
	return sb.String()
}
