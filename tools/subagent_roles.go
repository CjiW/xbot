package tools

import (
	"fmt"
	"os"

	log "xbot/logger"
)

// SubAgentRole 预定义的 SubAgent 角色
type SubAgentRole struct {
	Name         string
	Description  string
	SystemPrompt string
	AllowedTools []string

	// Capabilities 声明 SubAgent 可获得的能力（Phase 3）
	// 在 .xbot/agents/*.md frontmatter 中通过 capabilities 字段声明
	Capabilities SubAgentCapabilities
}

// SubAgentCapabilities SubAgent 能力声明
type SubAgentCapabilities struct {
	Memory      bool // 可访问 Letta memory（core/archival/recall）
	SendMessage bool // 可直接向 IM 渠道发送消息
	SpawnAgent  bool // 可创建子 Agent（需注意递归深度限制）
}

// ToMap 转换为 map[string]bool，用于跨包传递（避免循环依赖）。
// 始终包含所有三个 key，确保显式设置的 false 值不会在反序列化时被默认值覆盖。
func (c SubAgentCapabilities) ToMap() map[string]bool {
	m := make(map[string]bool)
	m["memory"] = c.Memory
	m["send_message"] = c.SendMessage
	m["spawn_agent"] = c.SpawnAgent
	return m
}

// CapabilitiesFromMap 从 map[string]bool 构造 SubAgentCapabilities。
// 默认 SpawnAgent=true：所有 agent 都能创建子 agent，除非显式设置 spawn_agent=false。
func CapabilitiesFromMap(m map[string]bool) SubAgentCapabilities {
	caps := SubAgentCapabilities{
		SpawnAgent: true, // 默认允许 spawn 子 agent
	}
	if m != nil {
		caps.Memory = m["memory"]
		caps.SendMessage = m["send_message"]
		if _, ok := m["spawn_agent"]; ok {
			caps.SpawnAgent = m["spawn_agent"]
		}
	}
	return caps
}

// agentsDir 存储全局 agents 目录路径，供运行时按需加载
var agentsDir string

// InitAgentRoles 设置全局 agents 目录路径（启动时调用一次）
// 实际加载在每次 GetSubAgentRole 调用时按需进行
func InitAgentRoles(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		log.WithField("dir", dir).Info("Agents directory not found, no predefined roles available")
		return nil
	}
	agentsDir = dir
	// 验证目录可读
	roles, err := LoadAgentRoles(dir)
	if err != nil {
		return fmt.Errorf("validate agent roles in %s: %w", dir, err)
	}
	log.WithField("count", len(roles)).Info("Agent roles directory configured")
	return nil
}

// GetSubAgentRole 根据名称查找角色（每次从文件加载，支持热更新）
// 先查用户私有目录，再查全局目录（用户角色优先）
func GetSubAgentRole(name string, userAgentDirs ...string) (*SubAgentRole, bool) {
	// 先搜索用户私有目录
	for _, dir := range userAgentDirs {
		if dir == "" {
			continue
		}
		roles, err := LoadAgentRoles(dir)
		if err != nil {
			log.WithField("dir", dir).WithError(err).Warn("Failed to load user agent roles, skipping directory")
			continue
		}
		for i := range roles {
			if roles[i].Name == name {
				return &roles[i], true
			}
		}
	}

	// 再搜索全局目录
	if agentsDir == "" {
		return nil, false
	}
	roles, err := LoadAgentRoles(agentsDir)
	if err != nil {
		log.WithError(err).Warn("Failed to load agent roles")
		return nil, false
	}
	for i := range roles {
		if roles[i].Name == name {
			return &roles[i], true
		}
	}
	return nil, false
}
