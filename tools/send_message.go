package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"xbot/llm"
)

// SendMessageTool sends a message to any addressable target.
// For groups, uses a meeting model: moderator controls who speaks via @mentions.
type SendMessageTool struct{}

func (t *SendMessageTool) Name() string { return "SendMessage" }

func (t *SendMessageTool) Description() string {
	return `Send a message to any addressable target (agent, group, or IM user).

## Addressing
- Agent: "agent:<role>/<instance>" (e.g., "agent:reviewer/cr1")
- Group: "group:<id>" (e.g., "group:g1")
- IM user (Feishu): "feishu:<open_id>" (e.g., "feishu:ou_xxx")

## Agent target
Blocks until reply (RPC), returns the agent's response.

## Group target — Meeting Mode
Group chats work like a moderated meeting:
- Only the moderator's messages with @mentions trigger agents to speak.
- Messages without @mentions are added to the discussion history but do NOT trigger anyone.
- Each @mentioned agent receives the FULL discussion history + the current message.
- The agent's response is added to the history for future reference.

Examples:
- SendMessage(to="group:g1", message="Let's discuss the API design.")
  → Adds moderator message to history. No agent triggered.
- SendMessage(to="group:g1", message="@agent:reviewer/r1 What do you think?")
  → Triggers agent:reviewer/r1 with full history + this question. Response added to history.
- SendMessage(to="group:g1", message="@agent:reviewer/r1 @agent:tester/t1 Please both review.")
  → Triggers both agents sequentially. Both see the same history. Both responses added.

## IM target
Sends message immediately (fire-and-forget).`
}

type SendMessageParams struct {
	To      string `json:"to" jsonschema:"required,description=Target address (agent:xxx, group:xxx, feishu:xxx)"`
	Message string `json:"message" jsonschema:"required,description=Message content to send"`
}

func (t *SendMessageTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "to", Type: "string", Description: "Target address (agent:xxx, group:xxx, feishu:xxx)", Required: true},
		{Name: "message", Type: "string", Description: "Message content to send. For groups, use @agent:role/instance to trigger specific agents.", Required: true},
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
	if len(channelName) > 6 && channelName[:6] == "agent:" {
		return t.sendToAgent(ctx, channelName, params.Message)
	}

	// Group addresses use meeting model
	if len(channelName) > 6 && channelName[:6] == "group:" {
		return t.sendToGroup(ctx, channelName, params.Message)
	}

	// IM addresses go through Dispatcher
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

// sendToAgent sends a message to a single agent via Dispatcher.
// The agent must have been registered as an AgentChannel (by SubAgent or CreateChat).
func (t *SendMessageTool) sendToAgent(ctx *ToolContext, addr, message string) (*ToolResult, error) {
	if ctx.MessageSender == nil {
		return nil, fmt.Errorf("message sending not available in this context")
	}
	result, err := ctx.MessageSender.SendMessage(addr, "", message)
	if err != nil {
		return nil, fmt.Errorf("agent send failed: %w", err)
	}
	if result == "" {
		return nil, fmt.Errorf("agent %s returned empty response (session may have ended)", addr)
	}
	return NewResult(result), nil
}

// sendToGroup handles the meeting model for group chats.
// Moderator messages without @mentions just add to history.
// @mentioned agents receive the full discussion history + the question.
func (t *SendMessageTool) sendToGroup(ctx *ToolContext, groupName, message string) (*ToolResult, error) {
	gs, ok := GetGroupState(groupName)
	if !ok {
		return nil, fmt.Errorf("group %q not found (create it with CreateChat first)", groupName)
	}
	if gs.Closed {
		return nil, fmt.Errorf("group %q is closed", groupName)
	}

	// Identify sender as the moderator
	sender := "moderator"

	// Parse @mentions from the message
	mentions := parseMentions(message)

	// Always add moderator message to history
	gs.AddMessage(sender, message, false)

	// No @mentions → just record, don't trigger anyone
	if len(mentions) == 0 {
		historyLen := len(gs.Messages)
		return NewResult(fmt.Sprintf("Message added to group %s discussion (history: %d messages). No agents @mentioned, so no one was triggered.", groupName, historyLen)), nil
	}

	// Trigger each @mentioned agent sequentially via Dispatcher
	if ctx.MessageSender == nil {
		return nil, fmt.Errorf("message sending not available in this context")
	}

	var responses []string
	for _, agentAddr := range mentions {
		if !gs.IsMember(agentAddr) {
			responses = append(responses, fmt.Sprintf("[ERROR] %s is not a member of this group", agentAddr))
			continue
		}

		// Build the prompt with full discussion history
		history := gs.GetHistory()
		prompt := fmt.Sprintf(`You are participating in a group discussion. Here is the full conversation so far:

%s

---

The moderator just @mentioned you. Please respond to their message. Stay focused on the topic and provide your analysis/opinion.`, history)

		// Send via Dispatcher (AgentChannel)
		result, err := ctx.MessageSender.SendMessage(agentAddr, "", prompt)
		if err != nil {
			responses = append(responses, fmt.Sprintf("[ERROR] %s: %v", agentAddr, err))
			continue
		}

		// Add agent's response to group history
		gs.AddMessage(agentAddr, result, false)
		responses = append(responses, fmt.Sprintf("[%s]:\n%s", agentAddr, result))
	}

	// Check round limit
	gs.mu.Lock()
	gs.Round++
	round := gs.Round
	maxRounds := gs.MaxRounds
	gs.mu.Unlock()

	if maxRounds > 0 && round >= maxRounds {
		gs.Close(fmt.Sprintf("max rounds (%d) reached", maxRounds))
	}

	summary := fmt.Sprintf("Group %s — round %d/%d\nModerator: %s\n\n%s",
		groupName, round, maxRounds, message, strings.Join(responses, "\n\n---\n\n"))

	if gs.Closed {
		summary += "\n\n[Group closed: max rounds reached]"
	}

	return NewResult(summary), nil
}

// parseMentions extracts @agent:role/instance addresses from a message.
// Returns unique addresses in order of first appearance.
func parseMentions(message string) []string {
	var result []string
	seen := make(map[string]bool)
	// Find all @agent:xxx/yyy patterns
	for i := 0; i < len(message); i++ {
		if message[i] == '@' && i+6 < len(message) && message[i+1:i+7] == "agent:" {
			// Find end of address (whitespace or end of string)
			end := len(message)
			for j := i + 7; j < len(message); j++ {
				if message[j] == ' ' || message[j] == '\n' || message[j] == '\t' || message[j] == '\r' {
					end = j
					break
				}
			}
			addr := message[i+1 : end] // strip the @
			if addr != "" && !seen[addr] {
				seen[addr] = true
				result = append(result, addr)
			}
		}
	}
	return result
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
