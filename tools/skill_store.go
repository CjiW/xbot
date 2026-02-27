package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	log "xbot/logger"
)

// SkillStore 管理 skill 的存储和激活状态
// Skill 格式: skills/{skill-name}/SKILL.md (含 YAML frontmatter)
// 可选子目录: scripts/, references/, assets/
type SkillStore struct {
	mu            sync.RWMutex
	dir           string            // skills 根目录（DataDir/skills）
	active        map[string]string // 已激活的 skill: name -> body content (不含 frontmatter)
	autoActivated map[string]bool   // 由 AutoActivate 自动激活的 skill（区别于手动激活，回复后自动清理）
}

// NewSkillStore 创建 SkillStore
func NewSkillStore(dir string) *SkillStore {
	os.MkdirAll(dir, 0755)
	return &SkillStore{
		dir:           dir,
		active:        make(map[string]string),
		autoActivated: make(map[string]bool),
	}
}

// Dir 返回 skills 根目录
func (s *SkillStore) Dir() string {
	return s.dir
}

// SkillInfo skill 基本信息
type SkillInfo struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Triggers     []string `json:"triggers,omitempty"`
	AutoActivate bool     `json:"auto_activate"`
	Active       bool     `json:"active"`
	Path         string   `json:"path"`
}

// ListSkills 列出所有可用的 skill
func (s *SkillStore) ListSkills() ([]SkillInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var skills []SkillInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(s.dir, e.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			continue
		}

		fm := s.parseFrontmatterFull(skillFile)
		name := fm.Name
		if name == "" {
			name = e.Name()
		}
		_, activated := s.active[name]
		skills = append(skills, SkillInfo{
			Name:         name,
			Description:  fm.Description,
			Triggers:     fm.Triggers,
			AutoActivate: fm.AutoActivate,
			Active:       activated,
			Path:         skillDir,
		})
	}
	return skills, nil
}

// skillFrontmatter SKILL.md 的 YAML frontmatter 解析结果
type skillFrontmatter struct {
	Name         string
	Description  string
	Triggers     []string // 关键词列表，用于消息匹配自动激活
	AutoActivate bool     // 是否参与自动激活（默认 true）
}

// parseFrontmatterFull 从 SKILL.md 解析完整 YAML frontmatter
func (s *SkillStore) parseFrontmatterFull(path string) skillFrontmatter {
	data, err := os.ReadFile(path)
	if err != nil {
		return skillFrontmatter{AutoActivate: true}
	}
	content := string(data)

	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return skillFrontmatter{AutoActivate: true}
	}

	trimmed := strings.TrimSpace(content)
	rest := trimmed[3:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return skillFrontmatter{AutoActivate: true}
	}

	fm := skillFrontmatter{AutoActivate: true}
	for _, line := range strings.Split(rest[:endIdx], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			fm.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "description:") {
			fm.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		} else if strings.HasPrefix(line, "triggers:") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "triggers:"))
			for _, t := range strings.Split(raw, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					fm.Triggers = append(fm.Triggers, strings.ToLower(t))
				}
			}
		} else if strings.HasPrefix(line, "auto_activate:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "auto_activate:"))
			fm.AutoActivate = val != "false"
		}
	}
	return fm
}

// parseFrontmatter 兼容旧调用，只返回 name 和 description
func (s *SkillStore) parseFrontmatter(path string) (name, description string) {
	fm := s.parseFrontmatterFull(path)
	return fm.Name, fm.Description
}

// getSkillBody 读取 SKILL.md 的 body 部分（去掉 YAML frontmatter）
func (s *SkillStore) getSkillBody(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)

	// 去掉 frontmatter
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "---") {
		rest := trimmed[3:]
		endIdx := strings.Index(rest, "\n---")
		if endIdx >= 0 {
			body := strings.TrimSpace(rest[endIdx+4:]) // 跳过 \n---
			return body, nil
		}
	}
	// 没有 frontmatter，返回全部内容
	return content, nil
}

// GetSkillContent 读取 skill 的完整 SKILL.md 内容
func (s *SkillStore) GetSkillContent(name string) (string, error) {
	skillFile := s.findSkillFile(name)
	if skillFile == "" {
		return "", fmt.Errorf("skill %q not found", name)
	}
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return "", fmt.Errorf("read skill: %w", err)
	}
	return string(data), nil
}

// GetSkillDir 返回 skill 的目录路径
func (s *SkillStore) GetSkillDir(name string) string {
	// 先按 frontmatter name 查找
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(s.dir, e.Name(), "SKILL.md")
		n, _ := s.parseFrontmatter(skillFile)
		if n == name || e.Name() == name {
			return filepath.Join(s.dir, e.Name())
		}
	}
	return ""
}

// findSkillFile 查找 skill 的 SKILL.md 路径（支持按 name 或目录名匹配）
func (s *SkillStore) findSkillFile(name string) string {
	// 先尝试按目录名直接查找
	direct := filepath.Join(s.dir, name, "SKILL.md")
	if _, err := os.Stat(direct); err == nil {
		return direct
	}

	// 再遍历查找 frontmatter name 匹配的
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(s.dir, e.Name(), "SKILL.md")
		n, _ := s.parseFrontmatter(skillFile)
		if n == name {
			return skillFile
		}
	}
	return ""
}

// SaveSkill 创建或更新 skill
// 如果 skill 目录不存在，创建 {dir}/{name}/SKILL.md
func (s *SkillStore) SaveSkill(name, content string) error {
	skillDir := filepath.Join(s.dir, name)
	os.MkdirAll(skillDir, 0755)

	skillFile := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("save skill: %w", err)
	}

	// 如果该 skill 已激活，更新内存中的 body
	body, _ := s.getSkillBody(skillFile)
	s.mu.Lock()
	if _, ok := s.active[name]; ok {
		s.active[name] = body
	}
	s.mu.Unlock()

	log.WithField("skill", name).Info("Skill saved")
	return nil
}

// DeleteSkill 删除 skill（整个目录）
func (s *SkillStore) DeleteSkill(name string) error {
	skillDir := s.GetSkillDir(name)
	if skillDir == "" {
		return fmt.Errorf("skill %q not found", name)
	}

	s.Deactivate(name)

	if err := os.RemoveAll(skillDir); err != nil {
		return fmt.Errorf("delete skill: %w", err)
	}
	log.WithField("skill", name).Info("Skill deleted")
	return nil
}

// Activate 激活 skill（加载 SKILL.md body 到内存）
func (s *SkillStore) Activate(name string) error {
	skillFile := s.findSkillFile(name)
	if skillFile == "" {
		return fmt.Errorf("skill %q not found", name)
	}

	body, err := s.getSkillBody(skillFile)
	if err != nil {
		return fmt.Errorf("read skill body: %w", err)
	}

	s.mu.Lock()
	s.active[name] = body
	s.mu.Unlock()

	log.WithField("skill", name).Info("Skill activated")
	return nil
}

// Deactivate 停用 skill
func (s *SkillStore) Deactivate(name string) {
	s.mu.Lock()
	_, existed := s.active[name]
	delete(s.active, name)
	s.mu.Unlock()

	if existed {
		log.WithField("skill", name).Info("Skill deactivated")
	}
}

// ActiveNames 返回当前激活的 skill 名称列表
func (s *SkillStore) ActiveNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.active))
	for name := range s.active {
		names = append(names, name)
	}
	return names
}

// AutoActivate 根据用户消息自动激活匹配的 skill
// 对所有设置了 triggers 且 auto_activate != false 的 skill，检查用户消息是否包含触发词
// 返回本次新激活的 skill 名称列表
func (s *SkillStore) AutoActivate(userMessage string) []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}

	msgLower := strings.ToLower(userMessage)
	var activated []string

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(s.dir, e.Name(), "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			continue
		}

		fm := s.parseFrontmatterFull(skillFile)
		if !fm.AutoActivate || len(fm.Triggers) == 0 {
			continue
		}

		name := fm.Name
		if name == "" {
			name = e.Name()
		}

		s.mu.RLock()
		_, alreadyActive := s.active[name]
		s.mu.RUnlock()
		if alreadyActive {
			continue
		}

		for _, trigger := range fm.Triggers {
			if strings.Contains(msgLower, trigger) {
				body, err := s.getSkillBody(skillFile)
				if err != nil {
					log.WithError(err).WithField("skill", name).Warn("Auto-activate: failed to read skill body")
					break
				}
				s.mu.Lock()
				s.active[name] = body
				s.autoActivated[name] = true
				s.mu.Unlock()
				activated = append(activated, name)
				log.WithFields(log.Fields{
					"skill":   name,
					"trigger": trigger,
				}).Info("Skill auto-activated by trigger match")
				break
			}
		}
	}
	return activated
}

// DeactivateAuto 清理所有由 AutoActivate 自动激活的 skill
// 手动激活的 skill 不受影响
func (s *SkillStore) DeactivateAuto() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.autoActivated) == 0 {
		return
	}

	var names []string
	for name := range s.autoActivated {
		delete(s.active, name)
		names = append(names, name)
	}
	s.autoActivated = make(map[string]bool)

	log.WithField("skills", names).Info("Auto-activated skills deactivated")
}

// GetSkillsCatalog 返回所有可用（未激活）skill 的简要目录
// 注入到系统提示中，让 LLM 知道可以用 Skill activate 激活哪些 skill
func (s *SkillStore) GetSkillsCatalog() string {
	skills, err := s.ListSkills()
	if err != nil || len(skills) == 0 {
		return ""
	}

	var inactive []SkillInfo
	for _, sk := range skills {
		if !sk.Active {
			inactive = append(inactive, sk)
		}
	}
	if len(inactive) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Available Skills (not yet active)\n\n")
	sb.WriteString("Use the Skill tool with action \"activate\" to enable a skill when the conversation topic matches.\n\n")
	for _, sk := range inactive {
		fmt.Fprintf(&sb, "- **%s**: %s\n", sk.Name, sk.Description)
	}
	return sb.String()
}

// GetActiveSkillsPrompt 返回所有已激活 skill 的合并 prompt，用于注入系统提示
func (s *SkillStore) GetActiveSkillsPrompt() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.active) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Active Skills\n\n")
	for name, body := range s.active {
		fmt.Fprintf(&sb, "## Skill: %s\n\n%s\n\n", name, body)
	}
	return sb.String()
}
