package tools

import (
	"encoding/json"
	"fmt"
	"xbot/llm"
)

type SubAgentTool struct{}

func (t *SubAgentTool) Name() string {
	return "SubAgent"
}

func (t *SubAgentTool) Description() string {
	return `Delegate a task to a sub-agent with a predefined role.
The sub-agent runs independently with its own tool set and context, specialized for the given role.
The sub-agent runs synchronously and returns its final response.

Parameters (JSON):
  - task: string (required), the task description for the sub-agent
  - role: string (required), the predefined role name to use

Available roles are listed in the <available_agents> section of the system prompt.

Example: {"task": "Review the changes in core/agent.go for potential bugs", "role": "code-reviewer"}`
}

func (t *SubAgentTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task", Type: "string", Description: "The task description for the sub-agent to execute", Required: true},
		{Name: "role", Type: "string", Description: "Predefined role name (e.g. code-reviewer)", Required: true},
	}
}

func (t *SubAgentTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Task string `json:"task"`
		Role string `json:"role"`
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

	role, ok := GetSubAgentRole(params.Role)
	if !ok {
		return nil, fmt.Errorf("unknown role: %s, see <available_agents> in system prompt", params.Role)
	}

	if ctx == nil || ctx.Manager == nil {
		return nil, fmt.Errorf("sub-agent capability not available")
	}

	result, err := ctx.Manager.RunSubAgent(ctx, params.Task, role.SystemPrompt, role.AllowedTools)
	if err != nil {
		return nil, fmt.Errorf("sub-agent failed: %w", err)
	}

	return NewResult(result), nil
}
