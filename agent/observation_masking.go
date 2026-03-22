package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// MaskedObservation 存储一条被遮蔽的 tool result 的完整信息。
type MaskedObservation struct {
	ID         string    `json:"id"`
	ToolName   string    `json:"tool_name"`
	Arguments  string    `json:"arguments"`
	Content    string    `json:"content"` // 完整的原始 tool result
	MaskedAt   time.Time `json:"masked_at"`
	MessageIdx int       `json:"message_idx"` // 在 messages slice 中的原始位置
}

const (
	defaultMaxEntries = 200       // 默认最大条数
	defaultMaxChars   = 2_000_000 // 默认最大存储字符数（~2MB）
)

// ObservationMaskStore 管理 observation masking 的存储和召回。
// 零成本压缩策略：遮蔽旧 tool result，不发给 LLM，但完整保留可通过工具召回。
// 双重容量限制：maxSize（条数）+ maxChars（总字符数），任一超限则淘汰最旧条目。
type ObservationMaskStore struct {
	mu         sync.RWMutex
	entries    []MaskedObservation // 按 mask 顺序存储
	maxSize    int                 // 最大存储条数
	maxChars   int                 // 最大存储总字符数
	totalChars int                 // 当前总字符数
}

// NewObservationMaskStore 创建 ObservationMaskStore。
func NewObservationMaskStore(maxSize int) *ObservationMaskStore {
	if maxSize <= 0 {
		maxSize = defaultMaxEntries
	}
	return &ObservationMaskStore{
		maxSize:  maxSize,
		maxChars: defaultMaxChars,
	}
}

// generateMaskID 生成 mask ID: "mk_" + 8位随机 hex。
func generateMaskID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "mk_" + hex.EncodeToString(b)
}

// Mask 遮蔽一条 tool result，存储完整内容并返回占位符文本。
// 占位符格式: 📂 [masked:mk_xxxx] ToolName(args_preview) — N chars — 结果已遮蔽，使用 recall_masked 可查看完整内容
func (s *ObservationMaskStore) Mask(toolName, arguments, content string, messageIdx int) (MaskedObservation, string) {
	id := generateMaskID()

	entry := MaskedObservation{
		ID:         id,
		ToolName:   toolName,
		Arguments:  arguments,
		Content:    content,
		MaskedAt:   time.Now(),
		MessageIdx: messageIdx,
	}

	s.mu.Lock()
	// 双重容量限制：超条数或超字符数时，淘汰最旧条目
	contentLen := len([]rune(content))
	for len(s.entries) >= s.maxSize || (s.totalChars+contentLen > s.maxChars && len(s.entries) > 0) {
		evicted := s.entries[0]
		s.totalChars -= len([]rune(evicted.Content))
		s.entries = s.entries[1:]
	}
	s.entries = append(s.entries, entry)
	s.totalChars += contentLen
	s.mu.Unlock()

	// 生成占位符
	argsPreview := arguments
	if len([]rune(argsPreview)) > 80 {
		argsPreview = string([]rune(argsPreview)[:80]) + "..."
	}
	charCount := len([]rune(content))
	placeholder := fmt.Sprintf("📂 [masked:%s] %s(%s) — %d chars — 结果已遮蔽，使用 recall_masked 可查看完整内容", id, toolName, argsPreview, charCount)

	return entry, placeholder
}

// Recall 按 ID 召回已遮蔽的完整 tool result。
func (s *ObservationMaskStore) Recall(id string) (MaskedObservation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, e := range s.entries {
		if e.ID == id {
			return e, nil
		}
	}
	return MaskedObservation{}, fmt.Errorf("masked observation %s not found", id)
}

// List 列出所有已遮蔽的 observation（按 mask 时间倒序）。
func (s *ObservationMaskStore) List() []MaskedObservation {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]MaskedObservation, len(s.entries))
	copy(result, s.entries)
	return result
}

// Size 返回当前存储的 observation 数量。
func (s *ObservationMaskStore) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Clear 清空所有已遮蔽的 observation。
func (s *ObservationMaskStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.totalChars = 0
}

// --- tools.MaskedRecallStore 接口实现 ---
// 这些方法让 ObservationMaskStore 满足 tools 包的 MaskedRecallStore 接口。
// 不需要导入 tools 包（Go 鸭子类型），只需方法签名匹配。

// RecallMasked 按 ID 召回已遮蔽的内容。
func (s *ObservationMaskStore) RecallMasked(id string) (string, string, error) {
	obs, err := s.Recall(id)
	if err != nil {
		return "", "", err
	}
	argsPreview := obs.Arguments
	if len([]rune(argsPreview)) > 80 {
		argsPreview = string([]rune(argsPreview)[:80]) + "..."
	}
	return fmt.Sprintf("%s(%s)", obs.ToolName, argsPreview), obs.Content, nil
}

// ListMasked 列出所有已遮蔽的 observation（摘要信息）。
func (s *ObservationMaskStore) ListMasked() []map[string]interface{} {
	entries := s.List()
	result := make([]map[string]interface{}, len(entries))
	for i, e := range entries {
		argsPreview := e.Arguments
		if len([]rune(argsPreview)) > 60 {
			argsPreview = string([]rune(argsPreview)[:60]) + "..."
		}
		result[i] = map[string]interface{}{
			"id":           e.ID,
			"tool_name":    e.ToolName,
			"args_preview": argsPreview,
			"char_count":   len([]rune(e.Content)),
		}
	}
	return result
}

// MaskOldToolResults 遮蔽 messages 中较旧的 tool result，返回修改后的 messages slice。
//
// 策略：
//   - 保留最近的 keepGroups 个完整 tool group
//   - 更早的 tool result 被遮蔽：完整内容存入 store，替换为占位符
//   - assistant(tool_calls) 消息保留（推理过程），只遮蔽 tool result
//   - stripThinkBlocks 应用于被遮蔽组的 assistant content
//
// 返回：修改后的 messages（新 slice），实际遮蔽数量。
func MaskOldToolResults(messages []llm.ChatMessage, store *ObservationMaskStore, keepGroups int) ([]llm.ChatMessage, int) {
	if keepGroups <= 0 {
		keepGroups = 3
	}

	// 导入避免循环
	type toolGroup struct{ start, end int }

	var groups []toolGroup
	for i := range messages {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			g := toolGroup{start: i, end: i}
			for j := i + 1; j < len(messages) && messages[j].Role == "tool"; j++ {
				g.end = j
			}
			groups = append(groups, g)
		}
	}

	maskCount := len(groups) - keepGroups
	if maskCount <= 0 {
		return messages, 0
	}

	result := make([]llm.ChatMessage, len(messages))
	copy(result, messages)

	maskedTotal := 0
	for g := range maskCount {
		grp := groups[g]
		for j := grp.start; j <= grp.end; j++ {
			msg := result[j]
			switch msg.Role {
			case "assistant":
				// 保留 assistant 消息（推理过程），但 strip think blocks 节省 token
				msg.Content = llm.StripThinkBlocks(msg.Content)
			case "tool":
				// 遮蔽 tool result
				if msg.Content != "" && msg.Content != "null" {
					_, placeholder := store.Mask(
						msg.ToolName,
						msg.ToolArguments,
						msg.Content,
						j,
					)
					msg.Content = placeholder
					maskedTotal++
				}
			}
			result[j] = msg
		}
	}

	log.WithFields(map[string]interface{}{
		"masked_count": maskedTotal,
		"kept_groups":  keepGroups,
		"total_groups": len(groups),
	}).Info("Observation masking: masked old tool results")

	return result, maskedTotal
}
