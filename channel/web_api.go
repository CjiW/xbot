package channel

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xbot/tools"
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
	Role      string  `json:"role"`
	Content   string  `json:"content"`
	CreatedAt string  `json:"created_at,omitempty"`
	ToolCalls *string `json:"tool_calls,omitempty"`
	Detail    *string `json:"detail,omitempty"` // iteration history JSON for assistant messages
}

// handleHistory handles GET /api/history
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

	// Count user messages as the history window.
	// Find the id of the N-th most recent user message, then fetch all displayable
	// messages from that point onward.
	limit := wc.config.MemoryWindow
	if limit <= 0 {
		limit = 50
	}
	var boundaryID sql.NullInt64
	err = wc.db.QueryRow(`
			SELECT id FROM session_messages
			WHERE tenant_id = ? AND role = 'user'
			ORDER BY id DESC
			LIMIT 1 OFFSET ?
		`, tenantID, limit).Scan(&boundaryID)
	if err != nil && err != sql.ErrNoRows {
		writeJSON(w, http.StatusInternalServerError, historyResponse{OK: false, Error: "query failed"})
		return
	}

	var rows *sql.Rows
	if boundaryID.Valid {
		rows, err = wc.db.Query(`
				SELECT role, content, created_at, tool_calls, detail
				FROM session_messages
				WHERE tenant_id = ? AND id >= ? AND role IN ('user', 'assistant')
				ORDER BY id ASC
			`, tenantID, boundaryID.Int64)
	} else {
		rows, err = wc.db.Query(`
				SELECT role, content, created_at, tool_calls, detail
				FROM session_messages
				WHERE tenant_id = ? AND role IN ('user', 'assistant')
				ORDER BY id ASC
			`, tenantID)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, historyResponse{OK: false, Error: "query failed"})
		return
	}
	defer rows.Close()

	var messages []histMsg
	for rows.Next() {
		var m histMsg
		var toolCalls, detail sql.NullString
		if err := rows.Scan(&m.Role, &m.Content, &m.CreatedAt, &toolCalls, &detail); err != nil {
			continue
		}
		if toolCalls.Valid {
			m.ToolCalls = &toolCalls.String
		}
		if detail.Valid {
			m.Detail = &detail.String
		}
		messages = append(messages, m)
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

	// Validate request size
	const maxSettingKeys = 20
	if len(req.Settings) > maxSettingKeys {
		writeJSON(w, http.StatusBadRequest, settingsResponse{
			OK: false, Error: fmt.Sprintf("too many settings (max %d)", maxSettingKeys),
		})
		return
	}
	const maxSettingValueLen = 32768 // 32KB per setting value
	for k, v := range req.Settings {
		if len(v) > maxSettingValueLen {
			writeJSON(w, http.StatusBadRequest, settingsResponse{
				OK:    false,
				Error: fmt.Sprintf("setting %q value too large (max %d bytes)", k, maxSettingValueLen),
			})
			return
		}
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
// Multi-Runner API
// ---------------------------------------------------------------------------

type runnersListResponse struct {
	OK      bool               `json:"ok"`
	Runners []tools.RunnerInfo `json:"runners,omitempty"`
	Error   string             `json:"error,omitempty"`
}

type runnerCreateRequest struct {
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	DockerImage string `json:"docker_image"`
	Workspace   string `json:"workspace"`
}

type runnerActiveResponse struct {
	OK    bool   `json:"ok"`
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

type runnerCommandResponse struct {
	OK      bool              `json:"ok"`
	Command string            `json:"command,omitempty"`
	Runner  *tools.RunnerInfo `json:"runner,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// handleRunners handles GET /api/runners (list) and POST /api/runners (create).
func (wc *WebChannel) handleRunners(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, runnersListResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		if wc.callbacks.RunnerList == nil {
			writeJSON(w, http.StatusOK, runnersListResponse{OK: true})
			return
		}
		runners, err := wc.callbacks.RunnerList(senderID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, runnersListResponse{OK: false, Error: "list failed"})
			return
		}
		writeJSON(w, http.StatusOK, runnersListResponse{OK: true, Runners: runners})
	case http.MethodPost:
		if wc.callbacks.RunnerCreate == nil {
			writeJSON(w, http.StatusServiceUnavailable, runnerCommandResponse{OK: false, Error: "runner management not configured"})
			return
		}
		var req runnerCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, runnerCommandResponse{OK: false, Error: "invalid request body"})
			return
		}
		if req.Name == "" {
			writeJSON(w, http.StatusBadRequest, runnerCommandResponse{OK: false, Error: "name is required"})
			return
		}
		cmd, err := wc.callbacks.RunnerCreate(senderID, req.Name, req.Mode, req.DockerImage, req.Workspace)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, runnerCommandResponse{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, runnerCommandResponse{
			OK:      true,
			Command: cmd,
			Runner: &tools.RunnerInfo{
				Name:        req.Name,
				Mode:        req.Mode,
				DockerImage: req.DockerImage,
				Workspace:   req.Workspace,
			},
		})
	}
}

// handleRunnerActive handles GET /api/runners/active (get) and PUT /api/runners/active (set).
func (wc *WebChannel) handleRunnerActive(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, runnerActiveResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		if wc.callbacks.RunnerGetActive == nil {
			writeJSON(w, http.StatusOK, runnerActiveResponse{OK: true, Name: ""})
			return
		}
		name, err := wc.callbacks.RunnerGetActive(senderID)
		if err != nil {
			writeJSON(w, http.StatusOK, runnerActiveResponse{OK: true, Name: ""})
			return
		}
		writeJSON(w, http.StatusOK, runnerActiveResponse{OK: true, Name: name})
	case http.MethodPut:
		if wc.callbacks.RunnerSetActive == nil {
			writeJSON(w, http.StatusServiceUnavailable, runnerActiveResponse{OK: false, Error: "runner management not configured"})
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			writeJSON(w, http.StatusBadRequest, runnerActiveResponse{OK: false, Error: "name is required"})
			return
		}
		if err := wc.callbacks.RunnerSetActive(senderID, req.Name); err != nil {
			writeJSON(w, http.StatusInternalServerError, runnerActiveResponse{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, runnerActiveResponse{OK: true, Name: req.Name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRunnerByName handles DELETE /api/runners/{name}.
func (wc *WebChannel) handleRunnerByName(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, runnerActiveResponse{OK: false, Error: "unauthorized"})
		return
	}

	// Extract runner name from URL: /api/runners/{name}
	name := strings.TrimPrefix(r.URL.Path, "/api/runners/")
	name = strings.TrimSuffix(name, "/")
	// Reject paths that look like other endpoints
	if name == "active" || name == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if r.Method == http.MethodDelete {
		if wc.callbacks.RunnerDelete == nil {
			writeJSON(w, http.StatusServiceUnavailable, runnerActiveResponse{OK: false, Error: "runner management not configured"})
			return
		}
		if err := wc.callbacks.RunnerDelete(senderID, name); err != nil {
			writeJSON(w, http.StatusInternalServerError, runnerActiveResponse{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, runnerActiveResponse{OK: true})
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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

// ---------------------------------------------------------------------------
// LLM Config API
// ---------------------------------------------------------------------------

type llmConfigResponse struct {
	OK         bool     `json:"ok"`
	IsGlobal   bool     `json:"is_global,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	BaseURL    string   `json:"base_url,omitempty"`
	Model      string   `json:"model,omitempty"`
	Models     []string `json:"models,omitempty"`
	MaxContext int      `json:"max_context,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type llmConfigSetRequest struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	Model    string `json:"model"`
}

type llmModelSetRequest struct {
	Model string `json:"model"`
}

type llmMaxContextRequest struct {
	MaxContext int `json:"max_context"`
}

// handleLLMConfig handles GET/POST/DELETE /api/llm-config
func (wc *WebChannel) handleLLMConfig(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, llmConfigResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		wc.handleLLMConfigGet(w, senderID)
	case http.MethodPost:
		wc.handleLLMConfigSet(w, r, senderID)
	case http.MethodDelete:
		wc.handleLLMConfigDelete(w, senderID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (wc *WebChannel) handleLLMConfigGet(w http.ResponseWriter, senderID string) {
	if wc.callbacks.LLMGetConfig == nil {
		writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})
		return
	}

	provider, baseURL, model, ok := wc.callbacks.LLMGetConfig(senderID)

	// Also fetch available models if a list callback exists
	var models []string
	if wc.callbacks.LLMList != nil {
		var currentModel string
		models, currentModel = wc.callbacks.LLMList(senderID)
		if currentModel != "" {
			model = currentModel
		}
	}

	resp := llmConfigResponse{
		OK:       true,
		IsGlobal: !ok,
		Provider: provider,
		BaseURL:  baseURL,
		Model:    model,
		Models:   models,
	}
	// Also fetch max context if callback exists
	if wc.callbacks.LLMGetMaxContext != nil {
		resp.MaxContext = wc.callbacks.LLMGetMaxContext(senderID)
	}
	writeJSON(w, http.StatusOK, resp)

}

func (wc *WebChannel) handleLLMConfigSet(w http.ResponseWriter, r *http.Request, senderID string) {
	if wc.callbacks.LLMSetConfig == nil {
		writeJSON(w, http.StatusServiceUnavailable, llmConfigResponse{OK: false, Error: "not configured"})
		return
	}

	var req llmConfigSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "invalid request body"})
		return
	}

	if req.Provider == "" || req.BaseURL == "" || req.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "provider, base_url, api_key are required"})
		return
	}

	if err := wc.callbacks.LLMSetConfig(senderID, req.Provider, req.BaseURL, req.APIKey, req.Model); err != nil {
		writeJSON(w, http.StatusInternalServerError, llmConfigResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})
}

func (wc *WebChannel) handleLLMConfigDelete(w http.ResponseWriter, senderID string) {
	if wc.callbacks.LLMDelete == nil {
		writeJSON(w, http.StatusServiceUnavailable, llmConfigResponse{OK: false, Error: "not configured"})
		return
	}

	if err := wc.callbacks.LLMDelete(senderID); err != nil {
		writeJSON(w, http.StatusInternalServerError, llmConfigResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})
}

// handleLLMMaxContext handles GET/POST /api/llm-max-context
func (wc *WebChannel) handleLLMMaxContext(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, llmConfigResponse{OK: false, Error: "unauthorized"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		if wc.callbacks.LLMGetMaxContext == nil {
			writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})
			return
		}
		maxCtx := wc.callbacks.LLMGetMaxContext(senderID)
		writeJSON(w, http.StatusOK, llmConfigResponse{OK: true, MaxContext: maxCtx})

	case http.MethodPost:
		if wc.callbacks.LLMSetMaxContext == nil {
			writeJSON(w, http.StatusServiceUnavailable, llmConfigResponse{OK: false, Error: "not configured"})
			return
		}
		var req llmMaxContextRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "invalid request body"})
			return
		}
		if req.MaxContext < 0 {
			writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "max_context must be >= 0"})
			return
		}
		if err := wc.callbacks.LLMSetMaxContext(senderID, req.MaxContext); err != nil {
			writeJSON(w, http.StatusInternalServerError, llmConfigResponse{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleLLMModelSet handles POST /api/llm-config/model (switch model only)
func (wc *WebChannel) handleLLMModelSet(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, llmConfigResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.LLMSet == nil {
		writeJSON(w, http.StatusServiceUnavailable, llmConfigResponse{OK: false, Error: "not configured"})
		return
	}

	var req llmModelSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "invalid request body"})
		return
	}

	if req.Model == "" {
		writeJSON(w, http.StatusBadRequest, llmConfigResponse{OK: false, Error: "model is required"})
		return
	}

	if err := wc.callbacks.LLMSet(senderID, req.Model); err != nil {
		writeJSON(w, http.StatusInternalServerError, llmConfigResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, llmConfigResponse{OK: true})
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

// ---------------------------------------------------------------------------
// /api/market/my — list user's own agents/skills with publish status
// ---------------------------------------------------------------------------

type myMarketEntry struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Published   bool   `json:"published"`
}

type myMarketResponse struct {
	OK      bool            `json:"ok"`
	Entries []myMarketEntry `json:"entries,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// handleMarketMy handles GET /api/market/my?type=skill
func (wc *WebChannel) handleMarketMy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, myMarketResponse{OK: false, Error: "method not allowed"})
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, myMarketResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.RegistryListMy == nil {
		writeJSON(w, http.StatusOK, myMarketResponse{OK: true, Entries: nil})
		return
	}

	entryType := r.URL.Query().Get("type")
	published, local, err := wc.callbacks.RegistryListMy(senderID, entryType)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, myMarketResponse{OK: false, Error: "list failed"})
		return
	}

	// Build published name set for lookup (only public entries count as published)
	publishedSet := make(map[string]string) // name -> description
	for _, pe := range published {
		if pe.Sharing == "public" {
			publishedSet[pe.Name] = pe.Description
		}
	}

	result := make([]myMarketEntry, 0)
	for _, key := range local {
		// key format: "skill:name" or "agent:name"
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		typ, name := parts[0], parts[1]

		entry := myMarketEntry{
			Name: name,
			Type: typ,
		}
		if desc, ok := publishedSet[name]; ok {
			entry.Published = true
			entry.Description = desc
		}
		result = append(result, entry)
	}

	writeJSON(w, http.StatusOK, myMarketResponse{OK: true, Entries: result})
}

// ---------------------------------------------------------------------------
// /api/market/publish — publish user's skill/agent to marketplace
// ---------------------------------------------------------------------------

type marketPublishRequest struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// handleMarketPublish handles POST /api/market/publish
func (wc *WebChannel) handleMarketPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, marketResponse{OK: false, Error: "method not allowed"})
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, marketResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.RegistryPublish == nil {
		writeJSON(w, http.StatusServiceUnavailable, marketResponse{OK: false, Error: "registry not configured"})
		return
	}

	var req marketPublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "invalid request body"})
		return
	}

	if req.Type == "" || req.Name == "" {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "type and name are required"})
		return
	}

	if err := wc.callbacks.RegistryPublish(req.Type, req.Name, senderID); err != nil {
		writeJSON(w, http.StatusInternalServerError, marketResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, marketResponse{OK: true})
}

// ---------------------------------------------------------------------------
// /api/market/unpublish — unpublish user's skill/agent from marketplace
// ---------------------------------------------------------------------------

type marketUnpublishRequest struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// handleMarketUnpublish handles POST /api/market/unpublish
func (wc *WebChannel) handleMarketUnpublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, marketResponse{OK: false, Error: "method not allowed"})
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		writeJSON(w, http.StatusUnauthorized, marketResponse{OK: false, Error: "unauthorized"})
		return
	}

	if wc.callbacks.RegistryUnpublish == nil {
		writeJSON(w, http.StatusServiceUnavailable, marketResponse{OK: false, Error: "registry not configured"})
		return
	}

	var req marketUnpublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "invalid request body"})
		return
	}

	if req.Type == "" || req.Name == "" {
		writeJSON(w, http.StatusBadRequest, marketResponse{OK: false, Error: "type and name are required"})
		return
	}

	if err := wc.callbacks.RegistryUnpublish(req.Type, req.Name, senderID); err != nil {
		writeJSON(w, http.StatusInternalServerError, marketResponse{OK: false, Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, marketResponse{OK: true})
}
