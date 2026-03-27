package channel

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// ---------------------------------------------------------------------------
// REST API handlers
// ---------------------------------------------------------------------------

type historyResponse struct {
	OK       bool      `json:"ok"`
	Messages []histMsg `json:"messages,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type histMsg struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at,omitempty"`
}

// handleHistory handles GET /api/history?limit=50
func (wc *WebChannel) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse limit
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	// Find tenant ID for this web user
	var tenantID int64
	err := wc.db.QueryRow(
		"SELECT id FROM tenants WHERE channel = 'web' AND chat_id = ?", senderID,
	).Scan(&tenantID)
	if err != nil {
		// No tenant yet = no history
		writeJSON(w, http.StatusOK, historyResponse{OK: true, Messages: nil})
		return
	}

	// Query session messages (exclude tool messages from history)
	rows, err := wc.db.Query(`
		SELECT role, content, created_at
		FROM session_messages
		WHERE tenant_id = ? AND role != 'tool'
		ORDER BY id DESC
		LIMIT ?
	`, tenantID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, historyResponse{OK: false, Error: "query failed"})
		return
	}
	defer rows.Close()

	var messages []histMsg
	for rows.Next() {
		var m histMsg
		if err := rows.Scan(&m.Role, &m.Content, &m.CreatedAt); err != nil {
			continue
		}
		messages = append(messages, m)
	}

	// Reverse to chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	writeJSON(w, http.StatusOK, historyResponse{OK: true, Messages: messages})
}

// ---------------------------------------------------------------------------
// Settings API
// ---------------------------------------------------------------------------

type settingsResponse struct {
	OK       bool              `json:"ok"`
	Settings map[string]string `json:"settings,omitempty"`
	Error    string            `json:"error,omitempty"`
}

type updateSettingsRequest struct {
	Settings map[string]string `json:"settings"`
}

// handleSettings handles GET/PUT /api/settings
func (wc *WebChannel) handleSettings(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, settingsResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		wc.handleGetSettings(w, r, senderID)
	case http.MethodPut:
		wc.handleUpdateSettings(w, r, senderID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGetSettings returns all settings for the current user
func (wc *WebChannel) handleGetSettings(w http.ResponseWriter, r *http.Request, senderID string) {
	rows, err := wc.db.Query(
		"SELECT key, value FROM user_settings WHERE channel = 'web' AND sender_id = ?", senderID,
	)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, settingsResponse{OK: false, Error: "query failed"})
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		settings[k] = v
	}

	writeJSON(w, http.StatusOK, settingsResponse{OK: true, Settings: settings})
}

// handleUpdateSettings upserts settings for the current user
func (wc *WebChannel) handleUpdateSettings(w http.ResponseWriter, r *http.Request, senderID string) {
	var req updateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, settingsResponse{OK: false, Error: "invalid request body"})
		return
	}

	if len(req.Settings) == 0 {
		writeJSON(w, http.StatusBadRequest, settingsResponse{OK: false, Error: "no settings provided"})
		return
	}

	now := time.Now().Unix()
	for k, v := range req.Settings {
		_, err := wc.db.Exec(
			"INSERT OR REPLACE INTO user_settings (channel, sender_id, key, value, updated_at) VALUES ('web', ?, ?, ?, ?)",
			senderID, k, v, now,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, settingsResponse{OK: false, Error: "update failed"})
			return
		}
	}

	writeJSON(w, http.StatusOK, settingsResponse{OK: true})
}
