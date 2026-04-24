package hooks

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// MCPExecutor calls MCP tools as hook handlers. It delegates the actual tool
// invocation to an injected function (typically provided by the MCP Manager at
// initialisation time).
type MCPExecutor struct {
	// callTool invokes an MCP tool on a given server.
	// Parameters: serverName, toolName, input.
	// Returns: output, isError (tool-reported error flag), err (transport error).
	callTool func(ctx context.Context, serverName, toolName string, input map[string]any) (map[string]any, bool, error)
}

// NewMCPExecutor creates a new MCPExecutor. If callTool is nil, every call to
// Execute will return an error.
func NewMCPExecutor(callTool func(ctx context.Context, serverName, toolName string, input map[string]any) (map[string]any, bool, error)) *MCPExecutor {
	return &MCPExecutor{callTool: callTool}
}

// Type returns "mcp_tool".
func (e *MCPExecutor) Type() string { return "mcp_tool" }

// varPattern matches ${...} variable references.
var varPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// Execute interpolates variables in def.Input, calls the MCP tool, and returns
// the result.
//
// Execution flow:
//  1. Verify callTool is not nil.
//  2. Interpolate ${...} variables in def.Input.
//  3. Call callTool(ctx, def.Server, def.Tool, interpolatedInput).
//  4. On isError=true → non-blocking (Decision="allow", Reason holds error).
//  5. On success → parse output into Result; use "decision" key if present.
func (e *MCPExecutor) Execute(ctx context.Context, def *HookDef, event Event) (*Result, error) {
	// 1. Guard: nil callTool.
	if e.callTool == nil {
		return nil, fmt.Errorf("mcp executor: callTool is nil")
	}

	// 2. Interpolate input.
	input := interpolateInput(def.Input, event)

	// 3. Call MCP tool.
	output, isError, err := e.callTool(ctx, def.Server, def.Tool, input)
	if err != nil {
		return nil, err
	}

	// 4. Tool-reported error → non-blocking.
	if isError {
		reason := ""
		if output != nil {
			if r, ok := output["reason"].(string); ok {
				reason = r
			} else if r, ok := output["error"].(string); ok {
				reason = r
			}
		}
		return &Result{
			Decision: "allow",
			Reason:   reason,
		}, nil
	}

	// 5. Success — build Result from output.
	result := &Result{Decision: "allow"}
	if output != nil {
		if v, ok := output["decision"].(string); ok {
			result.Decision = v
		}
		if v, ok := output["reason"].(string); ok {
			result.Reason = v
		}
		if v, ok := output["context"].(string); ok {
			result.Context = v
		}
		if v, ok := output["updatedInput"].(map[string]any); ok {
			result.UpdatedInput = v
		}
	}
	return result, nil
}

// interpolateInput replaces ${...} variable references in string values of the
// input map. Non-string values are preserved as-is.
//
// Variable resolution order:
//   - ${tool_input.X} → event.ToolInput()["X"]
//   - ${tool_name}    → event.ToolName()
//   - ${session_id}   → event.Payload()["session_id"]
//   - General ${key}  → event.ToolInput()["key"], then event.Payload()["key"]
func interpolateInput(input map[string]any, event Event) map[string]any {
	if input == nil {
		return nil
	}

	out := make(map[string]any, len(input))
	for k, v := range input {
		s, ok := v.(string)
		if !ok {
			out[k] = v
			continue
		}
		out[k] = varPattern.ReplaceAllStringFunc(s, func(match string) string {
			varName := varPattern.FindStringSubmatch(match)[1]

			// ${tool_input.X} → look up X in ToolInput().
			if strings.HasPrefix(varName, "tool_input.") {
				key := strings.TrimPrefix(varName, "tool_input.")
				if toolInput := event.ToolInput(); toolInput != nil {
					if v, found := toolInput[key]; found {
						return fmt.Sprintf("%v", v)
					}
				}
				return match // unresolved
			}

			// ${tool_name} → event.ToolName().
			if varName == "tool_name" {
				return event.ToolName()
			}

			// General: try ToolInput first, then Payload.
			if toolInput := event.ToolInput(); toolInput != nil {
				if v, found := toolInput[varName]; found {
					return fmt.Sprintf("%v", v)
				}
			}
			if payload := event.Payload(); payload != nil {
				if v, found := payload[varName]; found {
					return fmt.Sprintf("%v", v)
				}
			}
			return match // unresolved
		})
	}
	return out
}
