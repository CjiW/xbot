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
// Returns error if a public entry with the same type+name already exists from a different author.
func (rm *RegistryManager) Publish(entryType, name, author string) error {
	// Dedup: reject if same type+name already public by another author
	existing, err := rm.sharedStore.GetByTypeAndName(entryType, name)
	if err != nil {
		return fmt.Errorf("dedup check: %w", err)
	}
	if existing != nil && existing.Author != author && existing.Sharing == "public" {
		return fmt.Errorf("%s %q 已被 %s 发布，不能重名分享", entryType, name, existing.Author)
	}

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
	skillDir := rm.findSkillDirForUser(name, author)
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

	cacheDir := rm.registryCacheDir("skill", info.Name)
	if err := rm.snapshotToCache(skillDir, cacheDir); err != nil {
		return fmt.Errorf("snapshot skill: %w", err)
	}

	entry := &sqlite.SharedEntry{
		Type:        "skill",
		Name:        info.Name,
		Description: info.Description,
		Author:      info.Author,
		Tags:        info.Tags,
		SourcePath:  cacheDir,
		Sharing:     "public",
	}

	return rm.sharedStore.Publish(entry)
}

func (rm *RegistryManager) publishAgent(name, author string) error {
	agentDir := rm.findAgentDirForUser(name, author)
	if agentDir == "" {
		return fmt.Errorf("agent %q not found", name)
	}

	roles, err := tools.LoadAgentRoles(agentDir)
	if err != nil || len(roles) == 0 {
		return fmt.Errorf("failed to load agent %q: %v", name, err)
	}

	role := roles[0]

	cacheDir := rm.registryCacheDir("agent", role.Name)
	if err := rm.snapshotToCache(agentDir, cacheDir); err != nil {
		return fmt.Errorf("snapshot agent: %w", err)
	}

	entry := &sqlite.SharedEntry{
		Type:        "agent",
		Name:        role.Name,
		Description: role.Description,
		Author:      author,
		SourcePath:  cacheDir,
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

	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("%s %q already installed", entryType, entry.Name)
	}

	if _, err := os.Stat(entry.SourcePath); os.IsNotExist(err) {
		return fmt.Errorf("%s %q 的源文件已不存在（可能已被删除或改名），请联系发布者重新发布", entryType, entry.Name)
	}

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

// ListMy lists the user's own published entries and all locally available items
// (global + user-private directories).
func (rm *RegistryManager) ListMy(senderID string, entryType string) (published []sqlite.SharedEntry, local []string, err error) {
	// Published by this user
	published, err = rm.sharedStore.ListByAuthor(senderID)
	if err != nil {
		return nil, nil, err
	}

	if entryType != "" {
		var filtered []sqlite.SharedEntry
		for _, e := range published {
			if e.Type == entryType {
				filtered = append(filtered, e)
			}
		}
		published = filtered
	}

	seen := make(map[string]bool)

	// Skills: global dirs + user-private dir
	if entryType == "" || entryType == "skill" {
		for _, dir := range rm.store.globalDirs {
			scanSkillDir(dir, &local, seen)
		}
		scanSkillDir(tools.UserSkillsRoot(rm.workDir, senderID), &local, seen)
	}

	// Agents: global dir + user-private dir
	if entryType == "" || entryType == "agent" {
		if rm.agentStore != nil && rm.agentStore.globalDir != "" {
			scanAgentDir(rm.agentStore.globalDir, &local, seen)
		}
		scanAgentDir(tools.UserAgentsRoot(rm.workDir, senderID), &local, seen)
	}

	return published, local, nil
}

func scanSkillDir(dir string, out *[]string, seen map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		key := "skill:" + ent.Name()
		if seen[key] {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, ent.Name(), "SKILL.md")); err == nil {
			seen[key] = true
			*out = append(*out, key)
		}
	}
}

func scanAgentDir(dir string, out *[]string, seen map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		key := "agent:" + ent.Name()
		if seen[key] {
			continue
		}
		seen[key] = true
		*out = append(*out, key)
	}
}

// Browse lists public entries in the marketplace.
func (rm *RegistryManager) Browse(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
	return rm.sharedStore.ListShared(entryType, limit, offset)
}

// --- registry cache ---

// registryCacheDir returns the snapshot directory for a published entry.
func (rm *RegistryManager) registryCacheDir(entryType, name string) string {
	return filepath.Join(rm.workDir, ".xbot", "registry", entryType, name)
}

// snapshotToCache copies src directory into cacheDir, replacing any existing cache.
func (rm *RegistryManager) snapshotToCache(src, cacheDir string) error {
	if err := os.RemoveAll(cacheDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clean cache: %w", err)
	}
	return copyDir(src, cacheDir)
}

// --- helpers ---

func (rm *RegistryManager) findSkillDir(name string) string {
	for _, dir := range rm.store.globalDirs {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
			return path
		}
	}
	return ""
}

// findSkillDirForUser searches global + user-private skill dirs.
func (rm *RegistryManager) findSkillDirForUser(name, senderID string) string {
	if dir := rm.findSkillDir(name); dir != "" {
		return dir
	}
	if senderID != "" {
		path := filepath.Join(tools.UserSkillsRoot(rm.workDir, senderID), name)
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
			return path
		}
	}
	return ""
}

func (rm *RegistryManager) findAgentDir(name string) string {
	if rm.agentStore != nil && rm.agentStore.globalDir != "" {
		path := filepath.Join(rm.agentStore.globalDir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// findAgentDirForUser searches global + user-private agent dirs.
func (rm *RegistryManager) findAgentDirForUser(name, senderID string) string {
	if dir := rm.findAgentDir(name); dir != "" {
		return dir
	}
	if senderID != "" {
		path := filepath.Join(tools.UserAgentsRoot(rm.workDir, senderID), name)
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
