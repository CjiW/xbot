package agent

import "strings"

// ExtractFinalReply 从 LLM 的完整输出中提取最终回复。
// 短内容原样返回；长内容取最后一段作为结论（最后一段太短时合并倒数两段）。
func ExtractFinalReply(content string) string {
	if len(content) < 500 {
		return content
	}
	paragraphs := strings.Split(strings.TrimSpace(content), "\n\n")
	if len(paragraphs) > 1 {
		last := strings.TrimSpace(paragraphs[len(paragraphs)-1])
		if len(last) < 50 && len(paragraphs) > 2 {
			return strings.TrimSpace(
				paragraphs[len(paragraphs)-2] + "\n\n" + last,
			)
		}
		return last
	}
	return content
}
