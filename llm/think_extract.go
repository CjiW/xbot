package llm

import (
	"regexp"
	"strings"
)

// ExtractThinkBlocks extracts thinking/reasoning content from text.
// Supports: <think...</think, <reasoning>...</reasoning>, <thinking>...</thinking>
// Also considers response.ReasoningContent from structured API fields.
// Returns the inner thinking content with tags stripped, or empty string.
func ExtractThinkBlocks(content string) string {
	if content == "" {
		return ""
	}
	var parts []string
	extract := func(re *regexp.Regexp) {
		matches := re.FindAllStringSubmatch(content, -1)
		for _, m := range matches {
			if len(m) > 1 && m[1] != "" {
				parts = append(parts, strings.TrimSpace(m[1]))
			}
		}
	}
	// <think...</think blocks (DeepSeek-style)
	extract(regexp.MustCompile(`(?s)<think\b[^>]*>(.*?)</think\s*>`))
	// <reasoning>...</reasoning> blocks
	extract(regexp.MustCompile(`(?s)<reasoning>(.*?)</reasoning>`))
	// <thinking>...</thinking> blocks
	extract(regexp.MustCompile(`(?s)<thinking>(.*?)</thinking>`))
	return strings.Join(parts, "\n")
}
