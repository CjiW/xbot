package tools

import (
	"encoding/json"
	"fmt"

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
- Agent: "agent:<role>-<instance>" (e.g., "agent:reviewer-cr1")
- Group: "group:<id>" (e.g., "group:roundtable")
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

	if ctx.MessageSender == nil {
		return nil, fmt.Errorf("message sending not available in this context")
	}

	// Parse address: "agent:xxx" → channel="agent:xxx", chatID=""
	// "feishu:ou_xxx" → channel="feishu", chatID="ou_xxx"
	channelName, chatID := parseAddress(params.To)
	if channelName == "" {
		return nil, fmt.Errorf("invalid address format: %q", params.To)
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
