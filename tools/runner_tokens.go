package tools

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"

	log "xbot/logger"
)

// RunnerTokenSettings holds per-user runner configuration associated with a token.
type RunnerTokenSettings struct {
	Mode        string // "native" or "docker"
	DockerImage string
	Workspace   string
}

// RunnerLLMSettings holds optional local LLM configuration for a runner.
type RunnerLLMSettings struct {
	Provider string
	APIKey   string
	Model    string
	BaseURL  string
}

// HasLLM returns true if the runner declares LLM capability.
// Note: APIKey is not required here because TUI runners hold their own
// API key locally — the server only needs to know the runner CAN do LLM.
func (l *RunnerLLMSettings) HasLLM() bool {
	return l.Provider != ""
}

// RunnerTokenEntry represents a single per-user runner token.
type RunnerTokenEntry struct {
	Token     string
	UserID    string
	CreatedAt time.Time
	Settings  RunnerTokenSettings
}

// RunnerTokenStore persists per-user runner tokens in SQLite.
// Each user has at most one active token; generating a new one replaces the old.
type RunnerTokenStore struct {
	db *sql.DB
}

// NewRunnerTokenStore creates a token store backed by the given database connection.
func NewRunnerTokenStore(db *sql.DB) *RunnerTokenStore {
	return &RunnerTokenStore{db: db}
}

// Generate creates a new token for the given user, replacing any existing one.
// Returns the new entry.
func (s *RunnerTokenStore) Generate(userID string, settings RunnerTokenSettings) (*RunnerTokenEntry, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate random token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(b)

	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO runner_tokens (user_id, token, mode, docker_image, workspace, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			token = excluded.token,
			mode = excluded.mode,
			docker_image = excluded.docker_image,
			workspace = excluded.workspace,
			created_at = excluded.created_at
	`, userID, token, settings.Mode, settings.DockerImage, settings.Workspace, now.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("store runner token: %w", err)
	}

	return &RunnerTokenEntry{
		Token:     token,
		UserID:    userID,
		CreatedAt: now,
		Settings:  settings,
	}, nil
}

// Validate checks whether the token exists and is owned by the given user.
func (s *RunnerTokenStore) Validate(token, userID string) bool {
	var storedToken string
	err := s.db.QueryRow(
		"SELECT token FROM runner_tokens WHERE user_id = ?", userID,
	).Scan(&storedToken)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(storedToken), []byte(token)) == 1
}

// Get returns the current token entry for a user, or nil if none exists.
func (s *RunnerTokenStore) Get(userID string) *RunnerTokenEntry {
	var token, mode, dockerImage, workspace, createdAtStr string
	err := s.db.QueryRow(
		"SELECT token, mode, docker_image, workspace, created_at FROM runner_tokens WHERE user_id = ?",
		userID,
	).Scan(&token, &mode, &dockerImage, &workspace, &createdAtStr)
	if err != nil {
		return nil
	}
	createdAt, _ := time.Parse(time.RFC3339, createdAtStr)
	return &RunnerTokenEntry{
		Token:     token,
		UserID:    userID,
		CreatedAt: createdAt,
		Settings: RunnerTokenSettings{
			Mode:        mode,
			DockerImage: dockerImage,
			Workspace:   workspace,
		},
	}
}

// Revoke deletes the token for a user.
func (s *RunnerTokenStore) Revoke(userID string) {
	_, err := s.db.Exec("DELETE FROM runner_tokens WHERE user_id = ?", userID)
	if err != nil {
		log.WithError(err).Error("Failed to revoke runner token")
	}
}

// ---------------------------------------------------------------------------
// Multi-runner support (runners table)
// ---------------------------------------------------------------------------

// RunnerInfo describes a single runner belonging to a user.
type RunnerInfo struct {
	Name        string `json:"name"`
	Token       string `json:"token,omitempty"`
	Mode        string `json:"mode"`
	DockerImage string `json:"docker_image"`
	Workspace   string `json:"workspace"`
	Shell       string `json:"shell,omitempty"`
	CreatedAt   string `json:"created_at"`
	Online      bool   `json:"online"`
	// Local LLM configuration (set via web settings).
	LLMProvider string `json:"llm_provider,omitempty"`
	LLMAPIKey   string `json:"llm_api_key,omitempty"`
	LLMModel    string `json:"llm_model,omitempty"`
	LLMBaseURL  string `json:"llm_base_url,omitempty"`
}

// LLMSettings returns the runner's LLM configuration as RunnerLLMSettings.
func (r RunnerInfo) LLMSettings() RunnerLLMSettings {
	return RunnerLLMSettings{
		Provider: r.LLMProvider,
		APIKey:   r.LLMAPIKey,
		Model:    r.LLMModel,
		BaseURL:  r.LLMBaseURL,
	}
}

// CreateRunner creates a new named runner for the user, generates a token.
// Returns the token and the xbot-runner connect command fragment.
func (s *RunnerTokenStore) CreateRunner(userID, name, mode, dockerImage, workspace string, llm RunnerLLMSettings) (token, command string, err error) {
	if name == "" {
		return "", "", fmt.Errorf("runner name is required")
	}
	if mode == "" {
		mode = "native"
	}
	if dockerImage == "" {
		dockerImage = "ubuntu:22.04"
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.Begin()
	if err != nil {
		return "", "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
				INSERT INTO runners (user_id, name, token, mode, docker_image, workspace, llm_provider, llm_api_key, llm_model, llm_base_url, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(user_id, name) DO UPDATE SET
					token = excluded.token,
					mode = excluded.mode,
					docker_image = excluded.docker_image,
					workspace = excluded.workspace,
					llm_provider = excluded.llm_provider,
					llm_api_key = excluded.llm_api_key,
					llm_model = excluded.llm_model,
					llm_base_url = excluded.llm_base_url,
					created_at = excluded.created_at
			`, userID, name, token, mode, dockerImage, workspace, llm.Provider, llm.APIKey, llm.Model, llm.BaseURL, now)
	if err != nil {
		return "", "", fmt.Errorf("insert runner: %w", err)
	}

	// Also upsert into runner_tokens for backward compatibility.
	_, err = tx.Exec(`
			INSERT INTO runner_tokens (user_id, token, mode, docker_image, workspace, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id) DO UPDATE SET
				token = excluded.token,
				mode = excluded.mode,
				docker_image = excluded.docker_image,
				workspace = excluded.workspace,
				created_at = excluded.created_at
		`, userID, token, mode, dockerImage, workspace, now)
	if err != nil {
		return "", "", fmt.Errorf("upsert runner_tokens: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("commit tx: %w", err)
	}

	// If this is the user's first runner, set it as active.
	// If this is the user's first runner, set it as active.
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM runners WHERE user_id = ?", userID).Scan(&count)
	if count <= 1 {
		s.SetActiveRunner(userID, name)
	}

	return token, token, nil
}

// ListRunners returns all runners for a user.
func (s *RunnerTokenStore) ListRunners(userID string) ([]RunnerInfo, error) {
	rows, err := s.db.Query(
		"SELECT name, token, mode, docker_image, COALESCE(workspace,''), COALESCE(created_at,''), COALESCE(llm_provider,''), COALESCE(llm_api_key,''), COALESCE(llm_model,''), COALESCE(llm_base_url,'') FROM runners WHERE user_id = ? ORDER BY created_at",
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list runners: %w", err)
	}
	defer rows.Close()

	var runners []RunnerInfo
	for rows.Next() {
		var r RunnerInfo
		if err := rows.Scan(&r.Name, &r.Token, &r.Mode, &r.DockerImage, &r.Workspace, &r.CreatedAt, &r.LLMProvider, &r.LLMAPIKey, &r.LLMModel, &r.LLMBaseURL); err != nil {
			log.WithError(err).Warn("Failed to scan runner row, skipping")
			continue
		}
		runners = append(runners, r)
	}
	return runners, nil
}

// DeleteRunner removes a runner by name.
func (s *RunnerTokenStore) DeleteRunner(userID, name string) error {
	_, err := s.db.Exec("DELETE FROM runners WHERE user_id = ? AND name = ?", userID, name)
	if err != nil {
		return fmt.Errorf("delete runner: %w", err)
	}
	return nil
}

// UpdateRunnerLLM updates the LLM settings for an existing runner.
// If the runner record doesn't exist (e.g., TUI direct connect), it creates one.
func (s *RunnerTokenStore) UpdateRunnerLLM(userID, name string, llm RunnerLLMSettings) error {
	// Upsert: ensure a runners record exists even for TUI direct connects
	// that weren't created via the web RunnerPanel.
	_, err := s.db.Exec(`
		INSERT INTO runners (user_id, name, token, mode, workspace, llm_provider, llm_api_key, llm_model, llm_base_url, created_at)
		VALUES (?, ?, '', 'native', '', ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(user_id, name) DO UPDATE SET
			llm_provider = excluded.llm_provider,
			llm_api_key = excluded.llm_api_key,
			llm_model = excluded.llm_model,
			llm_base_url = excluded.llm_base_url
	`, userID, name, llm.Provider, llm.APIKey, llm.Model, llm.BaseURL)
	if err != nil {
		return fmt.Errorf("upsert runner LLM: %w", err)
	}
	return nil
}

// GetActiveRunner returns the name of the active runner for a user.
func (s *RunnerTokenStore) GetActiveRunner(userID string) (string, error) {
	var value string
	err := s.db.QueryRow(
		"SELECT value FROM user_settings WHERE channel = 'web' AND sender_id = ? AND key = 'active_runner'",
		userID,
	).Scan(&value)
	if err != nil {
		// Fallback: return first runner name if any exist.
		var name string
		if err2 := s.db.QueryRow("SELECT name FROM runners WHERE user_id = ? ORDER BY created_at LIMIT 1", userID).Scan(&name); err2 != nil {
			return "", fmt.Errorf("no active runner")
		}
		return name, nil
	}
	return value, nil
}

// SetActiveRunner sets the active runner for a user.
func (s *RunnerTokenStore) SetActiveRunner(userID, name string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO user_settings (channel, sender_id, key, value, updated_at)
		VALUES ('web', ?, 'active_runner', ?, ?)
	`, userID, name, now)
	if err != nil {
		return fmt.Errorf("set active runner: %w", err)
	}
	return nil
}

// FindByToken looks up a runner by token and returns the userID and runnerName.
func (s *RunnerTokenStore) FindByToken(token string) (userID, runnerName string, err error) {
	err = s.db.QueryRow(
		"SELECT user_id, name FROM runners WHERE token = ?", token,
	).Scan(&userID, &runnerName)
	if err != nil {
		return "", "", fmt.Errorf("runner not found for token")
	}
	return userID, runnerName, nil
}

// FindByTokenInRunnerTokens looks up the legacy runner_tokens table by token.
// Returns userID or empty string if not found.
func (s *RunnerTokenStore) FindByTokenInRunnerTokens(token string) (userID string) {
	err := s.db.QueryRow(
		"SELECT user_id FROM runner_tokens WHERE token = ?", token,
	).Scan(&userID)
	if err != nil {
		return ""
	}
	return userID
}

// ListAllRunners returns all runners for a user, including the built-in docker sandbox
// (if available) prepended to the list. Online status is populated from RemoteSandbox.
func ListAllRunners(senderID string) ([]RunnerInfo, error) {
	db := GetRunnerTokenDB()
	if db == nil {
		return nil, fmt.Errorf("runner management not configured")
	}
	store := NewRunnerTokenStore(db)
	runners, err := store.ListRunners(senderID)
	if err != nil {
		return nil, err
	}
	// Populate online status from RemoteSandbox or SandboxRouter
	if sb := GetSandbox(); sb != nil {
		switch router := sb.(type) {
		case *SandboxRouter:
			for i := range runners {
				runners[i].Online = router.IsRunnerOnline(senderID, runners[i].Name)
				if runners[i].Online && router.remote != nil {
					w, s := router.remote.GetConnectionInfo(senderID, runners[i].Name)
					if w != "" {
						runners[i].Workspace = w
					}
					if s != "" {
						runners[i].Shell = s
					}
				}
			}
			// Inject built-in docker sandbox if available
			if router.HasDocker() {
				dockerEntry := RunnerInfo{
					Name:        BuiltinDockerRunnerName,
					Mode:        "docker",
					DockerImage: router.DockerImage(),
					Online:      true,
				}
				runners = append([]RunnerInfo{dockerEntry}, runners...)
			}
		case *RemoteSandbox:
			for i := range runners {
				runners[i].Online = router.IsRunnerOnline(senderID, runners[i].Name)
				if runners[i].Online {
					w, s := router.GetConnectionInfo(senderID, runners[i].Name)
					if w != "" {
						runners[i].Workspace = w
					}
					if s != "" {
						runners[i].Shell = s
					}
				}
			}
		}
	}
	return runners, nil
}
