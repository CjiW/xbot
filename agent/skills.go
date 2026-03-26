package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
	"xbot/tools"
)

// SkillStore scans skill directories and generates a catalog for the system prompt.
// Skills are loaded on-demand by the LLM using the Read tool (OpenClaw-style progressive disclosure).
// Skill creation/deletion is done via Edit/Shell tools — no dedicated Skill tool needed.
type SkillStore struct {
	globalDirs     []string      // 全局只读 skills 根目录
	workDir        string        // 用于派生用户私有 skills 目录
	sandbox        tools.Sandbox // Sandbox 实例（nil 表示无沙箱）
	// per-user TTL cache (5 minutes). Uses map to support concurrent multi-user access
	// without cache thrashing (each user's cache is independent).
	mu         sync.RWMutex
	cache      map[string][]SkillInfo // key=userID, value=skills list
	cacheTimes map[string]time.Time   // key=userID, value=last refresh time
}

// NewSkillStore creates a SkillStore
func NewSkillStore(workDir string, globalDirs []string, sandbox tools.Sandbox) *SkillStore {
	return &SkillStore{
		workDir:    workDir,
		globalDirs: globalDirs,
		sandbox:    sandbox,
	}
}

// SkillInfo holds basic skill metadata parsed from SKILL.md frontmatter
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"` // absolute path to skill directory

	// v2: sharing & installation metadata
	Author        string `json:"author,omitempty"`
	Tags          string `json:"tags,omitempty"`
	Sharing       string `json:"sharing,omitempty"`
	InstalledFrom string `json:"installed_from,omitempty"`
	InstalledAt   int64  `json:"installed_at,omitempty"`
}

// userSkillsDir 返回用户 skill 目录路径（沙箱感知）
func (s *SkillStore) userSkillsDir(senderID string) string {
	if s.sandbox != nil && s.sandbox.Name() != "none" {
		return filepath.Join(s.sandbox.Workspace(senderID), "skills")
	}
	return tools.UserSkillsRoot(s.workDir, senderID)
}

// isUserSkillsSandboxed 返回用户 skills 目录是否在沙箱内
func (s *SkillStore) isUserSkillsSandboxed() bool {
	return s.sandbox != nil && s.sandbox.Name() != "none"
}

// ListSkills scans the skills directory and returns all discovered skills
func (s *SkillStore) ListSkills(ctx context.Context, senderID string) ([]SkillInfo, error) {
	// Check per-user cache
	s.mu.RLock()
	if s.cache != nil {
		if cached, ok := s.cache[senderID]; ok {
			if cacheTime, ok := s.cacheTimes[senderID]; ok && time.Since(cacheTime) < 5*time.Minute {
				s.mu.RUnlock()
				return cached, nil
			}
		}
	}
	s.mu.RUnlock()

	return s.refreshSkills(ctx, senderID)
}

// refreshSkills 扫描目录并更新缓存
func (s *SkillStore) refreshSkills(ctx context.Context, senderID string) ([]SkillInfo, error) {
	merged := make(map[string]SkillInfo)
	orderedNames := make([]string, 0)

	// 扫描全局目录（始终用 os.*）
	for _, dir := range s.globalDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillDir := filepath.Join(dir, e.Name())
			skillFile := filepath.Join(skillDir, "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				continue
			}

			data, err := os.ReadFile(skillFile)
			if err != nil {
				continue
			}

			name, description := parseSkillFrontmatter(data)
			if name == "" {
				name = e.Name()
			}

			if _, exists := merged[name]; !exists {
				orderedNames = append(orderedNames, name)
			}
			merged[name] = SkillInfo{
				Name:        name,
				Description: description,
				Path:        skillDir,
			}
		}
	}

	// 扫描用户目录（沙箱感知）
	s.scanUserSkills(ctx, senderID, merged, &orderedNames)

	sort.Strings(orderedNames)
	skills := make([]SkillInfo, 0, len(orderedNames))
	for _, name := range orderedNames {
		skills = append(skills, merged[name])
	}

	// 更新缓存
	s.mu.Lock()
	if s.cache == nil {
		s.cache = make(map[string][]SkillInfo)
		s.cacheTimes = make(map[string]time.Time)
	}
	s.cache[senderID] = skills
	s.cacheTimes[senderID] = time.Now()
	s.mu.Unlock()

	return skills, nil
}

// GetSkillsCatalog returns a formatted catalog of all available skills for the system prompt.
// The LLM uses the Read tool to load a skill's SKILL.md when the task matches its description.
func (s *SkillStore) GetSkillsCatalog(ctx context.Context, senderID string) string {
	skills, err := s.ListSkills(ctx, senderID)
	if err != nil {
		log.WithError(err).Warn("Failed to list skills for catalog")
		return ""
	}
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Available Skills\n\n")
	sb.WriteString("Skills 是特定任务的专门指导文档。当任务匹配时，用 `Skill` 工具加载对应的 skill 获取详细指令。\n\n")
	sb.WriteString("<available_skills>\n")
	for _, sk := range skills {
		fmt.Fprintf(&sb, "  <skill>\n    <name>%s</name>\n    <description>%s</description>\n  </skill>\n", sk.Name, sk.Description)
	}
	sb.WriteString("</available_skills>\n")
	return sb.String()
}

// InvalidateCache clears the skill cache for all users, forcing a rescan on the next ListSkills call.
func (s *SkillStore) InvalidateCache() {
	s.mu.Lock()
	s.cache = nil
	s.cacheTimes = nil
	s.mu.Unlock()
}

// scanUserSkills scans the user's private skills directory (sandbox-aware) and appends results to merged/orderedNames.
// Returns early (without error) if the user directory doesn't exist — missing directory is not an error.
func (s *SkillStore) scanUserSkills(ctx context.Context, senderID string, merged map[string]SkillInfo, orderedNames *[]string) {
	if senderID == "" {
		return
	}
	userDir := s.userSkillsDir(senderID)

	if s.isUserSkillsSandboxed() {
		entries, err := s.sandbox.ReadDir(ctx, userDir, senderID)
		if err != nil {
			return // directory doesn't exist or unreadable — not an error
		}
		for _, e := range entries {
			if !e.IsDir {
				continue
			}
			skillDir := filepath.Join(userDir, e.Name)
			skillFile := filepath.Join(skillDir, "SKILL.md")
			if _, err := s.sandbox.Stat(ctx, skillFile, senderID); err != nil {
				continue
			}
			data, err := s.sandbox.ReadFile(ctx, skillFile, senderID)
			if err != nil {
				continue
			}
			name, description := parseSkillFrontmatter(data)
			if name == "" {
				name = e.Name
			}
			if _, exists := merged[name]; !exists {
				*orderedNames = append(*orderedNames, name)
			}
			merged[name] = SkillInfo{Name: name, Description: description, Path: skillDir}
		}
	} else {
		entries, err := os.ReadDir(userDir)
		if err != nil {
			return // directory doesn't exist or unreadable — not an error
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillDir := filepath.Join(userDir, e.Name())
			skillFile := filepath.Join(skillDir, "SKILL.md")
			if _, err := os.Stat(skillFile); err != nil {
				continue
			}
			data, err := os.ReadFile(skillFile)
			if err != nil {
				continue
			}
			name, description := parseSkillFrontmatter(data)
			if name == "" {
				name = e.Name()
			}
			if _, exists := merged[name]; !exists {
				*orderedNames = append(*orderedNames, name)
			}
			merged[name] = SkillInfo{Name: name, Description: description, Path: skillDir}
		}
	}
}

// parseSkillFrontmatter extracts name and description from SKILL.md YAML frontmatter data.
func parseSkillFrontmatter(data []byte) (name, description string) {
	content := string(data)

	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return "", ""
	}

	trimmed := strings.TrimSpace(content)
	rest := trimmed[3:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return "", ""
	}

	for _, line := range strings.Split(rest[:endIdx], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return name, description
}

// parseSkillFrontmatterV2 parses SKILL.md YAML frontmatter from data bytes.
// It extracts name, description, sharing, author, and tags fields.
// On parse failure, it falls back to the directory name with sharing="private".
func parseSkillFrontmatterV2(data []byte, skillDir string) SkillInfo {
	content := string(data)
	info := SkillInfo{
		Path:    skillDir,
		Sharing: "private",
	}

	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		dirName := filepath.Base(skillDir)
		info.Name = dirName
		return info
	}

	trimmed := strings.TrimSpace(content)
	rest := trimmed[3:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		dirName := filepath.Base(skillDir)
		info.Name = dirName
		return info
	}

	for _, line := range strings.Split(rest[:endIdx], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			info.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "description:") {
			info.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		} else if strings.HasPrefix(line, "author:") {
			info.Author = strings.TrimSpace(strings.TrimPrefix(line, "author:"))
		} else if strings.HasPrefix(line, "tags:") {
			info.Tags = strings.TrimSpace(strings.TrimPrefix(line, "tags:"))
		} else if strings.HasPrefix(line, "sharing:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "sharing:"))
			if val == "public" {
				info.Sharing = "public"
			}
		}
	}

	if info.Name == "" {
		info.Name = filepath.Base(skillDir)
	}
	return info
}
