package prompt

import _ "embed"

// Default 是编译时嵌入的默认系统提示词模板。
// 当用户未配置 prompt 文件（Agent.PromptFile / PROMPT_FILE）时使用。
// 渠道无关：不含任何渠道特定提示，渠道特化内容由 ChannelPromptProvider 注入。
//
//go:embed prompt.md
var Default string
