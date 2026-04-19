package tools

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
)

// CreateChatTool creates a new conversation (agent private chat or group chat).
// This replaces the SubAgent tool for creating agents, and adds group chat creation.
type CreateChatTool struct{}

func (t *CreateChatTool) Name() string { return "CreateChat" }

func (t *CreateChatTool) Description() string {
	return `⚠️ NOT YET IMPLEMENTED — This tool is reserved for future use.

Create a new conversation — either a private chat with a SubAgent or a group chat among multiple agents.

## Types
- "agent": Creates a new SubAgent session. The agent runs in the background.
  Use SendMessage to send tasks and receive responses.
- "group": Creates a group chat among multiple agents.
  Use SendMessage to broadcast messages to all members.

Currently returns "not implemented" error. Use the SubAgent tool to spawn agents instead.`
}

type CreateChatParams struct {
	// Type: "agent" or "group"
	Type string `json:"type" jsonschema:"required,description=Conversation type: agent or group"`
	// --- Agent params ---
	Role      string `json:"role,omitempty" jsonschema:"description=SubAgent role name (for agent type)"`
	Instance  string `json:"instance,omitempty" jsonschema:"description=Unique instance ID (for agent type)"`
	Task      string `json:"task,omitempty" jsonschema:"description=Initial task message (for agent type, optional)"`
	ModelTier string `json:"model_tier,omitempty" jsonschema:"description=Model tier: vanguard/swift/balance (for agent type)"`
	// --- Group params ---
	Members   []string `json:"members,omitempty" jsonschema:"description=Member addresses for group (e.g. [\"agent:reviewer\",\"agent:tester\"])"`
	MaxRounds int      `json:"max_rounds,omitempty" jsonschema:"description=Max conversation rounds for group (default 10)"`
}

func (t *CreateChatTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "type", Type: "string", Description: "Conversation type: agent or group", Required: true},
		{Name: "role", Type: "string", Description: "SubAgent role name (for agent type)"},
		{Name: "instance", Type: "string", Description: "Unique instance ID (for agent type)"},
		{Name: "task", Type: "string", Description: "Initial task message (for agent type, optional)"},
		{Name: "model_tier", Type: "string", Description: "Model tier: vanguard/swift/balance (for agent type)"},
		{Name: "members", Type: "array", Description: "Member addresses for group", Items: &llm.ToolParamItems{Type: "string"}},
		{Name: "max_rounds", Type: "integer", Description: "Max conversation rounds for group (default 10)"},
	}
}

func (t *CreateChatTool) Execute(ctx *ToolContext, raw string) (*ToolResult, error) {
	var params CreateChatParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if ctx.MessageSender == nil {
		return nil, fmt.Errorf("message sending not available in this context")
	}

	switch params.Type {
	case "agent":
		return t.createAgentChat(ctx, &params)
	case "group":
		return t.createGroupChat(ctx, &params)
	default:
		return nil, fmt.Errorf("unknown type %q: must be agent or group", params.Type)
	}
}

func (t *CreateChatTool) createAgentChat(ctx *ToolContext, params *CreateChatParams) (*ToolResult, error) {
	if params.Role == "" {
		return nil, fmt.Errorf("role is required for agent type")
	}
	if params.Instance == "" {
		return nil, fmt.Errorf("instance is required for agent type")
	}

	// TODO: Full AgentChannel integration coming in next PR.
	// Currently the SubAgent tool handles agent creation.
	return nil, fmt.Errorf("CreateChat(agent) not yet implemented — use the SubAgent tool to spawn agents")
}

func (t *CreateChatTool) createGroupChat(ctx *ToolContext, params *CreateChatParams) (*ToolResult, error) {
	if len(params.Members) < 2 {
		return nil, fmt.Errorf("group requires at least 2 members")
	}

	// TODO: Full GroupChannel integration coming in next PR.
	return nil, fmt.Errorf("CreateChat(group) not yet implemented — group chat via Channel is coming soon")
}
