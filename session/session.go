package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"xbot/llm"
	log "xbot/logger"
)

// Session 单一会话，保存完整对话历史并持久化到 JSONL 文件
type Session struct {
	mu               sync.Mutex
	messages         []llm.ChatMessage
	lastConsolidated int      // 记忆合并偏移量：此值之前的消息已被合并
	file             *os.File // JSONL 追加写句柄
	path             string
}

// New 创建会话。如果 filePath 非空，从磁盘加载历史并保持文件打开用于追加写入。
func New(filePath string) *Session {
	s := &Session{path: filePath}
	if filePath != "" {
		s.loadFromDisk()
		s.openForAppend()
	}
	return s
}

// AddMessage 追加一条消息（内存 + 磁盘）
func (s *Session) AddMessage(msg llm.ChatMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	s.appendToDisk(msg)
}

// GetHistory 获取最近 maxMessages 条历史（用于 LLM 上下文窗口）
func (s *Session) GetHistory(maxMessages int) []llm.ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) <= maxMessages {
		cp := make([]llm.ChatMessage, len(s.messages))
		copy(cp, s.messages)
		return cp
	}
	cp := make([]llm.ChatMessage, maxMessages)
	copy(cp, s.messages[len(s.messages)-maxMessages:])
	return cp
}

// Clear 清空会话（内存 + 截断磁盘文件）
func (s *Session) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
	if s.file != nil {
		_ = s.file.Truncate(0)
		_, _ = s.file.Seek(0, 0)
	}
}

// Len 返回消息数量
func (s *Session) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

// LastConsolidated 返回记忆合并偏移量
func (s *Session) LastConsolidated() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastConsolidated
}

// SetLastConsolidated 更新记忆合并偏移量
func (s *Session) SetLastConsolidated(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastConsolidated = n
}

// GetMessages 获取全部消息的副本（用于记忆合并）
func (s *Session) GetMessages() []llm.ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]llm.ChatMessage, len(s.messages))
	copy(cp, s.messages)
	return cp
}

// Close 关闭文件句柄（程序退出时调用）
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

// --- 内部持久化方法 ---

// loadFromDisk 启动时从 JSONL 文件加载历史消息
func (s *Session) loadFromDisk() {
	f, err := os.Open(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Warn("Failed to load session file")
		}
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 支持长行（单条消息可能很长）
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	count := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg llm.ChatMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			log.WithError(err).Warn("Skipping corrupt session line")
			continue
		}
		s.messages = append(s.messages, msg)
		count++
	}

	if count > 0 {
		log.WithField("messages", count).Info("Session history loaded from disk")
	}
}

// openForAppend 打开文件用于追加写入
func (s *Session) openForAppend() {
	if s.path == "" {
		return
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.WithError(err).Error("Failed to create session directory")
		return
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.WithError(err).Error("Failed to open session file for writing")
		return
	}
	s.file = f
}

// appendToDisk 追加一条消息到磁盘（调用方已持有锁）
func (s *Session) appendToDisk(msg llm.ChatMessage) {
	if s.file == nil {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		log.WithError(err).Warn("Failed to marshal message for persistence")
		return
	}
	data = append(data, '\n')
	if _, err := s.file.Write(data); err != nil {
		log.WithError(err).Warn("Failed to write message to session file")
	}
}
