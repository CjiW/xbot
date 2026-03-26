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
	globalDir      string
	workDir        string
	sandbox        tools.Sandbox
	sandboxWorkDir string
}

// NewAgentStore creates an AgentStore
func NewAgentStore(workDir string, globalDir string, sandbox tools.Sandbox, sandboxWorkDir string) *AgentStore {
	return &AgentStore{workDir: workDir, globalDir: globalDir, sandbox: sandbox, sandboxWorkDir: sandboxWorkDir}
}

// userAgentsDir 返回用户 agent 目录路径（沙箱感知）
func (s *AgentStore) userAgentsDir(senderID string) string {
	if s.sandbox != nil && s.sandboxWorkDir != "" {
		return filepath.Join(s.sandboxWorkDir, "agents")
	}
	return tools.UserAgentsRoot(s.workDir, senderID)
}

// GetAgentsCatalog returns a formatted catalog of all available agents for the system prompt.
// Scans global agents first, then user-private agents; same-name agents are overridden by user version.
func (s *AgentStore) GetAgentsCatalog(ctx context.Context, senderID string) string {
	sources := []string{s.globalDir}
	if senderID != "" {
		sources = append(sources, s.userAgentsDir(senderID))
	}

	type agentInfo struct {
		name string
		role tools.SubAgentRole
	}

	merged := make(map[string]agentInfo)
	var orderedNames []string

	for i, dir := range sources {
		// Sandbox-aware existence check: use sandbox.Stat for sandbox paths, os.Stat for host paths.
		if i == 0 || (s.sandbox == nil || s.sandboxWorkDir == "") {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				continue
			}
		} else {
			if _, err := s.sandbox.Stat(ctx, dir, senderID); err != nil {
				continue
			}
		}

		// Sandbox-aware loading for user directories
		var roles []tools.SubAgentRole
		var err error
		if i == 0 || (s.sandbox == nil || s.sandboxWorkDir == "") {
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
