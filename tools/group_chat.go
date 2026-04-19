package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
)

// GroupChatTool manages group chats among multiple SubAgents.
// Allows creating groups, broadcasting messages, and closing groups.
type GroupChatTool struct{}

func (t *GroupChatTool) Name() string { return "GroupChat" }

func (t *GroupChatTool) Description() string {
	return `Manage group chats among multiple SubAgents for collaborative tasks.

Supports three actions:
- "create": Create a new group chat with specified members. All members must be active interactive SubAgents.
- "broadcast": Send a message from the coordinator to all group members.
- "close": Close a group chat and release resources.

## When to use
- Multiple SubAgents need to collaboratively discuss a topic (e.g., roundtable review)
- SubAgents need to see each other's opinions and respond
- Complex decision-making requiring multi-agent debate

## Anti-storm mechanisms
- MaxRounds limits total conversation rounds (default: 10)
- Each member can speak at most once per round
- Coordinator (you) can terminate the group at any time`
}

type GroupChatParams struct {
	// Action: "create", "broadcast", or "close"
	Action string `json:"action" jsonschema:"required,description=Action to perform: create/broadcast/close"`

	// --- Create action params ---
	// GroupID is a unique identifier for the group (required for all actions).
	GroupID string `json:"group_id,omitempty" jsonschema:"description=Unique group identifier (required for all actions)"`
	// MemberAddresses is a list of SubAgent mailbox addresses to include.
	MemberAddresses []string `json:"member_addresses,omitempty" jsonschema:"description=List of SubAgent addresses to include in the group (for create action)"`
	// MaxRounds limits the number of conversation rounds.
	MaxRounds int `json:"max_rounds,omitempty" jsonschema:"description=Maximum conversation rounds (default 10, for create action)"`

	// --- Broadcast action params ---
	// Message is the content to broadcast to all group members.
	Message string `json:"message,omitempty" jsonschema:"description=Message content to broadcast (for broadcast action)"`

	// --- Close action params ---
	// Reason is the reason for closing the group.
	Reason string `json:"reason,omitempty" jsonschema:"description=Reason for closing the group (for close action)"`
}

func (t *GroupChatTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action to perform: create/broadcast/close", Required: true},
		{Name: "group_id", Type: "string", Description: "Unique group identifier (required for all actions)"},
		{Name: "member_addresses", Type: "array", Description: "List of SubAgent addresses to include in the group (for create action)", Items: &llm.ToolParamItems{Type: "string"}},
		{Name: "max_rounds", Type: "integer", Description: "Maximum conversation rounds, default 10 (for create action)"},
		{Name: "message", Type: "string", Description: "Message content to broadcast (for broadcast action)"},
		{Name: "reason", Type: "string", Description: "Reason for closing the group (for close action)"},
	}
}

func (t *GroupChatTool) Execute(ctx *ToolContext, raw string) (*ToolResult, error) {
	var params GroupChatParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if ctx.PostOffice == nil {
		return nil, fmt.Errorf("PostOffice not available: agent messaging not enabled")
	}

	switch params.Action {
	case "create":
		return t.createGroup(ctx, &params)
	case "broadcast":
		return t.broadcastMessage(ctx, &params)
	case "close":
		return t.closeGroup(ctx, &params)
	default:
		return nil, fmt.Errorf("unknown action %q: must be create/broadcast/close", params.Action)
	}
}

func (t *GroupChatTool) createGroup(ctx *ToolContext, params *GroupChatParams) (*ToolResult, error) {
	if params.GroupID == "" {
		return nil, fmt.Errorf("group_id is required")
	}
	if len(params.MemberAddresses) == 0 {
		return nil, fmt.Errorf("member_addresses is required for create action")
	}

	maxRounds := params.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 10
	}

	// Verify all members exist
	var missing []string
	for _, addr := range params.MemberAddresses {
		mb, ok := ctx.PostOffice.LookupMailbox(addr)
		if !ok || mb == nil {
			missing = append(missing, addr)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the following members are not registered (create them as interactive SubAgents first): %s", strings.Join(missing, ", "))
	}

	coordinatorAddr := ctx.AgentID // the calling agent is the coordinator

	def := PostOfficeGroupDef{
		ID:              params.GroupID,
		MemberAddresses: params.MemberAddresses,
		CoordinatorAddr: coordinatorAddr,
		MaxRounds:       maxRounds,
	}

	if err := ctx.PostOffice.RegisterGroup(def); err != nil {
		return nil, fmt.Errorf("failed to create group: %w", err)
	}

	return NewResult(fmt.Sprintf("Group %q created with %d members, max %d rounds. Use action=\"broadcast\" to start the conversation.",
		params.GroupID, len(params.MemberAddresses), maxRounds)), nil
}

func (t *GroupChatTool) broadcastMessage(ctx *ToolContext, params *GroupChatParams) (*ToolResult, error) {
	if params.GroupID == "" {
		return nil, fmt.Errorf("group_id is required")
	}
	if params.Message == "" {
		return nil, fmt.Errorf("message is required for broadcast action")
	}

	group, ok := ctx.PostOffice.LookupGroup(params.GroupID)
	if !ok || group == nil {
		return nil, fmt.Errorf("group %q not found", params.GroupID)
	}
	if group.IsClosed() {
		return nil, fmt.Errorf("group %q is already closed", params.GroupID)
	}

	fromAddr := ctx.AgentID
	if err := group.Broadcast(fromAddr, params.Message); err != nil {
		return nil, fmt.Errorf("broadcast failed: %w", err)
	}

	round := group.CurrentRound()
	return NewResult(fmt.Sprintf("Message broadcast to group %q (round %d/%d).",
		params.GroupID, round, group.MaxRounds())), nil
}

func (t *GroupChatTool) closeGroup(ctx *ToolContext, params *GroupChatParams) (*ToolResult, error) {
	if params.GroupID == "" {
		return nil, fmt.Errorf("group_id is required")
	}

	group, ok := ctx.PostOffice.LookupGroup(params.GroupID)
	if !ok || group == nil {
		return nil, fmt.Errorf("group %q not found", params.GroupID)
	}

	reason := params.Reason
	if reason == "" {
		reason = "coordinator closed the group"
	}
	group.Close(reason)
	ctx.PostOffice.UnregisterGroup(params.GroupID)

	return NewResult(fmt.Sprintf("Group %q closed: %s", params.GroupID, reason)), nil
}
