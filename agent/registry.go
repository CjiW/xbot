package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	log "xbot/logger"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// RegistryManager manages skill/agent publishing, installation, and discovery.
type RegistryManager struct {
	store       *SkillStore
	agentStore  *AgentStore
	sharedStore *sqlite.SharedSkillRegistry
	workDir     string
}

// NewRegistryManager creates a new RegistryManager.
func NewRegistryManager(store *SkillStore, agentStore *AgentStore, sharedStore *sqlite.SharedSkillRegistry, workDir string) *RegistryManager {
	return &RegistryManager{
		store:       store,
		agentStore:  agentStore,
		sharedStore: sharedStore,
		workDir:     workDir,
	}
}

// Publish publishes a skill or agent to the shared registry.
func (rm *RegistryManager) Publish(entryType, name, author string) error {
	switch entryType {
	case "skill":
		return rm.publishSkill(name, author)
	case "agent":
		return rm.publishAgent(name, author)
	default:
		return fmt.Errorf("unknown type %q, must be 'skill' or 'agent'", entryType)
	}
}

func (rm *RegistryManager) publishSkill(name, author string) error {
	// Find skill directory (search global + user dirs)
	skillDir := rm.findSkillDir(name)
	if skillDir == "" {
		return fmt.Errorf("skill %q not found", name)
	}

	info := parseSkillFrontmatterV2(skillDir)
	if info.Author != "" && info.Author != author {
		return fmt.Errorf("skill %q is owned by %q, cannot publish as %q", name, info.Author, author)
	}
	if info.Author == "" {
		info.Author = author
	}

	entry := &sqlite.SharedEntry{
		Type:        "skill",
		Name:        info.Name,
		Description: info.Description,
		Author:      info.Author,
		Tags:        info.Tags,
		SourcePath:  skillDir,
		Sharing:     "public",
	}

	return rm.sharedStore.Publish(entry)
}

func (rm *RegistryManager) publishAgent(name, author string) error {
	// Find agent role file
	agentDir := rm.findAgentDir(name)
	if agentDir == "" {
		return fmt.Errorf("agent %q not found", name)
	}

	// Parse agent role file for description
	roles, err := tools.LoadAgentRoles(agentDir)
	if err != nil || len(roles) == 0 {
		return fmt.Errorf("failed to load agent %q: %v", name, err)
	}

	role := roles[0]
	entry := &sqlite.SharedEntry{
		Type:        "agent",
		Name:        role.Name,
		Description: role.Description,
		Author:      author,
		SourcePath:  agentDir,
		Sharing:     "public",
	}

	return rm.sharedStore.Publish(entry)
}

// Unpublish removes a skill/agent from the shared registry.
func (rm *RegistryManager) Unpublish(entryType, name, author string) error {
	entries, err := rm.sharedStore.ListByAuthor(author)
	if err != nil {
		return fmt.Errorf("list entries: %w", err)
	}

	for _, e := range entries {
		if e.Type == entryType && e.Name == name {
			return rm.sharedStore.Unpublish(e.ID, author)
		}
	}
	return fmt.Errorf("%s %q not found in your published entries", entryType, name)
}

// Install installs a shared skill/agent by ID to the user's private directory.
func (rm *RegistryManager) Install(entryType string, id int64, senderID string) error {
	entry, err := rm.sharedStore.GetByID(id)
	if err != nil {
		return fmt.Errorf("get entry: %w", err)
	}
	if entry == nil {
		return fmt.Errorf("entry with ID %d not found", id)
	}
	if entry.Type != entryType {
		return fmt.Errorf("entry %d is type %q, not %q", id, entry.Type, entryType)
	}

	var destDir string
	switch entryType {
	case "skill":
		destDir = filepath.Join(tools.UserSkillsRoot(rm.workDir, senderID), entry.Name)
	case "agent":
		destDir = filepath.Join(tools.UserAgentsRoot(rm.workDir, senderID), entry.Name)
	default:
		return fmt.Errorf("unknown type %q", entryType)
	}

	// Don't overwrite existing
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("%s %q already installed", entryType, entry.Name)
	}

	// Copy directory recursively
	if err := copyDir(entry.SourcePath, destDir); err != nil {
		return fmt.Errorf("copy %s: %w", entryType, err)
	}

	// Write installed metadata marker
	info := struct {
		InstalledFrom string `json:"installed_from"`
		InstalledAt   int64  `json:"installed_at"`
	}{
		InstalledFrom: fmt.Sprintf("registry:%d", id),
		InstalledAt:   time.Now().UnixMilli(),
	}
	// For skills, update SKILL.md frontmatter if exists
	if entryType == "skill" {
		rm.markInstalled(destDir, info.InstalledFrom, info.InstalledAt)
	}

	log.WithFields(log.Fields{
		"type":   entryType,
		"name":   entry.Name,
		"sender": senderID,
		"from":   entry.SourcePath,
		"to":     destDir,
	}).Info("Installed from registry")
	return nil
}

// Uninstall removes a user-installed skill/agent.
func (rm *RegistryManager) Uninstall(entryType, name, senderID string) error {
	var dir string
	switch entryType {
	case "skill":
		dir = filepath.Join(tools.UserSkillsRoot(rm.workDir, senderID), name)
	case "agent":
		dir = filepath.Join(tools.UserAgentsRoot(rm.workDir, senderID), name)
	default:
		return fmt.Errorf("unknown type %q", entryType)
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("%s %q is not installed", entryType, name)
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", entryType, err)
	}

	log.WithFields(log.Fields{
		"type":   entryType,
		"name":   name,
		"sender": senderID,
	}).Info("Uninstalled")
	return nil
}

// Search searches the shared registry.
func (rm *RegistryManager) Search(query, entryType string, limit int) ([]sqlite.SharedEntry, error) {
	if query == "" {
		return rm.sharedStore.ListShared(entryType, limit, 0)
	}
	return rm.sharedStore.SearchShared(query, entryType, limit)
}

// ListMy lists the user's own published and installed entries.
func (rm *RegistryManager) ListMy(senderID string, entryType string) (published []sqlite.SharedEntry, installed []string, err error) {
	// Published
	published, err = rm.sharedStore.ListByAuthor(senderID)
	if err != nil {
		return nil, nil, err
	}

	// Filter by type if specified
	if entryType != "" {
		var filtered []sqlite.SharedEntry
		for _, e := range published {
			if e.Type == entryType {
				filtered = append(filtered, e)
			}
		}
		published = filtered
	}

	// Installed: scan user's private directories
	if entryType == "" || entryType == "skill" {
		skillsDir := tools.UserSkillsRoot(rm.workDir, senderID)
		if entries, e := os.ReadDir(skillsDir); e == nil {
			for _, ent := range entries {
				if ent.IsDir() {
					installed = append(installed, "skill:"+ent.Name())
				}
			}
		}
	}
	if entryType == "" || entryType == "agent" {
		agentsDir := tools.UserAgentsRoot(rm.workDir, senderID)
		if entries, e := os.ReadDir(agentsDir); e == nil {
			for _, ent := range entries {
				if ent.IsDir() {
					installed = append(installed, "agent:"+ent.Name())
				}
			}
		}
	}

	return published, installed, nil
}

// Browse lists public entries in the marketplace.
func (rm *RegistryManager) Browse(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
	return rm.sharedStore.ListShared(entryType, limit, offset)
}

// --- helpers ---

func (rm *RegistryManager) findSkillDir(name string) string {
	// Search global dirs
	for _, dir := range rm.store.globalDirs {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
			return path
		}
	}
	return ""
}

func (rm *RegistryManager) findAgentDir(name string) string {
	// Search global agents dir
	if rm.agentStore != nil && rm.agentStore.globalDir != "" {
		path := filepath.Join(rm.agentStore.globalDir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func (rm *RegistryManager) markInstalled(skillDir, installedFrom string, installedAt int64) {
	// Placeholder for writing install metadata.
	// Currently a no-op to avoid breaking SKILL.md YAML parsing.
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(targetPath, data, 0o644)
	})
}
