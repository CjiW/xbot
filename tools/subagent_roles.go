package tools

import (
	"fmt"
	"os"
	"strings"

	log "xbot/logger"
)

// SubAgentRole 预定义的 SubAgent 角色
type SubAgentRole struct {
	Name         string
	Description  string
	SystemPrompt string
	AllowedTools []string
}

// agentRoles 从文件加载的角色列表
var agentRoles []SubAgentRole

// InitAgentRoles 从指定目录加载 agent 角色定义
// 如果目录不存在，不报错（没有预定义角色也可以）
func InitAgentRoles(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		log.WithField("dir", dir).Info("Agents directory not found, no predefined roles loaded")
		return nil
	}

	roles, err := LoadAgentRoles(dir)
	if err != nil {
		return fmt.Errorf("load agent roles from %s: %w", dir, err)
	}

	agentRoles = roles
	log.WithField("count", len(roles)).Info("Agent roles loaded")
	for _, r := range roles {
		log.WithFields(log.Fields{
			"name":  r.Name,
			"tools": strings.Join(r.AllowedTools, ", "),
		}).Debug("Loaded agent role")
	}
	return nil
}

// GetSubAgentRole 根据名称查找角色
func GetSubAgentRole(name string) (*SubAgentRole, bool) {
	for i := range agentRoles {
		if agentRoles[i].Name == name {
			return &agentRoles[i], true
		}
	}
	return nil, false
}

// ListSubAgentRoles 返回所有可用角色
func ListSubAgentRoles() []SubAgentRole {
	return agentRoles
}
