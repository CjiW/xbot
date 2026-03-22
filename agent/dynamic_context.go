package agent

import (
	"fmt"

	"xbot/llm"
)

// DynamicContextInjector 在 Run() 循环中检测动态信息变化并注入。
// 首轮不注入（system prompt 已包含最新值），后续 iteration 检测到 CWD 变化时注入。
// 注入位置：tool message content 末尾（与 sys_reminder 相同方式，在 sys_reminder 之前）。
type DynamicContextInjector struct {
	lastCWD string
	getCWD  func() string // 获取当前 CWD 的函数（主 Agent 用 session.GetCurrentDir()，SubAgent 用 cfg.InitialCWD）
}

// NewDynamicContextInjector 创建动态上下文注入器。
func NewDynamicContextInjector(getCWD func() string) *DynamicContextInjector {
	return &DynamicContextInjector{getCWD: getCWD}
}

// InjectIfNeeded 检测 CWD 变化，如有变化则将 <dynamic-context> 追加到最新 tool message 末尾。
// 返回 true 表示发生了注入。
//
// 注入顺序：在 sys_reminder 之前（dynamic-context 描述事实性环境变化，sys_reminder 描述行为引导）。
func (d *DynamicContextInjector) InjectIfNeeded(messages []llm.ChatMessage) bool {
	currentCWD := d.getCWD()
	if d.lastCWD == "" {
		// 首轮：记录但不注入（system prompt 中的 CWD 已是最新值）
		d.lastCWD = currentCWD
		return false
	}

	if currentCWD == d.lastCWD {
		return false // 无变化，不注入
	}

	// CWD 发生变化，构建注入内容
	injection := "<dynamic-context>\n" +
		"环境变化:\n" +
		fmt.Sprintf("- 当前目录已切换为：%s，切换后所有 Shell 命令在新目录执行", currentCWD) +
		"\n</dynamic-context>"

	// 追加到最后一条消息（tool message）末尾
	if len(messages) > 0 {
		lastIdx := len(messages) - 1
		messages[lastIdx].Content += "\n\n" + injection
	}

	d.lastCWD = currentCWD
	return true
}
