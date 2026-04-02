package agent

import _ "embed"

// embeddedPrompt 是编译时嵌入的默认系统提示词模板。
// 当用户未配置 prompt 文件时使用此默认值。
// 用户可通过 Agent.PromptFile 或 PROMPT_FILE 环境变量指定自定义 prompt 文件覆盖。
//
//go:embed default_prompt.md
var embeddedPrompt string
