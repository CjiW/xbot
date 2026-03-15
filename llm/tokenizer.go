package llm

import (
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

	// Claude series (uses cl100k_base similar encoding)
	"claude-3-opus":              tokenizer.GPT4,
	"claude-3-sonnet":            tokenizer.GPT4,
	"claude-3-haiku":             tokenizer.GPT4,
	"claude-3-5-sonnet":          tokenizer.GPT4,
	"claude-3-5-sonnet-20241022": tokenizer.GPT4,
	"claude-3-5-haiku":           tokenizer.GPT4,
	"claude-2":                   tokenizer.GPT4,
	"claude-2.1":                 tokenizer.GPT4,
	"claude-instant":             tokenizer.GPT4,

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

		// Count tool call tokens if present
		for _, tc := range msg.ToolCalls {
			total += overheadPerMessage // role
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

		// Count tool result tokens (only for tool role, content counted above)
		// Note: tool messages already counted in the content section above, don't double count
	}

	return total, nil
}

// CountToolsTokens counts the total tokens for a list of tool definitions.
// Each tool definition includes name, description, and parameters.
func CountToolsTokens(toolDefs []ToolDefinition, model string) (int, error) {
	if len(toolDefs) == 0 {
		return 0, nil
	}

	total := 0
	// Approximate token overhead for tools structure
	// Format: {"type":"function","function":{"name":"...","description":"...","parameters":{...}}}
	overheadPerTool := 30 // JSON formatting overhead per tool

	for _, td := range toolDefs {
		total += overheadPerTool

		// Tool name
		if td.Name() != "" {
			count, err := CountTokens(td.Name(), model)
			if err != nil {
				return 0, err
			}
			total += count
		}

		// Tool description
		if td.Description() != "" {
			count, err := CountTokens(td.Description(), model)
			if err != nil {
				return 0, err
			}
			total += count
		}

		// Tool parameters
		for _, p := range td.Parameters() {
			// Parameter name and type
			count, err := CountTokens(p.Name+" "+p.Type, model)
			if err != nil {
				return 0, err
			}
			total += count

			// Parameter description
			if p.Description != "" {
				count, err := CountTokens(p.Description, model)
				if err != nil {
					return 0, err
				}
				total += count
			}
		}
	}

	return total, nil
}
