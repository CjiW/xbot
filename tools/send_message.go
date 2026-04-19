package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"xbot/llm"
)

// SendMessageTool sends a message to any addressable Channel.
// This is the unified messaging tool — works for agent-to-agent,
// agent-to-IM, and group broadcast.
type SendMessageTool struct{}

func (t *SendMessageTool) Name() string { return "SendMessage" }

func (t *SendMessageTool) Description() string {
	return `Send a message to any addressable target (agent, group, or IM user).

## Addressing
- Agent: "agent:<role>/<instance>" (e.g., "agent:reviewer/cr1")
- Group: "group:<id>" (e.g., "group:g1")
- IM user (Feishu): "feishu:<open_id>" (e.g., "feishu:ou_xxx")

## Behavior
- For agent targets: blocks until reply (RPC), returns the agent's response
- For group targets: broadcasts to all members, returns confirmation
- For IM targets: sends message immediately (fire-and-forget)

## Use cases
- Send a task to a SubAgent after creating it with CreateChat
- Proactively notify a Feishu user about task completion
- Broadcast a message to a group of agents`
}

type SendMessageParams struct {
	To      string `json:"to" jsonschema:"required,description=Target address (agent:xxx, group:xxx, feishu:xxx)"`
	Message string `json:"message" jsonschema:"required,description=Message content to send"`
}

func (t *SendMessageTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "to", Type: "string", Description: "Target address (agent:xxx, group:xxx, feishu:xxx)", Required: true},
		{Name: "message", Type: "string", Description: "Message content to send", Required: true},
	}
}

func (t *SendMessageTool) Execute(ctx *ToolContext, raw string) (*ToolResult, error) {
	var params SendMessageParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	channelName, chatID := parseAddress(params.To)
	if channelName == "" {
		return nil, fmt.Errorf("invalid address format: %q", params.To)
	}

	// Agent addresses go through InteractiveSubAgentManager.SendInteractive
	// (not Dispatcher, since SubAgents are not registered Channels).
	if len(channelName) > 6 && channelName[:6] == "agent:" {
		im, ok := ctx.Manager.(InteractiveSubAgentManager)
		if !ok {
			return nil, fmt.Errorf("agent messaging not available in this context")
		}
		// Parse "agent:<role>-<instance>" → role, instance
		role, instance := parseAgentAddress(channelName)
		if role == "" || instance == "" {
			return nil, fmt.Errorf("invalid agent address %q: expected format agent:<role>-<instance>", params.To)
		}
		// Load role to get system prompt and capabilities for SendInteractive
		roleDef, ok := loadRoleFromCtx(ctx, role)
		if !ok {
			return nil, fmt.Errorf("unknown agent role: %s", role)
		}
		result, err := im.SendInteractive(ctx, params.Message, role, roleDef.SystemPrompt, roleDef.AllowedTools, roleDef.Capabilities, instance, "")
		if err != nil {
			return nil, fmt.Errorf("agent send failed: %w", err)
		}
		return NewResult(result), nil
	}

	// Group and IM addresses go through Dispatcher
	if ctx.MessageSender == nil {
		return nil, fmt.Errorf("message sending not available in this context")
	}

	result, err := ctx.MessageSender.SendMessage(channelName, chatID, params.Message)
	if err != nil {
		return nil, fmt.Errorf("send failed: %w", err)
	}

	if result != "" {
		return NewResult(result), nil
	}
	return NewResult(fmt.Sprintf("Message sent to %s", params.To)), nil
}

// parseAgentAddress splits "agent:<role>/<instance>" into (role, instance).
// Returns ("", "") if the format doesn't match.
func parseAgentAddress(addr string) (role, instance string) {
	// addr is already confirmed to start with "agent:"
	rest := addr[6:]
	idx := strings.Index(rest, "/")
	if idx < 0 {
		return "", ""
	}
	return rest[:idx], rest[idx+1:]
}

// loadRoleFromCtx loads a SubAgentRole using the ToolContext's sandbox and directory info.
func loadRoleFromCtx(ctx *ToolContext, roleName string) (*SubAgentRole, bool) {
	EnsureSynced(ctx)
	originUserID := ctx.OriginUserID
	if originUserID == "" {
		originUserID = ctx.SenderID
	}

	var roleSb Sandbox
	var roleUserID string
	var userAgentDirs []string
	if shouldUseSandbox(ctx) {
		roleSb = ctx.Sandbox
		roleUserID = originUserID
		if sbDir := sandboxBaseDir(ctx); sbDir != "" {
			userAgentDirs = append(userAgentDirs, filepath.Join(sbDir, "agents"))
		}
	} else {
		if originUserID != "" && ctx.WorkingDir != "" {
			userAgentDirs = append(userAgentDirs, UserAgentsRoot(ctx.WorkingDir, originUserID))
		}
		if ctx.WorkspaceRoot != "" {
			userAgentDirs = append(userAgentDirs, filepath.Join(ctx.WorkspaceRoot, ".agents"))
		}
	}

	role, ok := GetSubAgentRoleSandbox(ctx.Ctx, roleName, roleSb, roleUserID, userAgentDirs...)
	return role, ok
}

// parseAddress splits an address into (channelName, chatID).
// "agent:reviewer" → ("agent:reviewer", "")
// "feishu:ou_xxx" → ("feishu", "ou_xxx")
// "group:rt1" → ("group:rt1", "")
func parseAddress(addr string) (channelName, chatID string) {
	// Known IM prefixes: checked longest-first to avoid ambiguity
	imPrefixes := []string{"feishu", "web", "qq", "cli"}
	for _, prefix := range imPrefixes {
		if len(addr) > len(prefix)+1 && addr[:len(prefix)+1] == prefix+":" {
			return prefix, addr[len(prefix)+1:]
		}
	}
	// Agent or group: the whole address is the channel name
	return addr, ""
}
