package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"xbot/llm"
)

type SubAgentTool struct{}

func (t *SubAgentTool) Name() string {
	return "SubAgent"
}

func (t *SubAgentTool) Description() string {
	roles := ListSubAgentRoles()
	var roleLines []string
	for _, r := range roles {
		toolsInfo := ""
		if len(r.AllowedTools) > 0 {
			toolsInfo = fmt.Sprintf(" [tools: %s]", strings.Join(r.AllowedTools, ", "))
		}
		roleLines = append(roleLines, fmt.Sprintf("  - \"%s\": %s%s", r.Name, r.Description, toolsInfo))
	}
	roleList := strings.Join(roleLines, "\n")

	if roleList == "" {
		return `Delegate a task to a sub-agent that runs independently with its own tool set and context.
No predefined roles are available. Please configure agent roles in the .xbot/agents/ directory.`
	}

	return fmt.Sprintf(`Delegate a task to a sub-agent with a predefined role.
The sub-agent runs independently with its own tool set and context, specialized for the given role.
The sub-agent runs synchronously and returns its final response.

Parameters (JSON):
  - task: string (required), the task description for the sub-agent
  - role: string (required), the predefined role name to use

Available roles:
%s

Example: {"task": "Review the changes in core/agent.go for potential bugs", "role": "code-reviewer"}`, roleList)
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
		available := make([]string, 0)
		for _, r := range ListSubAgentRoles() {
			available = append(available, r.Name)
		}
		return nil, fmt.Errorf("role is required (available: %s)", strings.Join(available, ", "))
	}

	role, ok := GetSubAgentRole(params.Role)
	if !ok {
		available := make([]string, 0)
		for _, r := range ListSubAgentRoles() {
			available = append(available, r.Name)
		}
		return nil, fmt.Errorf("unknown role: %s (available: %s)", params.Role, strings.Join(available, ", "))
	}

	if ctx == nil || ctx.Manager == nil {
		return nil, fmt.Errorf("sub-agent capability not available")
	}

	result, err := ctx.Manager.RunSubAgent(ctx.Ctx, ctx.AgentID, params.Task, role.SystemPrompt, role.AllowedTools)
	if err != nil {
		return nil, fmt.Errorf("sub-agent failed: %w", err)
	}

	return NewResult(result), nil
}
