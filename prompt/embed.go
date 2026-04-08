package prompt

import _ "embed"

// Default 是编译时嵌入的默认系统提示词模板。
// 当用户未配置 prompt 文件（Agent.PromptFile / PROMPT_FILE）时使用。
// 渠道无关：不含任何渠道特定提示，渠道特化内容由 ChannelPromptProvider 注入。
//
//go:embed prompt.md
var Default string

// Fallback 是最小兜底系统提示词模板，仅在默认 prompt 无法解析时使用。
//
//go:embed fallback.md
var Fallback string

// CLIChannel 是 CLI 渠道的特化 prompt。
//
//go:embed channels/cli.md
var CLIChannel string

// FeishuChannel 是飞书渠道的特化 prompt。
//
//go:embed channels/feishu.md
var FeishuChannel string

// CronSystem 是 Cron 专用系统提示词模板。
// 使用 fmt.Sprintf(workDir, now) 渲染。
//
//go:embed cron/system.md
var CronSystem string
