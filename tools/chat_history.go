package tools

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// ChatHistoryStore 存储各个群组/会话的最近消息历史
type ChatHistoryStore struct {
	mu      sync.RWMutex
	history map[string]*ChatHistory // key: channel:chatID
	maxSize int                     // 每个群组保留的最大消息数
}

// ChatHistory 单个群组的消息历史
type ChatHistory struct {
	messages   []ChatMessage
	maxSize    int
	lastUpdate time.Time
}

// ChatMessage 单条消息记录
type ChatMessage struct {
	Content   string    `json:"content"`
	SenderID  string    `json:"sender_id"`
	Timestamp time.Time `json:"timestamp"`
}

// NewChatHistoryStore 创建聊天历史存储
func NewChatHistoryStore(maxSize int) *ChatHistoryStore {
	if maxSize <= 0 {
		maxSize = 20 // 默认保留最近 20 条
	}
	return &ChatHistoryStore{
		history: make(map[string]*ChatHistory),
		maxSize: maxSize,
	}
}

// Add 添加一条消息到历史
func (s *ChatHistoryStore) Add(channel, chatID, senderID, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.makeKey(channel, chatID)
	hist, exists := s.history[key]
	if !exists {
		hist = &ChatHistory{
			messages:   make([]ChatMessage, 0, s.maxSize),
			maxSize:    s.maxSize,
			lastUpdate: time.Now(),
		}
		s.history[key] = hist
	}

	// 添加新消息
	msg := ChatMessage{
		Content:   content,
		SenderID:  senderID,
		Timestamp: time.Now(),
	}
	hist.messages = append(hist.messages, msg)
	hist.lastUpdate = time.Now()

	// 限制大小
	if len(hist.messages) > hist.maxSize {
		// 保留最新的 maxSize 条消息
		hist.messages = hist.messages[len(hist.messages)-hist.maxSize:]
	}
}

// Get 获取指定群组的最近消息历史
func (s *ChatHistoryStore) Get(channel, chatID string, limit int) []ChatMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := s.makeKey(channel, chatID)
	hist, exists := s.history[key]
	if !exists {
		return nil
	}

	messages := hist.messages
	if limit > 0 && limit < len(messages) {
		// 返回最近的 limit 条消息
		messages = messages[len(messages)-limit:]
	}

	// 返回副本，避免外部修改
	result := make([]ChatMessage, len(messages))
	copy(result, messages)
	return result
}

// makeKey 生成存储 key
func (s *ChatHistoryStore) makeKey(channel, chatID string) string {
	return fmt.Sprintf("%s:%s", channel, chatID)
}

// ---- ChatHistoryTool: 让 LLM 可以查询聊天历史 ----

// ChatHistoryTool 聊天历史查询工具
type ChatHistoryTool struct {
	store *ChatHistoryStore
}

// NewChatHistoryTool 创建聊天历史工具
func NewChatHistoryTool(store *ChatHistoryStore) *ChatHistoryTool {
	return &ChatHistoryTool{store: store}
}

func (t *ChatHistoryTool) Name() string {
	return "ChatHistory"
}

func (t *ChatHistoryTool) Description() string {
	return `Query recent chat message history in the current group/conversation.
IMPORTANT: Only use this tool when you need to understand recent context or conversation flow that is not in your immediate memory.
Parameters (JSON):
  - limit: integer, optional, number of recent messages to retrieve (defaults to 10, max 50)
Example: {"limit": 10}`
}

func (t *ChatHistoryTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "limit", Type: "integer", Description: "Number of recent messages to retrieve (defaults to 10, max 50)", Required: false},
	}
}

type chatHistoryParams struct {
	Limit int `json:"limit"`
}

func (t *ChatHistoryTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	log.WithField("ctx", fmt.Sprintf("%+v", ctx)).Debug("ChatHistory tool called")

	params, err := ParseInput[chatHistoryParams](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	// 从 ToolContext 获取当前 channel 和 chatID
	channel := ctx.Channel
	chatID := ctx.ChatID

	log.WithFields(log.Fields{
		"channel": channel,
		"chat_id": chatID,
		"limit":   limit,
	}).Debug("ChatHistory retrieving messages")

	if channel == "" || chatID == "" {
		return NewResult("No active conversation context."), nil
	}

	messages := t.store.Get(channel, chatID, limit)
	if len(messages) == 0 {
		return NewResult("No recent message history found."), nil
	}

	// 格式化输出
	var sb strings.Builder
	fmt.Fprintf(&sb, "Recent %d messages in this conversation:\n\n", len(messages))

	for i, msg := range messages {
		// 格式化时间
		timeStr := msg.Timestamp.Format("15:04")
		if time.Since(msg.Timestamp) > 24*time.Hour {
			timeStr = msg.Timestamp.Format("01/02 15:04")
		}

		fmt.Fprintf(&sb, "[%d] %s <%s>: %s\n", i+1, timeStr, msg.SenderID, msg.Content)
	}

	return NewResult(sb.String()), nil
}
