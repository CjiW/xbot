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
// Uses OpenAI's official formula: https://cookbook.openai.com/examples/how_to_count_tokens_with_tiktoken
// Formula:
// - Every message: 3 tokens (role + content separators)
// - For each tool call: function name + arguments + overhead
// - For tool role messages: tool_call_id adds overhead
func CountMessagesTokens(messages []ChatMessage, model string) (int, error) {
	total := 0

	// OpenAI official constants
	const tokensPerMessage = 3
	const tokensPerName = 1

	for i, msg := range messages {
		// Every message gets base overhead
		total += tokensPerMessage

		// Count content tokens
		if msg.Content != "" {
			count, err := CountTokens(msg.Content, model)
			if err != nil {
				return 0, err
			}
			total += count
		}

		// Handle tool_calls - these are in the assistant message
		for _, tc := range msg.ToolCalls {
			// Function name
			if tc.Name != "" {
				count, err := CountTokens(tc.Name, model)
				if err != nil {
					return 0, err
				}
				total += count
			}
			// Arguments (JSON string)
			if tc.Arguments != "" {
				count, err := CountTokens(tc.Arguments, model)
				if err != nil {
					return 0, err
				}
				total += count
			}
		}

		// Handle tool role messages - they have tool_call_id
		// The content is the tool result
		if msg.Role == "tool" {
			// tool_call_id adds overhead
			if msg.ToolCallID != "" {
				total += tokensPerName
			}
		}

		// Last message gets +1 token (usually the assistant message)
		if i == len(messages)-1 {
			total += 1
		}
	}

	return total, nil
}
