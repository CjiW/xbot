package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xbot/bus"
	log "xbot/logger"
	"xbot/memory"
	"xbot/session"
	"xbot/tools"
)

// handlePromptQuery 构建完整提示词并写入文件发送给用户（dryrun，不调用 LLM）
func (a *Agent) handlePromptQuery(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	// 提取 /prompt 之后的 query 内容（先 trim 再截取，与 cmd 解析对齐）
	trimmed := strings.TrimSpace(msg.Content)
	query := strings.TrimSpace(trimmed[len("/prompt"):])
	if query == "" {
		query = "(empty query)"
	}

	// 替换 msg.Content 为 query，复用 buildPrompt
	dryMsg := msg
	dryMsg.Content = query
	messages, err := a.buildPrompt(ctx, dryMsg, tenantSession, false)
	if err != nil {
		return nil, err
	}

	// 获取工具定义
	sessionKey := msg.Channel + ":" + msg.ChatID
	toolDefs := a.tools.AsDefinitionsForSession(sessionKey)

	// 格式化输出
	var buf strings.Builder
	buf.WriteString("=== Prompt Dry Run ===\n\n")
	for i, m := range messages {
		fmt.Fprintf(&buf, "--- [%d] role: %s ---\n", i, m.Role)
		buf.WriteString(m.Content)
		buf.WriteString("\n\n")
	}

	fmt.Fprintf(&buf, "--- Tools (%d) ---\n", len(toolDefs))
	for _, td := range toolDefs {
		fmt.Fprintf(&buf, "- %s: %s\n", td.Name(), td.Description())
		for _, p := range td.Parameters() {
			req := ""
			if p.Required {
				req = " (required)"
			}
			fmt.Fprintf(&buf, "    %s (%s)%s: %s\n", p.Name, p.Type, req, p.Description)
		}
	}

	fmt.Fprintf(&buf, "\n--- Total messages: %d ---\n", len(messages))

	// 写入文件并发送
	workspaceRoot := tools.UserWorkspaceRoot(a.workDir, msg.SenderID)
	promptFile := filepath.Join(workspaceRoot, "prompt-dryrun.md")
	if err := os.WriteFile(promptFile, []byte(buf.String()), 0o644); err != nil {
		return nil, fmt.Errorf("write prompt file: %w", err)
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("[prompt-dryrun.md](%s)", promptFile),
	}, nil
}

// handleNewSession 处理 /new 命令：先归档记忆，再清空会话
func (a *Agent) handleNewSession(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	llmClient, model, _, _ := a.llmFactory.GetLLM(msg.SenderID)

	messages, err := tenantSession.GetMessages()
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "获取会话消息失败，请重试。",
		}, nil
	}
	lastConsolidated := tenantSession.LastConsolidated()
	mem := tenantSession.Memory()

	// 取尚未合并的消息进行归档
	snapshot := messages
	if lastConsolidated < len(messages) {
		snapshot = messages[lastConsolidated:]
	}

	if len(snapshot) > 0 {
		log.Ctx(ctx).WithField("tenant", tenantSession.String()).Infof("/new: archiving %d unconsolidated messages", len(snapshot))
		result, _ := mem.Memorize(ctx, memory.MemorizeInput{
			Messages:         snapshot,
			LastConsolidated: 0,
			LLMClient:        llmClient,
			Model:            model,
			ArchiveAll:       true,
			MemoryWindow:     a.memoryWindow,
		})
		if !result.OK {
			return &bus.OutboundMessage{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				Content: "记忆归档失败，会话未重置，请重试。",
			}, nil
		}
	}

	if err := tenantSession.Clear(); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to clear tenant session")
	}
	if err := tenantSession.SetLastConsolidated(0); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to reset last consolidated")
	}

	// 清除记忆整理状态，取消正在进行的整理任务（多路径协调）
	tenantKey := msg.Channel + ":" + msg.ChatID

	// Phase 2: 清理智能触发状态
	a.triggerProviders.Delete(tenantKey)

	// Phase 2: 清理 offload 数据
	if a.offloadStore != nil {
		a.offloadStore.CleanSession(tenantKey)
	}

	a.clearConsolidationState(tenantKey)

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "会话已重置，记忆已归档。",
	}, nil
}
