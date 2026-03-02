package session

import (
	"fmt"
	"sync"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// TenantSession represents a single tenant's conversation session
type TenantSession struct {
	tenantID   int64
	channel    string
	chatID     string
	sessionSvc *sqlite.SessionService
	memorySvc  *sqlite.MemoryService
	memory     *TenantMemory
	mcpManager *tools.SessionMCPManager // 会话 MCP 管理器
	lastActive time.Time                // 会话活跃时间
	mu         sync.RWMutex             // 保护 lastActive
}

// AddMessage adds a message to this tenant's session
func (s *TenantSession) AddMessage(msg llm.ChatMessage) error {
	return s.sessionSvc.AddMessage(s.tenantID, msg)
}

// GetHistory retrieves recent messages for LLM context window
func (s *TenantSession) GetHistory(maxMessages int) ([]llm.ChatMessage, error) {
	return s.sessionSvc.GetHistory(s.tenantID, maxMessages)
}

// GetMessages retrieves all messages for this tenant
func (s *TenantSession) GetMessages() ([]llm.ChatMessage, error) {
	return s.sessionSvc.GetAllMessages(s.tenantID)
}

// Len returns the number of messages in this tenant's session
func (s *TenantSession) Len() (int, error) {
	return s.sessionSvc.GetMessagesCount(s.tenantID)
}

// LastConsolidated returns the last consolidated message index
func (s *TenantSession) LastConsolidated() int {
	lastConsolidated, err := s.memorySvc.GetState(s.tenantID)
	if err != nil {
		// If error, return 0 as safe default
		return 0
	}
	return lastConsolidated
}

// SetLastConsolidated updates the last consolidated message index
func (s *TenantSession) SetLastConsolidated(n int) error {
	return s.memorySvc.SetState(s.tenantID, n)
}

// Clear removes all messages from this tenant's session
func (s *TenantSession) Clear() error {
	return s.sessionSvc.Clear(s.tenantID)
}

// Memory returns the memory accessor for this tenant
func (s *TenantSession) Memory() *TenantMemory {
	return s.memory
}

// TenantID returns the tenant ID
func (s *TenantSession) TenantID() int64 {
	return s.tenantID
}

// Channel returns the channel name
func (s *TenantSession) Channel() string {
	return s.channel
}

// ChatID returns the chat ID
func (s *TenantSession) ChatID() string {
	return s.chatID
}

// String returns a string representation of the tenant
func (s *TenantSession) String() string {
	return fmt.Sprintf("%s:%s (tenant_id=%d)", s.channel, s.chatID, s.tenantID)
}

// GetSessionKey 返回会话唯一标识
func (s *TenantSession) GetSessionKey() string {
	return s.channel + ":" + s.chatID
}

// MarkActive 更新会话活跃时间
func (s *TenantSession) MarkActive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActive = time.Now()
}

// SetMCPManager 设置会话 MCP 管理器
func (s *TenantSession) SetMCPManager(mgr *tools.SessionMCPManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mcpManager = mgr
}

// GetMCPManager 获取 MCP 管理器
func (s *TenantSession) GetMCPManager() *tools.SessionMCPManager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mcpManager
}

// LastActive 返回会话最后活跃时间
func (s *TenantSession) LastActive() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastActive
}

// CleanupInactiveMCPs 清理不活跃的 MCP 连接
// 返回会话最后活跃时间（用于判断会话是否需要从缓存中移除）
func (s *TenantSession) CleanupInactiveMCPs() time.Time {
	s.mu.RLock()
	mgr := s.mcpManager
	s.mu.RUnlock()

	if mgr != nil {
		return mgr.UnloadInactiveServers()
	}
	return s.LastActive()
}

// Close 关闭会话资源
func (s *TenantSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.mcpManager != nil {
		s.mcpManager.Close()
		s.mcpManager = nil
	}
}

// InvalidateMCP 使会话的 MCP 连接失效，强制下次使用时重新加载
func (s *TenantSession) InvalidateMCP() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.mcpManager != nil {
		s.mcpManager.Invalidate()
	}
}
