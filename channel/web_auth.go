package channel

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

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

// handleRegister handles POST /api/auth/register
func (wc *WebChannel) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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

	// Create session
	token := strings.ReplaceAll(uuid.New().String(), "-", "")
	wc.sessionsMu.Lock()
	wc.sessions[token] = sessionInfo{
		userID:   id,
		username: strings.TrimSpace(req.Username),
		expires:  time.Now().Add(webSessionMaxAge),
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
		// Store senderID in context for handler use
		ctx := contextWithSenderID(r.Context(), "web:"+strconv.Itoa(si.userID))
		next(w, r.WithContext(ctx))
	}
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

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
