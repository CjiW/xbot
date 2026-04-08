package agent

import "xbot/prompt"

// EmbeddedPrompt 返回编译时嵌入的默认系统提示词。
// 由 PromptLoader 在文件不存在时使用。
func EmbeddedPrompt() string { return prompt.Default }

// EmbeddedFallbackPrompt 返回编译时嵌入的最小兜底系统提示词。
func EmbeddedFallbackPrompt() string { return prompt.Fallback }

// EmbeddedCronPrompt 返回编译时嵌入的 Cron 系统提示词模板。
func EmbeddedCronPrompt() string { return prompt.CronSystem }
