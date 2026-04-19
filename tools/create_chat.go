package tools

import (
	"encoding/json"
	"fmt"
	"sync/atomic"

	"xbot/llm"
)

// groupCounter generates unique IDs for group channels.
var groupCounter atomic.Int64

// CreateChatTool creates a new conversation (agent private chat or group chat).
// Agent type delegates to InteractiveSubAgentManager (existing SubAgent infrastructure).
// Group type creates a GroupChannel and registers it with the Dispatcher.
type CreateChatTool struct{}

func (t *CreateChatTool) Name() string { return "CreateChat" }

func (t *CreateChatTool) Description() string {
	return `Create a new conversation — either a private chat with a SubAgent or a group chat among multiple agents.

## Types
- "agent": Creates a new interactive SubAgent session. The agent runs in the background.
  Use SendMessage with the agent address to send follow-up tasks.
  The agent auto-cleans when unloaded or when the parent session ends.
- "group": Creates a group chat among multiple agents.
  Use SendMessage with the group address to broadcast to all members.
  Members must be already-running interactive SubAgents.

## Agent type
- Spawns an interactive SubAgent (same as SubAgent tool with interactive=true)
- Returns an address like "agent:<role>/<instance>" for use with SendMessage
- The SubAgent runs in background, processing messages via SendMessage

## Group type
- Creates a broadcast group among SubAgents
- Members are specified as addresses (e.g., ["agent:reviewer/cr1", "agent:tester/ts1"])
- Returns a group address like "group:<id>" for use with SendMessage
- Group auto-closes after max_rounds (default 10)

## Note
CreateChat(agent) is equivalent to the SubAgent tool's interactive mode.
For group chat with agent members, use the SubAgent tool with multiple interactive sessions
and coordinate via SendMessage to each agent individually.`
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

	im, ok := ctx.Manager.(InteractiveSubAgentManager)
	if !ok {
		return nil, fmt.Errorf("interactive SubAgent not supported in this context")
	}

	// Load role definition
	role, ok := loadRoleFromCtx(ctx, params.Role)
	if !ok {
		return nil, fmt.Errorf("unknown role: %s, see <available_agents> in system prompt", params.Role)
	}

	effectiveModel := params.ModelTier
	if effectiveModel == "" {
		effectiveModel = role.Model
	}

	// Spawn interactive SubAgent session
	task := params.Task
	if task == "" {
		task = "Ready. Waiting for instructions."
	}

	result, err := im.SpawnInteractive(ctx, task, params.Role, role.SystemPrompt, role.AllowedTools, role.Capabilities, params.Instance, effectiveModel)
	if err != nil {
		return nil, fmt.Errorf("failed to spawn SubAgent %q (%s): %w", params.Role, params.Instance, err)
	}

	addr := "agent:" + params.Role + "/" + params.Instance
	return NewResult(fmt.Sprintf("Created agent chat: %s\n%s\n\nUse SendMessage(to=\"%s\", message=\"...\") to send tasks.", addr, result, addr)), nil
}

func (t *CreateChatTool) createGroupChat(ctx *ToolContext, params *CreateChatParams) (*ToolResult, error) {
	if len(params.Members) < 2 {
		return nil, fmt.Errorf("group requires at least 2 members, got %d", len(params.Members))
	}

	if ctx.CreateGroupFn == nil {
		return nil, fmt.Errorf("group chat not available in this context")
	}

	maxRounds := params.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 10
	}

	// Generate unique group ID
	groupID := fmt.Sprintf("g%d", groupCounter.Add(1))

	groupName, err := ctx.CreateGroupFn(groupID, params.Members, maxRounds)
	if err != nil {
		return nil, fmt.Errorf("failed to create group: %w", err)
	}

	return NewResult(fmt.Sprintf("Created group chat: %s\nMembers: %v\nMax rounds: %d\n\nUse SendMessage(to=\"%s\", message=\"...\") to broadcast.", groupName, params.Members, maxRounds, groupName)), nil
}
