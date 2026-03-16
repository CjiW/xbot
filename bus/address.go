package bus

import (
	"fmt"
	"strings"
)

// Address 统一寻址标识。
//
// 格式: scheme://domain/id
//
// 示例:
//
//	im://feishu/ou_xxx       — 飞书用户
//	im://feishu/oc_xxx       — 飞书群聊
//	im://qq/12345            — QQ 用户
//	agent://main             — 主 Agent
//	agent://main/code-reviewer — SubAgent
//	system://cron            — 定时任务
type Address struct {
	Scheme string // "im", "agent", "system"
	Domain string // "feishu", "qq", "main", "cron"
	ID     string // 实体标识（可为空，如 agent://main）
}

// Common address schemes.
const (
	SchemeIM     = "im"
	SchemeAgent  = "agent"
	SchemeSystem = "system"
)

// ParseAddress 解析地址字符串。
//
//	"im://feishu/ou_xxx" → Address{Scheme:"im", Domain:"feishu", ID:"ou_xxx"}
//	"agent://main"       → Address{Scheme:"agent", Domain:"main", ID:""}
//	"agent://main/cr"    → Address{Scheme:"agent", Domain:"main", ID:"cr"}
func ParseAddress(s string) (Address, error) {
	// scheme://rest
	idx := strings.Index(s, "://")
	if idx < 0 {
		return Address{}, fmt.Errorf("invalid address %q: missing scheme", s)
	}
	scheme := s[:idx]
	rest := s[idx+3:] // domain[/id]

	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		// no id part: "agent://main"
		return Address{Scheme: scheme, Domain: rest}, nil
	}
	return Address{Scheme: scheme, Domain: rest[:slash], ID: rest[slash+1:]}, nil
}

// String 返回规范化的地址字符串。
func (a Address) String() string {
	if a.ID == "" {
		return a.Scheme + "://" + a.Domain
	}
	return a.Scheme + "://" + a.Domain + "/" + a.ID
}

// IsZero 判断地址是否为零值。
func (a Address) IsZero() bool {
	return a.Scheme == "" && a.Domain == "" && a.ID == ""
}

// IsIM 判断是否为 IM 渠道地址。
func (a Address) IsIM() bool {
	return a.Scheme == SchemeIM
}

// IsAgent 判断是否为 Agent 地址。
func (a Address) IsAgent() bool {
	return a.Scheme == SchemeAgent
}

// IsSystem 判断是否为系统地址。
func (a Address) IsSystem() bool {
	return a.Scheme == SchemeSystem
}

// ChannelName 返回 IM 渠道名称（兼容现有 Channel 字段）。
// 对于 IM 地址返回 Domain（如 "feishu"），其他返回 Scheme（如 "agent"）。
func (a Address) ChannelName() string {
	if a.Scheme == SchemeIM {
		return a.Domain
	}
	return a.Scheme
}

// --- 便捷构造函数 ---

// NewIMAddress 创建 IM 渠道地址。
//
//	NewIMAddress("feishu", "ou_xxx") → im://feishu/ou_xxx
func NewIMAddress(channel, id string) Address {
	return Address{Scheme: SchemeIM, Domain: channel, ID: id}
}

// NewAgentAddress 创建 Agent 地址。
//
//	NewAgentAddress("main")    → agent://main
//	NewAgentAddress("main/cr") → agent://main/cr
func NewAgentAddress(path string) Address {
	slash := strings.IndexByte(path, '/')
	if slash < 0 {
		return Address{Scheme: SchemeAgent, Domain: path}
	}
	return Address{Scheme: SchemeAgent, Domain: path[:slash], ID: path[slash+1:]}
}

// NewSystemAddress 创建系统地址。
//
//	NewSystemAddress("cron") → system://cron
func NewSystemAddress(name string) Address {
	return Address{Scheme: SchemeSystem, Domain: name}
}

// --- 从现有字段构造（迁移辅助） ---

// AddressFromChannelID 从现有的 (channel, id) 对构造 Address。
// 用于迁移期间，将旧的 Channel+ChatID/SenderID 转换为统一地址。
//
//	AddressFromChannelID("feishu", "oc_xxx") → im://feishu/oc_xxx
//	AddressFromChannelID("agent", "main/cr") → agent://main/cr
func AddressFromChannelID(channel, id string) Address {
	switch channel {
	case SchemeAgent:
		return NewAgentAddress(id)
	case SchemeSystem:
		return NewSystemAddress(id)
	default:
		return NewIMAddress(channel, id)
	}
}
