package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadAgentRoles 从目录加载所有 agent 定义文件（*.md）
// 每个文件包含 YAML frontmatter（name, description, tools）和 SystemPrompt 正文
func LoadAgentRoles(dir string) ([]SubAgentRole, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read agents dir %s: %w", dir, err)
	}

	var roles []SubAgentRole
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		role, err := ParseAgentFile(path)
		if err != nil {
			return nil, fmt.Errorf("parse agent file %s: %w", path, err)
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// ParseAgentFile 解析单个 agent 定义文件
// 格式：YAML frontmatter（--- 之间）+ Markdown 正文作为 SystemPrompt
func ParseAgentFile(path string) (SubAgentRole, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SubAgentRole{}, err
	}

	content := string(data)

	// 分离 frontmatter 和正文
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return SubAgentRole{}, fmt.Errorf("invalid frontmatter: %w", err)
	}

	// 解析 frontmatter 字段
	name, description, allowedTools, caps, err := parseFrontmatter(frontmatter)
	if err != nil {
		return SubAgentRole{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	if name == "" {
		// 用文件名（去掉 .md）作为 fallback
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	// 默认允许 spawn_agent，除非 frontmatter 中显式设置 spawn_agent: false
	// （已在 parseFrontmatter 中处理）
	return SubAgentRole{
		Name:         name,
		Description:  description,
		SystemPrompt: strings.TrimSpace(body),
		AllowedTools: allowedTools,
		Capabilities: caps,
	}, nil
}

// splitFrontmatter 分离 YAML frontmatter 和正文
// 期望格式：以 "---\n" 开头，第二个 "---\n" 结束 frontmatter
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	// 去掉可能的 BOM
	content = strings.TrimPrefix(content, "\xef\xbb\xbf")

	if !strings.HasPrefix(content, "---") {
		return "", "", fmt.Errorf("file does not start with ---")
	}

	// 找第二个 ---
	rest := content[3:] // 跳过第一个 ---
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")

	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", "", fmt.Errorf("closing --- not found")
	}

	frontmatter = rest[:idx]
	body = rest[idx+4:] // 跳过 "\n---"
	// 跳过 --- 后面的换行
	body = strings.TrimPrefix(body, "\r\n")
	body = strings.TrimPrefix(body, "\n")

	return frontmatter, body, nil
}

// parseFrontmatter 手动解析简单 YAML frontmatter
// 支持 name, description（字符串）、tools（列表）和 capabilities（子字段）
// 默认 spawn_agent=true，除非显式设置 spawn_agent: false
func parseFrontmatter(fm string) (name, description string, tools []string, caps SubAgentCapabilities, err error) {
	caps = SubAgentCapabilities{
		SpawnAgent: true, // 默认允许 spawn 子 agent
	}
	lines := strings.Split(fm, "\n")
	var currentField string

	for _, line := range lines {
		// 去掉 \r
		line = strings.TrimRight(line, "\r")

		// 跳过空行和注释
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// 列表项：以 "  - " 或 "- " 开头（属于当前字段）
		if strings.HasPrefix(trimmed, "- ") {
			if currentField == "tools" {
				item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
				if item != "" {
					tools = append(tools, item)
				}
			}
			continue
		}

		// 缩进的键值对（capabilities 子字段）
		if (strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")) && currentField == "capabilities" {
			colonIdx := strings.Index(trimmed, ":")
			if colonIdx < 0 {
				continue
			}
			key := strings.TrimSpace(trimmed[:colonIdx])
			value := strings.TrimSpace(trimmed[colonIdx+1:])
			value = stripQuotes(value)
			switch key {
			case "memory":
				caps.Memory = isTruthy(value)
			case "send_message":
				caps.SendMessage = isTruthy(value)
			case "spawn_agent":
				caps.SpawnAgent = isTruthy(value)
			}
			continue
		}

		// 键值对：key: value
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}

		key := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])

		// 去掉引号包裹
		value = stripQuotes(value)

		switch key {
		case "name":
			name = value
			currentField = "name"
		case "description":
			description = value
			currentField = "description"
		case "tools":
			currentField = "tools"
			// tools 的值可能在同一行（如 tools: [a, b]）或后续行（列表格式）
			// 我们只支持列表格式，忽略同行值
		case "capabilities":
			currentField = "capabilities"
		default:
			currentField = ""
		}
	}

	// 校验 name 格式
	if name != "" {
		for _, c := range name {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				return "", "", nil, SubAgentCapabilities{}, fmt.Errorf("invalid agent name %q: only letters, digits, hyphens and underscores are allowed", name)
			}
		}
	}

	// 校验 tools 列表格式
	for _, t := range tools {
		if t == "" {
			return "", "", nil, SubAgentCapabilities{}, fmt.Errorf("empty tool name in tools list")
		}
		for _, c := range t {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
				return "", "", nil, SubAgentCapabilities{}, fmt.Errorf("invalid tool name %q: only letters, digits, hyphens, underscores and dots are allowed", t)
			}
		}
	}

	return name, description, tools, caps, nil
}

// isTruthy 判断字符串是否表示 true
func isTruthy(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "yes" || s == "1"
}

// stripQuotes 去掉字符串两端的引号（单引号或双引号）
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
