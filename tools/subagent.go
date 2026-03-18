package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"xbot/llm"
)

// InteractiveSubAgentManager 扩展 SubAgentManager，支持 interactive mode。
// agent 包的 Agent 实现此接口（如果是 nil 则不支持 interactive）。
type InteractiveSubAgentManager interface {
	SubAgentManager
	// SpawnInteractive 创建/复用 interactive SubAgent session 并执行任务。
	SpawnInteractive(ctx *ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps SubAgentCapabilities) (string, error)
	// SendInteractive 向已有的 interactive session 发送消息。
	SendInteractive(ctx *ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps SubAgentCapabilities) (string, error)
	// UnloadInteractive 结束 interactive session（巩固记忆 + 清理）。
	UnloadInteractive(ctx *ToolContext, roleName string) error
}

type SubAgentTool struct{}

func (t *SubAgentTool) Name() string {
	return "SubAgent"
}

func (t *SubAgentTool) Description() string {
	return `Delegate a task to a sub-agent with a predefined role.
The sub-agent runs independently with its own tool set and context, specialized for the given role.

## One-shot mode (default)
SubAgent(task, role) — runs once, returns result, no state retained.

## Interactive mode
Persistent multi-turn session. Create once, send multiple messages, unload when done.

| Call | Behavior |
|------|----------|
| SubAgent(task, role, interactive=true) | Create or reuse an interactive session |
| SubAgent(task, role, action="send") | Send a new message to an existing session |
| SubAgent(task, role, action="unload") | End session + memorize |

Parameters (JSON):
  - task: string (required), the task description
  - role: string (required), the predefined role name
  - interactive: bool (optional), create/reuse interactive session
  - action: string (optional), "send" or "unload" for interactive session control

Available roles are listed in the <available_agents> section of the system prompt.`
}

func (t *SubAgentTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task", Type: "string", Description: "The task description for the sub-agent to execute", Required: true},
		{Name: "role", Type: "string", Description: "Predefined role name (e.g. code-reviewer)", Required: true},
		{Name: "interactive", Type: "boolean", Description: "Create or reuse an interactive session for multi-turn conversation"},
		{Name: "action", Type: "string", Description: `Interactive session action: "send" (send message to existing session) or "unload" (end session and memorize)`},
	}
}

func (t *SubAgentTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Task        string `json:"task"`
		Role        string `json:"role"`
		Interactive bool   `json:"interactive"`
		Action      string `json:"action"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Task == "" {
		return nil, fmt.Errorf("task is required")
	}

	if params.Role == "" {
		return nil, fmt.Errorf("role is required, see <available_agents> in system prompt")
	}

	// Ensure global agents are synced to workspace
	EnsureSynced(ctx)

	// Search order: user private > synced global > host global
	// Use OriginUserID for user private agents (original user's directory)
	var userAgentDirs []string
	originUserID := ctx.OriginUserID
	if originUserID == "" {
		originUserID = ctx.SenderID // fallback：兼容旧数据
	}
	if ctx != nil && originUserID != "" && ctx.WorkingDir != "" {
		userAgentDirs = append(userAgentDirs, UserAgentsRoot(ctx.WorkingDir, originUserID))
	}
	if ctx != nil && ctx.WorkspaceRoot != "" {
		userAgentDirs = append(userAgentDirs, filepath.Join(ctx.WorkspaceRoot, ".agents"))
	}
	role, ok := GetSubAgentRole(params.Role, userAgentDirs...)
	if !ok {
		return nil, fmt.Errorf("unknown role: %s, see <available_agents> in system prompt", params.Role)
	}

	if ctx == nil || ctx.Manager == nil {
		return nil, fmt.Errorf("sub-agent capability not available")
	}

	// Interactive mode handling
	if params.Interactive || params.Action != "" {
		im, ok := ctx.Manager.(InteractiveSubAgentManager)
		if !ok {
			return nil, fmt.Errorf("interactive mode not supported by current agent")
		}

		switch params.Action {
		case "unload":
			if err := im.UnloadInteractive(ctx, params.Role); err != nil {
				return nil, err
			}
			return NewResult(fmt.Sprintf("Interactive session for role %q unloaded successfully.", params.Role)), nil

		case "send":
			if params.Task == "" {
				return nil, fmt.Errorf("task is required for action=\"send\"")
			}
			result, err := im.SendInteractive(ctx, params.Task, params.Role, role.SystemPrompt, role.AllowedTools, role.Capabilities)
			if err != nil {
				return nil, fmt.Errorf("interactive send failed: %w", err)
			}
			return NewResult(result), nil

		default:
			// action="" + interactive=true → spawn/reuse
			result, err := im.SpawnInteractive(ctx, params.Task, params.Role, role.SystemPrompt, role.AllowedTools, role.Capabilities)
			if err != nil {
				return nil, fmt.Errorf("interactive spawn failed: %w", err)
			}
			return NewResult(result), nil
		}
	}

	// Default: one-shot mode
	result, err := ctx.Manager.RunSubAgent(ctx, params.Task, role.SystemPrompt, role.AllowedTools, role.Capabilities, params.Role)
	if err != nil {
		return nil, fmt.Errorf("sub-agent failed: %w", err)
	}

	return NewResult(result), nil
}
