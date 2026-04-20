package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"xbot/llm"
)

// groupCounter generates unique IDs for group channels.
var groupCounter atomic.Int64

// CreateChatTool creates a new conversation (agent private chat or group chat).
type CreateChatTool struct{}

func (t *CreateChatTool) Name() string { return "CreateChat" }

func (t *CreateChatTool) Description() string {
	return `Create a new conversation — either a private chat with a SubAgent or a moderated group chat.

## Agent type
Creates an interactive SubAgent session (same as SubAgent tool with interactive=true).
Returns an address like "agent:<role>/<instance>" for use with SendMessage.
The SubAgent runs in background, processing messages via SendMessage.

## Group type — Meeting Mode
Creates a moderated group discussion among multiple SubAgents.
- Members are specified as agent addresses (e.g., ["agent:reviewer/cr1", "agent:tester/ts1"])
- Returns a group address like "group:<id>" for use with SendMessage
- The group works like a meeting: the moderator (you) controls who speaks
- Messages without @mentions just add to the discussion history (no agent triggered)
- Use @agent:role/instance in your message to trigger specific agents to respond
- Triggered agents see the FULL discussion history before responding
- Group auto-closes after max_rounds moderator messages with @mentions (default 10)

## Example workflow
1. CreateChat(type="group", members=["agent:reviewer/r1", "agent:tester/t1"])
2. SendMessage(to="group:g1", message="Let's discuss the API design.") → no agents triggered
3. SendMessage(to="group:g1", message="@agent:reviewer/r1 What's your opinion?") → reviewer responds
4. SendMessage(to="group:g1", message="@agent:tester/t1 Any concerns about testability?") → tester responds with full context`
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
	if !ok || im == nil {
		return nil, fmt.Errorf("interactive SubAgent not supported in this context (type %T)", ctx.Manager)
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

	// Register AgentChannel in Dispatcher so SendMessage(agent://) can route to it
	addr := "agent:" + params.Role + "/" + params.Instance
	if ctx.RegisterAgentChannel != nil {
		sendFn := func(sendCtx context.Context, msg string) (string, error) {
			return im.SendInteractive(ctx, msg, params.Role, role.SystemPrompt, role.AllowedTools, role.Capabilities, params.Instance, effectiveModel)
		}
		if regErr := ctx.RegisterAgentChannel(addr, sendFn); regErr != nil {
			result += fmt.Sprintf("\n\nWarning: AgentChannel registration failed: %v", regErr)
		}
	}

	return NewResult(fmt.Sprintf("Created agent chat: %s\n%s\n\nUse SendMessage(to=\"%s\", message=\"...\") to send tasks.", addr, result, addr)), nil
}

func (t *CreateChatTool) createGroupChat(ctx *ToolContext, params *CreateChatParams) (*ToolResult, error) {
	if len(params.Members) < 2 {
		return nil, fmt.Errorf("group requires at least 2 members, got %d", len(params.Members))
	}

	maxRounds := params.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 10
	}

	// Generate unique group ID
	groupID := fmt.Sprintf("g%d", groupCounter.Add(1))
	groupName := "group:" + groupID

	// Create group state directly (no external callback needed)
	CreateGroupState(groupID, "moderator", params.Members, maxRounds)

	return NewResult(fmt.Sprintf(
		"Created group chat: %s\nMembers: %v\nMax rounds: %d\n\n"+
			"Usage:\n"+
			"- SendMessage(to=\"%s\", message=\"...\") → add to discussion (no agent triggered)\n"+
			"- SendMessage(to=\"%s\", message=\"@agent:role/instance ...\") → trigger specific agent",
		groupName, params.Members, maxRounds, groupName, groupName)), nil
}
