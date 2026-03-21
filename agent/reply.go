package agent

import "strings"

// ExtractFinalReply 从 LLM 的完整输出中提取最终回复。
// 短内容（<500字）原样返回。
// 长内容按段落分割，取最后 2-3 段（上限 2000 字），避免丢失结论上下文。
func ExtractFinalReply(content string) string {
	if len(content) < 500 {
		return content
	}
	paragraphs := strings.Split(strings.TrimSpace(content), "\n\n")
	if len(paragraphs) <= 1 {
		return content
	}

	// 取最后几段：优先取 3 段，如果超过 2000 字符则缩减到 2 段
	takeLast := 3
	if len(paragraphs) < takeLast {
		takeLast = len(paragraphs)
	}

	candidate := strings.TrimSpace(strings.Join(paragraphs[len(paragraphs)-takeLast:], "\n\n"))
	if len(candidate) > 2000 && takeLast > 2 {
		candidate = strings.TrimSpace(strings.Join(paragraphs[len(paragraphs)-2:], "\n\n"))
	}
	return candidate
}
