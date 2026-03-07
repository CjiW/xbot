package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "xbot/logger"
	"xbot/tools"
)

// AgentStore scans agent directories and generates a catalog for the system prompt.
// Agents are predefined SubAgent roles loaded from .xbot/agents/*.md files.
type AgentStore struct {
	dir string // agents root directory (e.g. {WorkDir}/.xbot/agents)
}

// NewAgentStore creates an AgentStore
func NewAgentStore(dir string) *AgentStore {
	return &AgentStore{dir: dir}
}

// GetAgentsCatalog returns a formatted catalog of all available agents for the system prompt.
// This is dynamically generated on each call so new agent files are picked up without restart.
func (s *AgentStore) GetAgentsCatalog() string {
	if _, err := os.Stat(s.dir); os.IsNotExist(err) {
		return ""
	}

	roles, err := tools.LoadAgentRoles(s.dir)
	if err != nil {
		log.WithError(err).Warn("Failed to load agent roles for catalog")
		return ""
	}
	if len(roles) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<available_agents>\n")
	for _, r := range roles {
		toolsInfo := ""
		if len(r.AllowedTools) > 0 {
			toolsInfo = strings.Join(r.AllowedTools, ", ")
		}
		location := filepath.Join(s.dir, r.Name+".md")
		fmt.Fprintf(&sb, "  <agent>\n    <name>%s</name>\n    <description>%s</description>\n    <tools>%s</tools>\n    <location>%s</location>\n  </agent>\n",
			r.Name, r.Description, toolsInfo, location)
	}
	sb.WriteString("</available_agents>\n")
	return sb.String()
}
