package session

import (
	"fmt"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// TenantSession represents a single tenant's conversation session
type TenantSession struct {
	tenantID   int64
	channel    string
	chatID     string
	sessionSvc *sqlite.SessionService
	memorySvc  *sqlite.MemoryService
	memory     *TenantMemory
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
