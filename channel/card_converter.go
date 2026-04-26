// xbot Channel shared utilities
// Feishu Card conversion for CLI and Web channels

package channel

import (
	"encoding/json"
	"strings"
)

// ---------------------------------------------------------------------------
// __FEISHU_CARD__ protocol adaptation
// ---------------------------------------------------------------------------

// ConvertFeishuCard extracts human-readable content from __FEISHU_CARD__ prefixed JSON.
// Best-effort: if extraction fails, returns raw JSON stripped of prefix.
func ConvertFeishuCard(content string) string {
	// Strip prefix
	jsonStr := strings.TrimPrefix(content, "__FEISHU_CARD__")
	jsonStr = strings.TrimSpace(jsonStr)

	var card map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &card); err != nil {
		return jsonStr // fallback: return raw JSON
	}

	// Try to extract header.title.content
	var result strings.Builder
	if header, ok := card["header"].(map[string]any); ok {
		if title, ok := header["title"].(map[string]any); ok {
			if tc, ok := title["content"].(string); ok && tc != "" {
				result.WriteString("# ")
				result.WriteString(tc)
				result.WriteString("\n\n")
			}
		}
	}

	// Try to extract elements (simplified)
	if elements, ok := card["elements"].([]any); ok {
		for _, elem := range elements {
			if obj, ok := elem.(map[string]any); ok {
				tag, _ := obj["tag"].(string)
				switch tag {
				case "div":
					if text, ok := obj["text"].(string); ok {
						// text might be JSON with content field
						var textObj map[string]string
						if json.Unmarshal([]byte(text), &textObj) == nil {
							if c, ok := textObj["content"]; ok {
								result.WriteString(c)
								result.WriteString("\n")
							}
						} else {
							result.WriteString(text)
							result.WriteString("\n")
						}
					}
				case "markdown":
					if content, ok := obj["content"].(string); ok {
						result.WriteString(content)
						result.WriteString("\n")
					}
				}
			}
		}
	}

	if result.Len() == 0 {
		return jsonStr
	}
	return strings.TrimSpace(result.String())
}
