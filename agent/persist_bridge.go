package agent

import (
	"strings"

	"xbot/llm"
	log "xbot/logger"
	"xbot/session"
)

// PersistenceBridge manages incremental session persistence for a Run() execution.
// It replaces the scattered lastPersistedCount field and inline persistence logic
// that was previously on runState.
type PersistenceBridge struct {
	session            *session.TenantSession
	lastPersistedCount int
}

// NewPersistenceBridge creates a PersistenceBridge.
// session may be nil (pure in-memory mode, no persistence).
// initialCount is the number of messages already persisted (len(initialMessages)).
func NewPersistenceBridge(sess *session.TenantSession, initialCount int) *PersistenceBridge {
	return &PersistenceBridge{
		session:            sess,
		lastPersistedCount: initialCount,
	}
}

// IncrementalPersist persists new messages (those after lastPersistedCount) to the session.
// Skips system messages. Strips <system-reminder> tags before persisting.
// Updates lastPersistedCount on success.
// Returns nil if session is nil or all messages already persisted.
func (b *PersistenceBridge) IncrementalPersist(messages []llm.ChatMessage) error {
	if b.session == nil || len(messages) <= b.lastPersistedCount {
		return nil
	}
	persistOk := true
	for _, msg := range messages[b.lastPersistedCount:] {
		if msg.Role == "system" {
			continue
		}
		persistMsg := msg
		if strings.Contains(persistMsg.Content, "<system-reminder>") {
			persistMsg.Content = stripSystemReminder(persistMsg.Content)
		}
		if err := b.session.AddMessage(persistMsg); err != nil {
			log.WithError(err).Error("Failed to persist message to session")
			persistOk = false
			break
		}
	}
	if persistOk {
		b.lastPersistedCount = len(messages)
	}
	return nil
}

// RewriteAfterCompress clears the session and re-adds all compressed messages.
// Used after context compression when the entire session must be rewritten.
// Updates lastPersistedCount to totalMsgCount on success.
// Returns (true, nil) on success, (false, err) on partial/total failure.
func (b *PersistenceBridge) RewriteAfterCompress(sessionView []llm.ChatMessage, totalMsgCount int) (bool, error) {
	if b.session == nil {
		return true, nil
	}
	if err := b.session.Clear(); err != nil {
		log.WithError(err).Warn("Failed to clear session for compression, skipping persistence")
		return false, err
	}
	allOk := true
	for _, msg := range sessionView {
		if err := assertNoSystemPersist(msg); err != nil {
			continue
		}
		if err := b.session.AddMessage(msg); err != nil {
			log.WithError(err).Error("Partial write during compression, session may be corrupted")
			allOk = false
			break
		}
	}
	if allOk {
		b.lastPersistedCount = totalMsgCount
		return true, nil
	}
	return false, nil
}

// MarkAllPersisted updates the watermark to the given count without writing anything.
// Used after injecting synthetic messages (bg tasks, bg subagent notifications)
// that were already individually persisted.
func (b *PersistenceBridge) MarkAllPersisted(count int) {
	b.lastPersistedCount = count
}

// LastPersistedCount returns the current persistence watermark.
func (b *PersistenceBridge) LastPersistedCount() int {
	return b.lastPersistedCount
}

// ComputeEngineMessages returns messages that were produced during this Run
// (those after lastPersistedCount), for inclusion in RunOutput.EngineMessages.
// Returns nil if no new messages.
func (b *PersistenceBridge) ComputeEngineMessages(messages []llm.ChatMessage) []llm.ChatMessage {
	if len(messages) <= b.lastPersistedCount {
		return nil
	}
	engineMsgs := make([]llm.ChatMessage, len(messages)-b.lastPersistedCount)
	copy(engineMsgs, messages[b.lastPersistedCount:])
	return engineMsgs
}

// IsPersisted checks whether a message at the given index has already been persisted.
// Used by observation masking to decide if in-place updates should also be persisted.
func (b *PersistenceBridge) IsPersisted(messageIndex int) bool {
	return messageIndex < b.lastPersistedCount
}
