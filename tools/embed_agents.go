package tools

import (
	"embed"
	"io/fs"
	"path/filepath"
)

// EmbeddedAgents contains agent definitions built into the binary.
//
//go:embed embed_agents/*
var EmbeddedAgents embed.FS

// ListEmbeddedAgents returns names of all embedded agents (file names under embed_agents/).
func ListEmbeddedAgents() []string {
	entries, err := fs.ReadDir(EmbeddedAgents, "embed_agents")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			// Strip .md extension
			name := e.Name()
			if len(name) > 3 && filepath.Ext(name) == ".md" {
				name = name[:len(name)-3]
			}
			names = append(names, name)
		}
	}
	return names
}

// ReadEmbeddedAgentFile reads an embedded agent definition file.
// agentName is the agent name without .md extension (e.g., "explore").
// Returns nil if the agent doesn't exist.
func ReadEmbeddedAgentFile(agentName string) ([]byte, error) {
	path := filepath.Join("embed_agents", agentName+".md")
	return EmbeddedAgents.ReadFile(path)
}

// HasEmbeddedAgent checks if an agent with the given name exists in the embedded FS.
func HasEmbeddedAgent(name string) bool {
	for _, n := range ListEmbeddedAgents() {
		if n == name {
			return true
		}
	}
	return false
}
