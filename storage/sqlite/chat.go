package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	log "xbot/logger"
)

// UserChat represents a chatroom owned by a user.
type UserChat struct {
	ID        int64
	Channel   string
	SenderID  string
	ChatID    string
	Label     string
	CreatedAt time.Time
}

// UserChatWithPreview extends UserChat with tenant metadata (last active, message preview).
type UserChatWithPreview struct {
	ChatID     string    `json:"chat_id"`
	Label      string    `json:"label"`
	LastActive time.Time `json:"last_active"`
	Preview    string    `json:"preview"`
	IsCurrent  bool      `json:"is_current"`
}

// ChatService manages user chatrooms (multi-chat support).
type ChatService struct {
	conn *sql.DB
}

// NewChatService creates a new ChatService.
func NewChatService(conn *sql.DB) *ChatService {
	return &ChatService{conn: conn}
}

// ListUserChats returns all chatrooms for a user in a given channel.
// Includes the default chat (chatID=senderID) even if not in user_chats table.
// If currentChatID is non-empty, marks that chat as current.
func (s *ChatService) ListUserChats(channel, senderID, currentChatID string) ([]UserChatWithPreview, error) {
	conn := s.conn

	// Collect all chat IDs for this user:
	// 1. Default chat (chat_id = senderID)
	// 2. User-created chats from user_chats table
	chatIDs := []string{senderID}

	rows, err := conn.Query(
		"SELECT chat_id, label FROM user_chats WHERE channel = ? AND sender_id = ?",
		channel, senderID,
	)
	if err != nil {
		return nil, fmt.Errorf("list user chats: %w", err)
	}
	defer rows.Close()

	labelMap := map[string]string{}
	for rows.Next() {
		var cid, label string
		if err := rows.Scan(&cid, &label); err != nil {
			continue
		}
		chatIDs = append(chatIDs, cid)
		labelMap[cid] = label
	}

	// Build result with tenant metadata
	var result []UserChatWithPreview
	for _, cid := range chatIDs {
		var lastActive time.Time
		var preview string
		var tenantID int64

		err := conn.QueryRow(
			"SELECT id, last_active_at FROM tenants WHERE channel = ? AND chat_id = ?",
			channel, cid,
		).Scan(&tenantID, &lastActive)

		if err == sql.ErrNoRows {
			// Tenant doesn't exist yet (new chat, no messages)
			lastActive = time.Time{}
		} else if err != nil {
			lastActive = time.Time{}
		} else {
			// Get last message preview
			_ = conn.QueryRow(`
				SELECT content FROM session_messages
				WHERE tenant_id = ? AND role IN ('user', 'assistant')
				ORDER BY id DESC LIMIT 1`, tenantID).Scan(&preview)
		}

		label := labelMap[cid]
		if label == "" && cid == senderID {
			label = "默认会话"
		}

		result = append(result, UserChatWithPreview{
			ChatID:     cid,
			Label:      label,
			LastActive: lastActive,
			Preview:    truncate(preview, 80),
			IsCurrent:  cid == currentChatID,
		})
	}

	return result, nil
}

// CreateChat creates a new chatroom for a user. Returns the new chatID.
func (s *ChatService) CreateChat(channel, senderID, label string) (string, error) {
	conn := s.conn

	// Generate a unique chat ID
	var chatID string
	for i := 0; i < 10; i++ {
		var hex string
		err := conn.QueryRow("SELECT hex(randomblob(6))").Scan(&hex)
		if err != nil {
			return "", fmt.Errorf("generate chat id: %w", err)
		}
		chatID = "chat_" + hex

		// Check uniqueness
		var count int
		err = conn.QueryRow(
			"SELECT COUNT(*) FROM user_chats WHERE channel = ? AND sender_id = ? AND chat_id = ?",
			channel, senderID, chatID,
		).Scan(&count)
		if err == nil && count == 0 {
			break
		}
		chatID = ""
	}
	if chatID == "" {
		return "", fmt.Errorf("failed to generate unique chat id")
	}

	if label == "" {
		label = "新会话"
	}

	_, err := conn.Exec(
		"INSERT INTO user_chats (channel, sender_id, chat_id, label) VALUES (?, ?, ?, ?)",
		channel, senderID, chatID, label,
	)
	if err != nil {
		return "", fmt.Errorf("create chat: %w", err)
	}

	log.WithFields(log.Fields{
		"channel": channel, "sender": senderID, "chat_id": chatID, "label": label,
	}).Info("Chat created")
	return chatID, nil
}

// DeleteChat removes a chatroom. The default chat (chatID=senderID) cannot be deleted.
// Deletes the tenant and all associated data (cascading).
func (s *ChatService) DeleteChat(channel, senderID, chatID string) error {
	if chatID == senderID {
		return fmt.Errorf("cannot delete the default chat")
	}

	conn := s.conn

	// Verify ownership
	var count int
	err := conn.QueryRow(
		"SELECT COUNT(*) FROM user_chats WHERE channel = ? AND sender_id = ? AND chat_id = ?",
		channel, senderID, chatID,
	).Scan(&count)
	if err != nil {
		return fmt.Errorf("check chat ownership: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("chat not found or not owned by user")
	}

	// Delete from user_chats
	_, err = conn.Exec(
		"DELETE FROM user_chats WHERE channel = ? AND sender_id = ? AND chat_id = ?",
		channel, senderID, chatID,
	)
	if err != nil {
		return fmt.Errorf("delete chat record: %w", err)
	}

	// Delete tenant (cascades to session_messages, memory, etc.)
	_, err = conn.Exec(
		"DELETE FROM tenants WHERE channel = ? AND chat_id = ?",
		channel, chatID,
	)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}

	log.WithFields(log.Fields{
		"channel": channel, "sender": senderID, "chat_id": chatID,
	}).Info("Chat deleted")
	return nil
}

// RenameChat updates the label of a chatroom.
func (s *ChatService) RenameChat(channel, senderID, chatID, label string) error {
	if chatID == senderID {
		// Default chat: insert or update in user_chats
		conn := s.conn
		_, err := conn.Exec(`
			INSERT INTO user_chats (channel, sender_id, chat_id, label)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(channel, sender_id, chat_id) DO UPDATE SET label = ?`,
			channel, senderID, chatID, label, label,
		)
		return err
	}

	_, err := s.conn.Exec(
		"UPDATE user_chats SET label = ? WHERE channel = ? AND sender_id = ? AND chat_id = ?",
		label, channel, senderID, chatID,
	)
	return err
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-3]) + "..."
}
