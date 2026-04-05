package llm

import (
	"context"
	"fmt"
	"time"
)

// ProxyLLM forwards LLM requests to a remote runner via the sandbox protocol.
// The server does not call any LLM API directly — it serializes the request,
// sends it through the remote sandbox, and deserializes the response.
//
// Usage: inject ProxyLLM as the LLM provider when the user's active runner
// is in "local-llm" mode.
type ProxyLLM struct {
	// GenerateFunc is the function that sends the request to the runner
	// and returns the serialized LLMResponse JSON. Injected from tools/sandbox.
	GenerateFunc func(ctx context.Context, userID, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error)
	// ListModelsFunc returns available models on the runner side.
	ListModelsFunc func() []string
}

// Ensure ProxyLLM implements LLM interface.
var _ LLM = (*ProxyLLM)(nil)

// Generate forwards the LLM request to the remote runner.
func (p *ProxyLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	if p.GenerateFunc == nil {
		return nil, fmt.Errorf("proxy LLM: no generate function configured")
	}
	return p.GenerateFunc(ctx, "", model, messages, tools, thinkingMode)
}

// ListModels returns the available models from the remote runner.
func (p *ProxyLLM) ListModels() []string {
	if p.ListModelsFunc == nil {
		return nil
	}
	return p.ListModelsFunc()
}

// --- Protocol types for LLM proxy over runner protocol ---

// LLMProxyRequest is sent from server to runner via the "llm_generate" protocol message.
type LLMProxyRequest struct {
	Model        string        `json:"model"`
	Messages     []ChatMessage `json:"messages"`
	Tools        []ToolDefJSON `json:"tools,omitempty"`
	ThinkingMode string        `json:"thinking_mode,omitempty"`
}

// LLMProxyResponse is the runner's response to an LLM proxy request.
type LLMProxyResponse struct {
	Content          string       `json:"content"`
	ReasoningContent string       `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall   `json:"tool_calls,omitempty"`
	FinishReason     FinishReason `json:"finish_reason"`
	Usage            TokenUsage   `json:"usage"`
}

// LLMListModelsResponse is the runner's response to a list_models request.
type LLMListModelsResponse struct {
	Models []string `json:"models"`
}

// ToolDefJSON is a serializable tool definition (avoids interface{} for JSON marshaling).
type ToolDefJSON struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  []ToolParam `json:"parameters"`
}

// SerializeTools converts ToolDefinition interface slice to serializable JSON.
func SerializeTools(tools []ToolDefinition) []ToolDefJSON {
	if tools == nil {
		return nil
	}
	result := make([]ToolDefJSON, len(tools))
	for i, t := range tools {
		result[i] = ToolDefJSON{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		}
	}
	return result
}

// ProxyRequestTimeout is the default timeout for proxy LLM requests.
// LLM generation can be slow, so this is generous.
const ProxyRequestTimeout = 300 * time.Second
