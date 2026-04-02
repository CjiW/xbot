package agent

import _ "embed"

// embeddedPrompt 是编译时嵌入的默认系统提示词模板（agent/prompt.md）。
// 当用户未配置 prompt 文件（Agent.PromptFile / PROMPT_FILE）时使用此默认值。
// 渠道无关：不含任何渠道特定提示，渠道特化内容由 ChannelPromptProvider 注入。
//
//go:embed prompt.md
var embeddedPrompt string
