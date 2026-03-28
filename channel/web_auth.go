package channel

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	log "xbot/logger"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// strongPasswordChars defines the character sets for password generation.
var strongPasswordChars = []string{
	"abcdefghijklmnopqrstuvwxyz",
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	"0123456789",
	"!@#$%^&*-_=+?",
}

// GenerateStrongPassword generates a cryptographically secure random password.
func GenerateStrongPassword(length int) (string, error) {
	if length < 12 {
		length = 16
	}
	allChars := strings.Join(strongPasswordChars, "")

	var password strings.Builder
	// Ensure at least one character from each set
	for _, set := range strongPasswordChars {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return "", err
		}
		password.WriteByte(set[n.Int64()])
	}
	// Fill remaining length
	remaining := length - len(strongPasswordChars)
	for i := 0; i < remaining; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(allChars))))
		if err != nil {
			return "", err
		}
		password.WriteByte(allChars[n.Int64()])
	}

	// Shuffle using Fisher-Yates
	result := []byte(password.String())
	for i := len(result) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		result[i], result[j.Int64()] = result[j.Int64()], result[i]
	}
	return string(result), nil
}

// CreateWebUser creates a new web user with auto-generated strong password.
// Returns (username, plaintextPassword, error).
func CreateWebUser(db *sql.DB, username string) (string, string, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 64 {
		return "", "", fmt.Errorf("invalid username (must be 1-64 chars)")
	}

	// Check uniqueness
	var existingID int
	if err := db.QueryRow("SELECT id FROM web_users WHERE username = ?", username).Scan(&existingID); err == nil {
		return "", "", fmt.Errorf("username %q already exists", username)
	}

	// Generate strong password
	password, err := GenerateStrongPassword(16)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate password: %w", err)
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", "", fmt.Errorf("failed to hash password: %w", err)
	}

	// Insert user
	result, err := db.Exec(
		"INSERT INTO web_users (username, password) VALUES (?, ?)",
		username, string(hash),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return "", "", fmt.Errorf("username %q already exists", username)
		}
		return "", "", fmt.Errorf("failed to create user: %w", err)
	}

	id, _ := result.LastInsertId()
	log.WithFields(log.Fields{
		"user_id":  id,
		"username": username,
	}).Info("Web user created by admin")

	return username, password, nil
}

// ---------------------------------------------------------------------------
// Auth handlers
// ---------------------------------------------------------------------------

type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	UserID  int    `json:"user_id,omitempty"`
}

type feishuLinkRequest struct {
	FeishuUserID string `json:"feishu_user_id"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

type feishuLoginRequest struct {
	FeishuUserID string `json:"feishu_user_id"`
	Password     string `json:"password"`
}

type feishuLinkResponse struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message,omitempty"`
	Username string `json:"username,omitempty"`
}

// handleRegister handles POST /api/auth/register
func (wc *WebChannel) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Invite-only mode: reject self-registration
	if wc.config.InviteOnly {
		writeJSON(w, http.StatusForbidden, authResponse{OK: false, Message: "registration is invite-only, please contact admin"})
		return
	}

	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, authResponse{OK: false, Message: "invalid request body"})
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	req.Password = strings.TrimSpace(req.Password)

	if req.Username == "" || len(req.Username) > 64 || req.Password == "" || len(req.Password) > 128 {
		writeJSON(w, http.StatusBadRequest, authResponse{OK: false, Message: "invalid username or password"})
		return
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, authResponse{OK: false, Message: "internal error"})
		return
	}

	// Insert user
	result, err := wc.db.Exec(
		"INSERT INTO web_users (username, password) VALUES (?, ?)",
		req.Username, string(hash),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			writeJSON(w, http.StatusConflict, authResponse{OK: false, Message: "username already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, authResponse{OK: false, Message: "internal error"})
		return
	}

	id, _ := result.LastInsertId()
	writeJSON(w, http.StatusCreated, authResponse{OK: true, UserID: int(id)})
}

// handleLogin handles POST /api/auth/login
func (wc *WebChannel) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, authResponse{OK: false, Message: "invalid request body"})
		return
	}

	// Look up user
	var id int
	var hash string
	err := wc.db.QueryRow(
		"SELECT id, password FROM web_users WHERE username = ?",
		strings.TrimSpace(req.Username),
	).Scan(&id, &hash)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{OK: false, Message: "invalid credentials"})
		return
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{OK: false, Message: "invalid credentials"})
		return
	}

	// Auto-detect Feishu identity: look up linked feishu user ID
	feishuUID := FeishuGetLinkedUserID(wc.db, id)
	log.WithFields(log.Fields{
		"username":    req.Username,
		"user_id":     id,
		"feishu_user": feishuUID,
	}).Info("Password login — feishu link check")

	// Create session
	token := strings.ReplaceAll(uuid.New().String(), "-", "")
	wc.sessionsMu.Lock()
	wc.sessions[token] = sessionInfo{
		userID:       id,
		username:     strings.TrimSpace(req.Username),
		feishuUserID: feishuUID,
		expires:      time.Now().Add(webSessionMaxAge),
	}
	wc.sessionsMu.Unlock()

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     webSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(webSessionMaxAge.Seconds()),
	})

	writeJSON(w, http.StatusOK, authResponse{OK: true, UserID: id})
}

// handleLogout handles POST /api/auth/logout
func (wc *WebChannel) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:     webSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	// Remove session
	if cookie, err := r.Cookie(webSessionCookieName); err == nil {
		wc.sessionsMu.Lock()
		delete(wc.sessions, cookie.Value)
		wc.sessionsMu.Unlock()
	}

	writeJSON(w, http.StatusOK, authResponse{OK: true})
}

// validateSession checks the session cookie and returns session info
func (wc *WebChannel) validateSession(r *http.Request) *sessionInfo {
	cookie, err := r.Cookie(webSessionCookieName)
	if err != nil {
		return nil
	}

	wc.sessionsMu.RLock()
	si, ok := wc.sessions[cookie.Value]
	wc.sessionsMu.RUnlock()

	if !ok || time.Now().After(si.expires) {
		return nil
	}

	return &si
}

// authMiddleware wraps a handler with session validation
func (wc *WebChannel) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si := wc.validateSession(r)
		if si == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Store senderID in context for handler use (normalize for single-user mode)
		senderID := "web-" + strconv.Itoa(si.userID)
		if wc.callbacks.NormalizeSenderID != nil {
			senderID = wc.callbacks.NormalizeSenderID(senderID)
		}
		// If linked to Feishu account, use Feishu identity for all operations.
		// This ensures web users share session/persona/workspace/skills/agents with their Feishu account.
		if si.feishuUserID != "" {
			senderID = si.feishuUserID
		}
		ctx := contextWithSenderID(r.Context(), senderID)
		next(w, r.WithContext(ctx))
	}
}

// ---------------------------------------------------------------------------
// Feishu-Web account linking
// ---------------------------------------------------------------------------

// FeishuLinkUser creates or retrieves a web account linked to a Feishu user.
// If the feishu user is already linked, returns the existing web username.
// Otherwise creates a new web user with bcrypt-hashed password and stores the link.
func FeishuLinkUser(db *sql.DB, feishuUserID, username, password string) (string, error) {
	// Check if already linked
	var webUserIDStr string
	err := db.QueryRow(
		`SELECT value FROM user_settings WHERE channel = 'feishu' AND sender_id = ? AND key = 'web_user_id'`,
		feishuUserID,
	).Scan(&webUserIDStr)
	if err == nil {
		// Already linked — return existing username
		var existingName string
		if err := db.QueryRow("SELECT username FROM web_users WHERE id = ?", webUserIDStr).Scan(&existingName); err != nil {
			return "", fmt.Errorf("linked web user not found")
		}
		return existingName, nil
	}

	// Not linked — validate input
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || len(username) > 64 || password == "" || len(password) > 128 {
		return "", fmt.Errorf("invalid username or password")
	}

	// Check username uniqueness
	var existingID int
	if err := db.QueryRow("SELECT id FROM web_users WHERE username = ?", username).Scan(&existingID); err == nil {
		return "", fmt.Errorf("username already exists")
	}

	// Hash password with bcrypt
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("internal error")
	}

	// Create web user
	result, err := db.Exec(
		"INSERT INTO web_users (username, password) VALUES (?, ?)",
		username, string(hash),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return "", fmt.Errorf("username already exists")
		}
		return "", fmt.Errorf("failed to create user")
	}

	id, _ := result.LastInsertId()

	// Store the feishu→web link in user_settings
	now := time.Now().Unix()
	_, _ = db.Exec(
		`INSERT OR REPLACE INTO user_settings (channel, sender_id, key, value, updated_at) VALUES ('feishu', ?, 'web_user_id', ?, ?)`,
		feishuUserID, strconv.FormatInt(id, 10), now,
	)

	return username, nil
}

// FeishuGetLinkedUserID returns the Feishu user ID linked to a web user ID.
// Returns empty string if no link exists.
func FeishuGetLinkedUserID(db *sql.DB, webUserID int) string {
	var feishuUID string
	err := db.QueryRow(
		`SELECT sender_id FROM user_settings WHERE channel = 'feishu' AND key = 'web_user_id' AND value = ?`,
		strconv.Itoa(webUserID),
	).Scan(&feishuUID)
	if err != nil {
		return ""
	}
	return feishuUID
}

// FeishuGetLinkedUser returns the linked web username for a Feishu user.
func FeishuGetLinkedUser(db *sql.DB, feishuUserID string) (string, bool) {
	var webUserIDStr string
	err := db.QueryRow(
		`SELECT value FROM user_settings WHERE channel = 'feishu' AND sender_id = ? AND key = 'web_user_id'`,
		feishuUserID,
	).Scan(&webUserIDStr)
	if err != nil {
		return "", false
	}

	var username string
	if err := db.QueryRow("SELECT username FROM web_users WHERE id = ?", webUserIDStr).Scan(&username); err != nil {
		return "", false
	}
	return username, true
}

// FeishuUnlinkUser removes the Feishu-Web account link.
func FeishuUnlinkUser(db *sql.DB, feishuUserID string) error {
	_, err := db.Exec(
		`DELETE FROM user_settings WHERE channel = 'feishu' AND sender_id = ? AND key = 'web_user_id'`,
		feishuUserID,
	)
	return err
}

// handleFeishuLink handles POST /api/auth/feishu-link
// Requires admin token (Authorization: Bearer <secret>).
func (wc *WebChannel) handleFeishuLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify admin token
	if wc.config.FeishuLinkSecret != "" {
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + wc.config.FeishuLinkSecret
		if auth != expected {
			writeJSON(w, http.StatusUnauthorized, authResponse{OK: false, Message: "unauthorized"})
			return
		}
	} else {
		writeJSON(w, http.StatusForbidden, authResponse{OK: false, Message: "feishu link not configured"})
		return
	}

	var req feishuLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, authResponse{OK: false, Message: "invalid request body"})
		return
	}

	username, err := FeishuLinkUser(wc.db, req.FeishuUserID, req.Username, req.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, feishuLinkResponse{OK: false, Message: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, feishuLinkResponse{OK: true, Username: username})
}

// handleFeishuLogin handles POST /api/auth/feishu-login
// Allows a Feishu user to log in to the web using their linked credentials.
func (wc *WebChannel) handleFeishuLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req feishuLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, authResponse{OK: false, Message: "invalid request body"})
		return
	}

	// Look up linked web user
	username, ok := FeishuGetLinkedUser(wc.db, req.FeishuUserID)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, authResponse{OK: false, Message: "no linked web account"})
		return
	}

	// Verify password
	var id int
	var hash string
	err := wc.db.QueryRow("SELECT id, password FROM web_users WHERE username = ?", username).Scan(&id, &hash)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{OK: false, Message: "invalid credentials"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{OK: false, Message: "invalid credentials"})
		return
	}

	// Create session
	token := strings.ReplaceAll(uuid.New().String(), "-", "")
	wc.sessionsMu.Lock()
	wc.sessions[token] = sessionInfo{
		userID:       id,
		username:     username,
		feishuUserID: req.FeishuUserID,
		expires:      time.Now().Add(webSessionMaxAge),
	}
	wc.sessionsMu.Unlock()

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     webSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(webSessionMaxAge.Seconds()),
	})

	writeJSON(w, http.StatusOK, authResponse{OK: true, UserID: id})
}

// ---------------------------------------------------------------------------
// Context helpers
// ---------------------------------------------------------------------------

type contextKey string

const senderIDKey contextKey = "sender_id"

func contextWithSenderID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, senderIDKey, id)
}

func senderIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(senderIDKey).(string); ok {
		return id
	}
	return ""
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// handleAuthConfig handles GET /api/auth/config
// Returns public auth configuration (e.g., invite-only mode).
func (wc *WebChannel) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"invite_only": wc.config.InviteOnly,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
