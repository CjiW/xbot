package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
	if err := rm.snapshotDirToCache(skillDir, cacheDir); err != nil {
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

// publishAgent finds a single agent .md file, snapshots it to cache, and publishes.
func (rm *RegistryManager) publishAgent(name, author string) error {
	agentFile := rm.findAgentFile(name, author)
	if agentFile == "" {
		return fmt.Errorf("agent %q not found", name)
	}

	role, err := tools.ParseAgentFile(agentFile)
	if err != nil {
		return fmt.Errorf("parse agent %q: %v", name, err)
	}

	cacheDir := rm.registryCacheDir("agent", role.Name)
	if err := rm.snapshotFileToCache(agentFile, cacheDir); err != nil {
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

	if _, err := os.Stat(entry.SourcePath); os.IsNotExist(err) {
		return fmt.Errorf("%s %q 的源文件已不存在，请联系发布者重新发布", entryType, entry.Name)
	}

	switch entryType {
	case "skill":
		return rm.installSkill(entry, senderID)
	case "agent":
		return rm.installAgent(entry, senderID)
	default:
		return fmt.Errorf("unknown type %q", entryType)
	}
}

func (rm *RegistryManager) installSkill(entry *sqlite.SharedEntry, senderID string) error {
	destDir := filepath.Join(tools.UserSkillsRoot(rm.workDir, senderID), entry.Name)
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("skill %q already installed", entry.Name)
	}

	if err := copyDir(entry.SourcePath, destDir); err != nil {
		return fmt.Errorf("copy skill: %w", err)
	}

	rm.markInstalled(destDir, fmt.Sprintf("registry:%d", entry.ID), time.Now().UnixMilli())

	log.WithFields(log.Fields{
		"type": "skill", "name": entry.Name, "sender": senderID,
		"from": entry.SourcePath, "to": destDir,
	}).Info("Installed skill from registry")
	return nil
}

// installAgent copies all .md files from cache dir into user's agents dir.
func (rm *RegistryManager) installAgent(entry *sqlite.SharedEntry, senderID string) error {
	agentsDir := tools.UserAgentsRoot(rm.workDir, senderID)
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("create agents dir: %w", err)
	}

	// Cache dir contains the .md file(s); copy them to user's agents dir
	srcEntries, err := os.ReadDir(entry.SourcePath)
	if err != nil {
		return fmt.Errorf("read cache: %w", err)
	}

	installed := 0
	for _, ent := range srcEntries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
			continue
		}
		destFile := filepath.Join(agentsDir, ent.Name())
		if _, err := os.Stat(destFile); err == nil {
			return fmt.Errorf("agent %q already installed", strings.TrimSuffix(ent.Name(), ".md"))
		}

		data, err := os.ReadFile(filepath.Join(entry.SourcePath, ent.Name()))
		if err != nil {
			return fmt.Errorf("read agent file: %w", err)
		}
		if err := os.WriteFile(destFile, data, 0o644); err != nil {
			return fmt.Errorf("write agent file: %w", err)
		}
		installed++
	}

	log.WithFields(log.Fields{
		"type": "agent", "name": entry.Name, "sender": senderID,
		"from": entry.SourcePath, "to": agentsDir, "files": installed,
	}).Info("Installed agent from registry")
	return nil
}

// Uninstall removes a user-installed skill/agent.
func (rm *RegistryManager) Uninstall(entryType, name, senderID string) error {
	switch entryType {
	case "skill":
		return rm.uninstallSkill(name, senderID)
	case "agent":
		return rm.uninstallAgent(name, senderID)
	default:
		return fmt.Errorf("unknown type %q", entryType)
	}
}

func (rm *RegistryManager) uninstallSkill(name, senderID string) error {
	dir := filepath.Join(tools.UserSkillsRoot(rm.workDir, senderID), name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("skill %q is not installed", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove skill: %w", err)
	}
	log.WithFields(log.Fields{"type": "skill", "name": name, "sender": senderID}).Info("Uninstalled")
	return nil
}

// uninstallAgent removes the agent's .md file from user's agents dir.
func (rm *RegistryManager) uninstallAgent(name, senderID string) error {
	agentsDir := tools.UserAgentsRoot(rm.workDir, senderID)
	mdFile := filepath.Join(agentsDir, name+".md")
	if _, err := os.Stat(mdFile); os.IsNotExist(err) {
		return fmt.Errorf("agent %q is not installed", name)
	}
	if err := os.Remove(mdFile); err != nil {
		return fmt.Errorf("remove agent: %w", err)
	}
	log.WithFields(log.Fields{"type": "agent", "name": name, "sender": senderID}).Info("Uninstalled")
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

	// Skills: each skill is a DIRECTORY containing SKILL.md
	if entryType == "" || entryType == "skill" {
		for _, dir := range rm.store.globalDirs {
			scanSkillDir(dir, &local, seen)
		}
		scanSkillDir(tools.UserSkillsRoot(rm.workDir, senderID), &local, seen)
	}

	// Agents: each agent is a .md FILE in the agents directory
	if entryType == "" || entryType == "agent" {
		if rm.agentStore != nil && rm.agentStore.globalDir != "" {
			scanAgentDir(rm.agentStore.globalDir, &local, seen)
		}
		scanAgentDir(tools.UserAgentsRoot(rm.workDir, senderID), &local, seen)
	}

	return published, local, nil
}

// scanSkillDir scans for skill subdirectories containing SKILL.md.
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

// scanAgentDir scans for agent .md files, extracting name from filename.
func scanAgentDir(dir string, out *[]string, seen map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(ent.Name(), ".md")
		key := "agent:" + name
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

func (rm *RegistryManager) registryCacheDir(entryType, name string) string {
	return filepath.Join(rm.workDir, ".xbot", "registry", entryType, name)
}

// snapshotDirToCache copies a source directory into cacheDir, replacing any existing cache.
func (rm *RegistryManager) snapshotDirToCache(src, cacheDir string) error {
	if err := os.RemoveAll(cacheDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clean cache: %w", err)
	}
	return copyDir(src, cacheDir)
}

// snapshotFileToCache copies a single file into a cache directory.
func (rm *RegistryManager) snapshotFileToCache(srcFile, cacheDir string) error {
	if err := os.RemoveAll(cacheDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clean cache: %w", err)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	data, err := os.ReadFile(srcFile)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	return os.WriteFile(filepath.Join(cacheDir, filepath.Base(srcFile)), data, 0o644)
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

// findAgentFile finds the .md file for a named agent across global + user dirs.
func (rm *RegistryManager) findAgentFile(name, senderID string) string {
	// Search global agents dir
	if rm.agentStore != nil && rm.agentStore.globalDir != "" {
		path := filepath.Join(rm.agentStore.globalDir, name+".md")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	// Search user-private agents dir
	if senderID != "" {
		path := filepath.Join(tools.UserAgentsRoot(rm.workDir, senderID), name+".md")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func (rm *RegistryManager) markInstalled(skillDir, installedFrom string, installedAt int64) {
	// Placeholder for writing install metadata.
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
