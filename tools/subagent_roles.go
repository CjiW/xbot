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
}

// agentsDir 存储 agents 目录路径，供运行时按需加载
var agentsDir string

// InitAgentRoles 设置 agents 目录路径（启动时调用一次）
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
func GetSubAgentRole(name string) (*SubAgentRole, bool) {
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
