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
		roleLines = append(roleLines, fmt.Sprintf("  - \"%s\": %s", r.Name, r.Description))
	}
	roleList := strings.Join(roleLines, "\n")

	return fmt.Sprintf(`Delegate a task to a sub-agent that runs independently with its own tool set and context.
Use this when a task is complex, self-contained, or benefits from isolation (e.g., research, code generation, file operations on a separate concern).
The sub-agent has access to all standard tools (Bash, Read, Edit, Glob, Grep, etc.) but cannot create further sub-agents.
The sub-agent runs synchronously and returns its final response.

Parameters (JSON):
  - task: string (required), the task description for the sub-agent
  - role: string (optional), use a predefined role with specialized system prompt. If set, system_prompt is ignored.
  - system_prompt: string (optional), custom system prompt for the sub-agent. If empty and no role specified, uses default system prompt.

Available roles:
%s

Example: {"task": "Review the changes in core/agent.go for potential bugs", "role": "code-reviewer"}
Example: {"task": "Write unit tests for tools/subagent.go", "role": "test-writer"}
Example: {"task": "Read all Go files in the tools/ directory and write a summary of what each tool does."}
Example: {"task": "Find and fix all TODO comments in the codebase", "system_prompt": "You are a code cleanup specialist."}`, roleList)
}

func (t *SubAgentTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "task", Type: "string", Description: "The task description for the sub-agent to execute", Required: true},
		{Name: "role", Type: "string", Description: "Predefined role name (e.g. code-reviewer, test-writer, refactor, doc-writer, security-auditor, bug-finder). Overrides system_prompt if set.", Required: false},
		{Name: "system_prompt", Type: "string", Description: "Optional custom system prompt for the sub-agent (ignored if role is set)", Required: false},
	}
}

func (t *SubAgentTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Task         string `json:"task"`
		Role         string `json:"role"`
		SystemPrompt string `json:"system_prompt"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Task == "" {
		return nil, fmt.Errorf("task is required")
	}

	if ctx == nil || ctx.Manager == nil {
		return nil, fmt.Errorf("sub-agent capability not available")
	}

	systemPrompt := params.SystemPrompt
	if params.Role != "" {
		role, ok := GetSubAgentRole(params.Role)
		if !ok {
			available := make([]string, 0)
			for _, r := range ListSubAgentRoles() {
				available = append(available, r.Name)
			}
			return nil, fmt.Errorf("unknown role: %s (available: %s)", params.Role, strings.Join(available, ", "))
		}
		systemPrompt = role.SystemPrompt
	}

	result, err := ctx.Manager.RunSubAgent(ctx.Ctx, ctx.AgentID, params.Task, systemPrompt)
	if err != nil {
		return nil, fmt.Errorf("sub-agent failed: %w", err)
	}

	return NewResult(result), nil
}
