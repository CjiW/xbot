package agent

import (
	"fmt"
	"os"
	"sort"
	"strings"

	log "xbot/logger"
	"xbot/tools"
)

// AgentStore scans agent directories and generates a catalog for the system prompt.
// Agents are predefined SubAgent roles loaded from .xbot/agents/*.md files.
// Supports global + user-private merge strategy (like SkillStore).
type AgentStore struct {
	globalDir string // 全局 agents 目录 (e.g. {WorkDir}/.xbot/agents)
	workDir   string // 用于派生用户私有 agents 目录
}

// NewAgentStore creates an AgentStore
func NewAgentStore(workDir string, globalDir string) *AgentStore {
	return &AgentStore{workDir: workDir, globalDir: globalDir}
}

// GetAgentsCatalog returns a formatted catalog of all available agents for the system prompt.
// Scans global agents first, then user-private agents; same-name agents are overridden by user version.
func (s *AgentStore) GetAgentsCatalog(senderID string) string {
	sources := []string{s.globalDir}
	if senderID != "" {
		sources = append(sources, tools.UserAgentsRoot(s.workDir, senderID))
	}

	type agentInfo struct {
		name string
		role tools.SubAgentRole
	}

	merged := make(map[string]agentInfo)
	var orderedNames []string

	for _, dir := range sources {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		roles, err := tools.LoadAgentRoles(dir)
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
