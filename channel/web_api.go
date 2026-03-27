package channel

import (
	"net/http"
	"strconv"
)

// ---------------------------------------------------------------------------
// REST API handlers
// ---------------------------------------------------------------------------

type historyResponse struct {
	OK       bool         `json:"ok"`
	Messages []histMsg    `json:"messages,omitempty"`
	Error    string       `json:"error,omitempty"`
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

	// Query session messages
	rows, err := wc.db.Query(`
		SELECT role, content, created_at
		FROM session_messages
		WHERE tenant_id = ?
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
