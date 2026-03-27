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

// ---------------------------------------------------------------------------
// Runner Token API
// ---------------------------------------------------------------------------

type runnerTokenResponse struct {
	OK      bool   `json:"ok"`
	Command string `json:"command"`
	Error   string `json:"error,omitempty"`
}

type runnerTokenGenerateRequest struct {
	Mode        string `json:"mode"`
	DockerImage string `json:"docker_image"`
	Workspace   string `json:"workspace"`
}

// handleRunnerToken handles GET/POST/DELETE /api/runner/token
func (wc *WebChannel) handleRunnerToken(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, runnerTokenResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		wc.handleRunnerTokenGet(w, senderID)
	case http.MethodPost:
		wc.handleRunnerTokenGenerate(w, r, senderID)
	case http.MethodDelete:
		wc.handleRunnerTokenRevoke(w, senderID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wc *WebChannel) handleRunnerTokenGet(w http.ResponseWriter, senderID string) {
	if wc.callbacks.RunnerTokenGet == nil {
		writeJSON(w, http.StatusOK, runnerTokenResponse{OK: true, Command: ""})
		return
	}
	cmd := wc.callbacks.RunnerTokenGet(senderID)
	writeJSON(w, http.StatusOK, runnerTokenResponse{OK: true, Command: cmd})
}

func (wc *WebChannel) handleRunnerTokenGenerate(w http.ResponseWriter, r *http.Request, senderID string) {
	if wc.callbacks.RunnerTokenGenerate == nil {
		writeJSON(w, http.StatusServiceUnavailable, runnerTokenResponse{OK: false, Error: "runner token not configured"})
		return
	}

	var req runnerTokenGenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Use defaults on decode error
		req.Mode = "native"
	}

	cmd, err := wc.callbacks.RunnerTokenGenerate(senderID, req.Mode, req.DockerImage, req.Workspace)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, runnerTokenResponse{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, runnerTokenResponse{OK: true, Command: cmd})
}

func (wc *WebChannel) handleRunnerTokenRevoke(w http.ResponseWriter, senderID string) {
	if wc.callbacks.RunnerTokenRevoke == nil {
		writeJSON(w, http.StatusServiceUnavailable, runnerTokenResponse{OK: false, Error: "runner token not configured"})
		return
	}
	if err := wc.callbacks.RunnerTokenRevoke(senderID); err != nil {
		writeJSON(w, http.StatusInternalServerError, runnerTokenResponse{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, runnerTokenResponse{OK: true})
}

// ---------------------------------------------------------------------------
// Market API
// ---------------------------------------------------------------------------

type marketEntry struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	CreatedAt   string `json:"created_at"`
	Installed   bool   `json:"installed"`
}

type marketResponse struct {
	OK      bool          `json:"ok"`
	Entries []marketEntry `json:"entries,omitempty"`
	Error   string        `json:"error,omitempty"`
}

type marketInstallRequest struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

type marketUninstallRequest struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// handleMarket handles GET /api/market?type=agent&limit=20&offset=0
func (wc *WebChannel) handleMarket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, marketResponse{OK: false, Error: "method not allowed"})
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, marketResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.RegistryBrowse == nil {
		writeJSON(w, http.StatusOK, marketResponse{OK: true, Entries: nil})
		return
	}

	entryType := r.URL.Query().Get("type")
	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			offset = n
		}
	}

	entries, err := wc.callbacks.RegistryBrowse(entryType, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, marketResponse{OK: false, Error: "browse failed"})
		return
	}

	// Compute installed set for the user
	installedSet := make(map[string]bool)
	if wc.callbacks.RegistryListMy != nil {
		_, installed, err := wc.callbacks.RegistryListMy(senderID, entryType)
		if err == nil {
			for _, name := range installed {
				installedSet[name] = true
			}
		}
	}

	// Build response entries
	result := make([]marketEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, marketEntry{
			ID:          e.ID,
			Type:        e.Type,
			Name:        e.Name,
			Description: e.Description,
			Author:      e.Author,
			CreatedAt:   time.UnixMilli(e.CreatedAt).UTC().Format(time.RFC3339),
			Installed:   installedSet[e.Name],
		})
	}

	writeJSON(w, http.StatusOK, marketResponse{OK: true, Entries: result})
}

// handleMarketInstall handles POST /api/market/install
func (wc *WebChannel) handleMarketInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, marketResponse{OK: false, Error: "method not allowed"})
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, marketResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.RegistryInstall == nil {
		writeJSON(w, http.StatusServiceUnavailable, marketResponse{OK: false, Error: "registry not configured"})
		return
	}

	var req marketInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "invalid request body"})
		return
	}

	if err := wc.callbacks.RegistryInstall(req.Type, req.ID, senderID); err != nil {
		writeJSON(w, http.StatusInternalServerError, marketResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, marketResponse{OK: true})
}

// handleMarketUninstall handles POST /api/market/uninstall
func (wc *WebChannel) handleMarketUninstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, marketResponse{OK: false, Error: "method not allowed"})
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, marketResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.RegistryUninstall == nil {
		writeJSON(w, http.StatusServiceUnavailable, marketResponse{OK: false, Error: "registry not configured"})
		return
	}

	var req marketUninstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "invalid request body"})
		return
	}

	if err := wc.callbacks.RegistryUninstall(req.Type, req.Name, senderID); err != nil {
		writeJSON(w, http.StatusInternalServerError, marketResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, marketResponse{OK: true})
}
