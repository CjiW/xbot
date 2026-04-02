package agent

import "xbot/prompt"

// EmbeddedPrompt 返回编译时嵌入的默认系统提示词。
// 由 PromptLoader 在文件不存在时使用。
func EmbeddedPrompt() string { return prompt.Default }
