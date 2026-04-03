package llm

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

// modelToEncoding maps model names to their tokenizer model constants
var modelToEncoding = map[string]tokenizer.Model{
	// GPT-4 series
	"gpt-4":                  tokenizer.GPT4,
	"gpt-4-0314":             tokenizer.GPT4,
	"gpt-4-0613":             tokenizer.GPT4,
	"gpt-4-32k":              tokenizer.GPT4, // 32k uses same encoding as GPT4
	"gpt-4-32k-0314":         tokenizer.GPT4,
	"gpt-4-32k-0613":         tokenizer.GPT4,
	"gpt-4-turbo":            tokenizer.GPT4,
	"gpt-4-turbo-2024-04-09": tokenizer.GPT4,
	"gpt-4o":                 tokenizer.GPT4o,
	"gpt-4o-2024-05-13":      tokenizer.GPT4o,
	"gpt-4o-mini":            tokenizer.GPT4o,
	"gpt-4o-mini-2024-07-18": tokenizer.GPT4o,

	// GPT-3.5 series
	"gpt-3.5-turbo":      tokenizer.GPT35Turbo,
	"gpt-3.5-turbo-0301": tokenizer.GPT35Turbo,
	"gpt-3.5-turbo-0613": tokenizer.GPT35Turbo,
	"gpt-3.5-turbo-1106": tokenizer.GPT35Turbo,
	"gpt-3.5-turbo-0125": tokenizer.GPT35Turbo,

	// Claude series (uses cl100k_base as approximate tokenizer)
	// Note: Claude's actual tokenizer differs from cl100k_base with 10-20% deviation.
	// We use cl100k_base as an approximation since Claude's native tokenizer is not
	// publicly available. Token counts should be treated as estimates, not exact values.
	"claude-3-opus":              tokenizer.GPT4,
	"claude-3-sonnet":            tokenizer.GPT4,
	"claude-3-haiku":             tokenizer.GPT4,
	"claude-3-5-sonnet":          tokenizer.GPT4,
	"claude-3-5-sonnet-20240620": tokenizer.GPT4,
	"claude-3-5-sonnet-20241022": tokenizer.GPT4,
	"claude-3-5-haiku":           tokenizer.GPT4,
	"claude-2":                   tokenizer.GPT4,
	"claude-2.1":                 tokenizer.GPT4,
	"claude-instant":             tokenizer.GPT4,
	"claude-sonnet-4-20250514":   tokenizer.GPT4,
	"claude-opus-4-20250115":     tokenizer.GPT4,
	"claude-3-7-sonnet-20250219": tokenizer.GPT4,

	// MiniMax series (uses cl100k_base)
	"abab6.5s-chat": tokenizer.GPT35Turbo,
	"abab6.5g-chat": tokenizer.GPT35Turbo,
	"abab6s-chat":   tokenizer.GPT35Turbo,

	// DeepSeek
	"deepseek-chat":  tokenizer.GPT4,
	"deepseek-coder": tokenizer.GPT4,

	// Other models - default to GPT-4 encoding
	"default": tokenizer.GPT4,
}

// sortedPrefixes caches the sorted model prefixes for prefix matching
// Sorted by length descending (longest first) to avoid mis匹配
var (
	sortedPrefixes []string
	prefixOnce     sync.Once
)

func getSortedPrefixes() []string {
	prefixOnce.Do(func() {
		for k := range modelToEncoding {
			if k != "default" {
				sortedPrefixes = append(sortedPrefixes, k)
			}
		}
		sort.Slice(sortedPrefixes, func(i, j int) bool {
			return len(sortedPrefixes[i]) > len(sortedPrefixes[j])
		})
	})
	return sortedPrefixes
}

// getEncodingForModel returns the tokenizer model for a given model name
func getEncodingForModel(model string) tokenizer.Model {
	model = strings.ToLower(model)

	// Direct match
	if encoding, ok := modelToEncoding[model]; ok {
		return encoding
	}

	// Prefix match for models like "gpt-4o-xxx" -> "gpt-4o"
	// Use cached sorted prefixes (sorted by length descending)
	prefixes := getSortedPrefixes()

	for _, prefix := range prefixes {
		if strings.HasPrefix(model, prefix) {
			return modelToEncoding[prefix]
		}
	}

	return tokenizer.GPT4 // Default fallback
}

// encoderCache caches tokenizer encoders to avoid repeated initialization
var encoderCache sync.Map // map[tokenizer.Model]tokenizer.Codec

// getEncoder returns a cached encoder for the given model, or creates a new one
func getEncoder(encodingModel tokenizer.Model) (tokenizer.Codec, error) {
	if enc, ok := encoderCache.Load(encodingModel); ok {
		return enc.(tokenizer.Codec), nil
	}
	enc, err := tokenizer.ForModel(encodingModel)
	if err != nil {
		return nil, err
	}
	encoderCache.Store(encodingModel, enc)
	return enc, nil
}

// CountTokens counts the number of tokens in the given text for the specified model.
// Returns the token count and any error.
func CountTokens(text string, model string) (int, error) {
	encodingModel := getEncodingForModel(model)

	// Get the encoder (with caching)
	enc, err := getEncoder(encodingModel)
	if err != nil {
		// Fallback to GPT-4 encoder
		enc, err = getEncoder(tokenizer.GPT4)
		if err != nil {
			return 0, err
		}
	}

	// Encode and count
	ids, _, err := enc.Encode(text)
	if err != nil {
		return 0, err
	}

	return len(ids), nil
}

// RoleTokenCount 按角色分类的 token 统计结果
type RoleTokenCount struct {
	System    int
	User      int
	Assistant int
	Tool      int
}

// CountMessagesTokensByRole counts tokens for a list of messages, broken down by role.
func CountMessagesTokensByRole(messages []ChatMessage, model string) (RoleTokenCount, error) {
	var result RoleTokenCount

	// Approximate token overhead per message (role + formatting)
	overheadPerMessage := 4

	for _, msg := range messages {
			var contentTokens int
			var toolCallTokens int

			// Count content tokens
			if msg.Content != "" {
				count, err := CountTokens(msg.Content, model)
				if err != nil {
					return result, err
				}
				contentTokens = count
			}

			// Count reasoning_content tokens (DeepSeek-R1, o1/o3 models)
			if msg.ReasoningContent != "" {
				count, err := CountTokens(msg.ReasoningContent, model)
				if err != nil {
					return result, err
				}
				contentTokens += count
			}

			// Count tool call tokens if present (assistant messages with tool calls)
			for _, tc := range msg.ToolCalls {
				toolCallTokens += overheadPerMessage // per tool call overhead
				if tc.Name != "" {
					count, err := CountTokens(tc.Name, model)
					if err != nil {
						return result, err
					}
					toolCallTokens += count
				}
				if tc.Arguments != "" {
					count, err := CountTokens(tc.Arguments, model)
					if err != nil {
						return result, err
					}
					toolCallTokens += count
				}
			}

			// Count tool_call_id tokens for tool role messages
			if msg.ToolCallID != "" {
				count, err := CountTokens(msg.ToolCallID, model)
				if err != nil {
					return result, err
				}
				contentTokens += count
			}

			totalForMsg := overheadPerMessage + contentTokens + toolCallTokens

		switch msg.Role {
		case "system":
			result.System += totalForMsg
		case "user":
			result.User += totalForMsg
		case "assistant":
			result.Assistant += totalForMsg
		case "tool":
			result.Tool += totalForMsg
		}
	}

	return result, nil
}

// CountMessagesTokens counts the total tokens for a list of messages.
// This is more accurate than simple text counting as it accounts for role formatting.
func CountMessagesTokens(messages []ChatMessage, model string) (int, error) {
	total := 0

	// Approximate token overhead per message (role + formatting)
	// Typically 4 tokens for role + 2 for formatting
	overheadPerMessage := 4

	for _, msg := range messages {
			// Add overhead
			total += overheadPerMessage

			// Count content tokens
			if msg.Content != "" {
				count, err := CountTokens(msg.Content, model)
				if err != nil {
					return 0, err
				}
				total += count
			}

			// Count reasoning_content tokens (DeepSeek-R1, o1/o3 models).
			// Sent to LLM as part of assistant messages and included in
			// API prompt_tokens, but must also be counted locally for
			// accurate delta estimation between LLM calls.
			if msg.ReasoningContent != "" {
				count, err := CountTokens(msg.ReasoningContent, model)
				if err != nil {
					return 0, err
				}
				total += count
			}

			// Count tool call tokens if present (assistant messages with tool calls)
			for _, tc := range msg.ToolCalls {
				total += overheadPerMessage // per tool call formatting overhead
				if tc.Name != "" {
					count, err := CountTokens(tc.Name, model)
					if err != nil {
						return 0, err
					}
					total += count
				}
				if tc.Arguments != "" {
					count, err := CountTokens(tc.Arguments, model)
					if err != nil {
						return 0, err
					}
					total += count
				}
			}

			// Count tool_call_id tokens for tool role messages (sent to LLM)
			if msg.ToolCallID != "" {
				count, err := CountTokens(msg.ToolCallID, model)
				if err != nil {
					return 0, err
				}
				total += count
			}
		}

	return total, nil
}

// CountToolsTokens counts the total tokens for a list of tool definitions.
// It serializes the tool definitions to JSON format and counts tokens accurately.
func CountToolsTokens(toolDefs []ToolDefinition, model string) (int, error) {
	if len(toolDefs) == 0 {
		return 0, nil
	}

	// Convert to OpenAI tool format JSON and count tokens
	// This is the most accurate method as it counts the exact JSON sent to the LLM
	toolsJSON, err := serializeToolsToJSON(toolDefs)
	if err != nil {
		// Fallback: use rough estimation if serialization fails
		return estimateToolsTokens(toolDefs), nil
	}

	return CountTokens(toolsJSON, model)
}

// serializeToolsToJSON serializes tool definitions to JSON (same format as sent to LLM)
func serializeToolsToJSON(toolDefs []ToolDefinition) (string, error) {
	var sb strings.Builder
	sb.WriteString("[")

	for i, td := range toolDefs {
		if i > 0 {
			sb.WriteString(",")
		}

		// Build properties map
		properties := make(map[string]map[string]any)
		var required []string
		for _, p := range td.Parameters() {
			properties[p.Name] = map[string]any{
				"type":        p.Type,
				"description": p.Description,
			}
			if p.Required {
				required = append(required, p.Name)
			}
		}

		// Build the JSON structure
		toolJSON := map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        td.Name(),
				"description": td.Description(),
				"parameters": map[string]any{
					"type":       "object",
					"properties": properties,
					"required":   required,
				},
			},
		}

		jsonBytes, err := json.Marshal(toolJSON)
		if err != nil {
			return "", err
		}
		sb.Write(jsonBytes)
	}

	sb.WriteString("]")
	return sb.String(), nil
}

// estimateToolsTokens provides a rough estimate when JSON serialization fails
func estimateToolsTokens(toolDefs []ToolDefinition) int {
	// More accurate estimation based on typical tool definition sizes
	// Each tool: ~200 tokens overhead (JSON structure) + name + description + parameters
	overheadPerTool := 200

	total := 0
	for _, td := range toolDefs {
		total += overheadPerTool
		total += len(td.Name()) / 4        // rough: 4 chars per token
		total += len(td.Description()) / 4 // rough: 4 chars per token
		for range td.Parameters() {
			total += 50 // each parameter ~50 tokens
		}
	}
	return total
}
