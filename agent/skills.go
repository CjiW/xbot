package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	log "xbot/logger"
	"xbot/tools"
)

// SkillStore scans skill directories and generates a catalog for the system prompt.
// Skills are loaded on-demand by the LLM using the Read tool (OpenClaw-style progressive disclosure).
// Skill creation/deletion is done via Edit/Shell tools — no dedicated Skill tool needed.
type SkillStore struct {
	globalDirs []string // 全局只读 skills 根目录
	workDir    string   // 用于派生用户私有 skills 目录
}

// NewSkillStore creates a SkillStore
func NewSkillStore(workDir string, globalDirs []string) *SkillStore {
	return &SkillStore{workDir: workDir, globalDirs: globalDirs}
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

// ListSkills scans the skills directory and returns all discovered skills
func (s *SkillStore) ListSkills(senderID string) ([]SkillInfo, error) {
	sources := make([]string, 0, len(s.globalDirs)+1)
	sources = append(sources, s.globalDirs...)
	if senderID != "" {
		sources = append(sources, tools.UserSkillsRoot(s.workDir, senderID))
	}

	merged := make(map[string]SkillInfo)
	orderedNames := make([]string, 0)

	for _, dir := range sources {
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

			name, description := parseSkillFrontmatter(skillFile)
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

	sort.Strings(orderedNames)
	skills := make([]SkillInfo, 0, len(orderedNames))
	for _, name := range orderedNames {
		skills = append(skills, merged[name])
	}
	return skills, nil
}

// GetSkillsCatalog returns a formatted catalog of all available skills for the system prompt.
// The LLM uses the Read tool to load a skill's SKILL.md when the task matches its description.
func (s *SkillStore) GetSkillsCatalog(senderID string) string {
	skills, err := s.ListSkills(senderID)
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

// parseSkillFrontmatter extracts name and description from a SKILL.md YAML frontmatter
func parseSkillFrontmatter(path string) (name, description string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
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

// parseSkillFrontmatterV2 parses SKILL.md YAML frontmatter from a skill directory.
// It extracts name, description, sharing, author, and tags fields.
// On parse failure, it falls back to the directory name with sharing="private".
func parseSkillFrontmatterV2(skillDir string) SkillInfo {
	skillFile := filepath.Join(skillDir, "SKILL.md")
	data, err := os.ReadFile(skillFile)
	if err != nil {
		dirName := filepath.Base(skillDir)
		return SkillInfo{
			Name:    dirName,
			Path:    skillDir,
			Sharing: "private",
		}
	}

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
