package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"xbot/llm"
)

// LLM configuration for local LLM mode.
// Set via command-line flags or environment variables.
var (
	llmClient llm.LLM
	llmModels []string
)

// LLMProxyRequest mirrors the server-side LLM proxy request.
type LLMProxyRequest struct {
	Model        string            `json:"model"`
	Messages     []llm.ChatMessage `json:"messages"`
	Tools        []llm.ToolDefJSON `json:"tools,omitempty"`
	ThinkingMode string            `json:"thinking_mode,omitempty"`
}

// initLLMClient initializes the LLM client from flags/env.
// Returns nil if LLM is not configured (pure sandbox mode).
func initLLMClient(provider, baseURL, apiKey, model string) error {
	if provider == "" || apiKey == "" {
		log.Printf("  Local LLM: not configured (pure sandbox mode)")
		return nil
	}

	switch provider {
	case "openai":
		cfg := llm.OpenAIConfig{
			APIKey:       apiKey,
			BaseURL:      baseURL,
			DefaultModel: model,
		}
		llmClient = llm.NewOpenAILLM(cfg)
		llmModels = llmClient.ListModels()

	case "anthropic":
		cfg := llm.AnthropicConfig{
			APIKey:       apiKey,
			BaseURL:      baseURL,
			DefaultModel: model,
		}
		llmClient = llm.NewAnthropicLLM(cfg)
		llmModels = llmClient.ListModels()

	default:
		return fmt.Errorf("unsupported LLM provider: %s", provider)
	}

	log.Printf("  Local LLM: configured provider=%s model=%s (%d models available)",
		provider, model, len(llmModels))
	return nil
}

// handleLLMGenerate handles "llm_generate" requests from the server.
func handleLLMGenerate(msg RunnerMessage) *RunnerMessage {
	if llmClient == nil {
		return makeError(msg.ID, "ENOTSUP", "local LLM not configured on this runner")
	}

	var req LLMProxyRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", "invalid llm_generate request: "+err.Error())
	}

	// Convert ToolDefJSON back to ChatMessage format for the LLM call.
	var tools []llm.ToolDefinition
	if len(req.Tools) > 0 {
		tools = make([]llm.ToolDefinition, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = &toolDefAdapter{name: t.Name, desc: t.Description, params: t.Parameters}
		}
	}

	// Use a generous timeout — LLM calls can be slow.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	resp, err := llmClient.Generate(ctx, req.Model, req.Messages, tools, req.ThinkingMode)
	if err != nil {
		return makeError(msg.ID, "EIO", "LLM generate failed: "+err.Error())
	}

	return makeResponse(msg.ID, "llm_response", resp)
}

// handleLLMModels handles "llm_models" requests from the server.
func handleLLMModels(msg RunnerMessage) *RunnerMessage {
	if llmClient == nil {
		return makeError(msg.ID, "ENOTSUP", "local LLM not configured on this runner")
	}

	return makeResponse(msg.ID, "llm_models_response", llm.LLMListModelsResponse{
		Models: llmModels,
	})
}

// toolDefAdapter adapts ToolDefJSON to llm.ToolDefinition interface.
type toolDefAdapter struct {
	name   string
	desc   string
	params []llm.ToolParam
}

func (t *toolDefAdapter) Name() string                { return t.name }
func (t *toolDefAdapter) Description() string         { return t.desc }
func (t *toolDefAdapter) Parameters() []llm.ToolParam { return t.params }
