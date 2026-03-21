package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sync"
	"time"

	log "xbot/logger"
)

// Server is a lightweight HTTP server for OAuth callbacks.
type Server struct {
	mu       sync.Mutex
	config   Config
	server   *http.Server
	mgr      *Manager
	SendFunc func(channel, chatID, content string) error // Function to send messages back to chat
}

// Config contains the OAuth server configuration.
type Config struct {
	Enable  bool   // Whether to enable the OAuth server
	Host    string // Host to listen on (default 127.0.0.1, safer than 0.0.0.0)
	Port    int    // Port to listen on (default 8081, can reuse pprof port)
	BaseURL string // Public base URL for callbacks (e.g., https://your-domain.com)
}

// NewServer creates a new OAuth server.
func NewServer(cfg Config, mgr *Manager) *Server {
	if cfg.Port == 0 {
		cfg.Port = 8081
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1" // 默认绑定 localhost，避免暴露到所有网络接口
	}
	return &Server{
		config: cfg,
		mgr:    mgr,
	}
}

// Start starts the OAuth HTTP server if enabled.
func (s *Server) Start() error {
	if !s.config.Enable {
		log.Info("OAuth server disabled")
		return nil
	}

	if s.config.BaseURL == "" {
		return fmt.Errorf("OAUTH_BASE_URL is required when OAuth is enabled")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", s.handleCallback)
	mux.HandleFunc("/oauth/health", s.handleHealth)

	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.config.Host, s.config.Port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Error("OAuth server error")
		}
	}()

	log.WithFields(log.Fields{
		"host":    s.config.Host,
		"port":    s.config.Port,
		"baseURL": s.config.BaseURL,
	}).Info("OAuth server started")

	return nil
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.server == nil {
		return nil
	}
	log.Info("Shutting down OAuth server")
	return s.server.Shutdown(ctx)
}

// handleCallback handles OAuth provider callbacks.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Log full URL for debugging
	log.WithField("url", r.URL.String()).Info("OAuth callback received")

	query := r.URL.Query()
	state := query.Get("state")
	code := query.Get("code")
	errorMsg := query.Get("error")

	log.WithFields(log.Fields{
		"state": state,
		"code":  code,
		"error": errorMsg,
	}).Info("OAuth callback params")

	if errorMsg != "" {
		s.renderError(w, "Authorization denied", errorMsg)
		s.mgr.DeleteFlow(state)
		return
	}

	if state == "" {
		s.renderError(w, "Invalid callback", "Missing state parameter")
		return
	}

	if code == "" {
		s.renderError(w, "Invalid callback", "Missing code parameter - authorization might have been denied or failed")
		return
	}

	// Get provider from the flow (stored when flow was started)
	flow, ok := s.mgr.GetFlow(state)
	if !ok {
		s.renderError(w, "Invalid callback", "Invalid or expired state token")
		return
	}
	provider := flow.Provider

	token, err := s.mgr.CompleteFlow(r.Context(), state, code)
	if err != nil {
		log.WithError(err).Error("OAuth flow failed")
		s.renderError(w, "Authorization failed", err.Error())
		return
	}

	log.WithFields(log.Fields{
		"provider": provider,
		"channel":  flow.Channel,
		"chat_id":  flow.ChatID,
		"expires":  token.ExpiresAt,
	}).Info("OAuth authorization successful")
	s.renderSuccess(w, provider)

	// Send success message back to the chat
	if s.SendFunc != nil {
		successMsg := "✅ 授权成功！现在可以继续之前的操作了。"
		if err := s.SendFunc(flow.Channel, flow.ChatID, successMsg); err != nil {
			log.WithError(err).Error("Failed to send OAuth success message")
		}
	}
}

// handleHealth returns health status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
	})
}

func (s *Server) renderSuccess(w http.ResponseWriter, provider string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>授权成功</title>
<style>body{font-family:sans-serif;max-width:500px;margin:50px auto;padding:20px;text-align:center}
.success{color:#10b981;font-size:48px;margin-bottom:20px}
.card{background:#f9fafb;border-radius:8px;padding:30px}
</style></head><body>
<div class="card">
<div class="success">✓</div>
<h2>授权成功</h2>
<p>您已成功授权 `+html.EscapeString(provider)+`。</p>
<p>现在可以关闭此窗口并返回对话。</p>
</div></body></html>`)
}

func (s *Server) renderError(w http.ResponseWriter, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>授权失败</title>
<style>body{font-family:sans-serif;max-width:500px;margin:50px auto;padding:20px;text-align:center}
.error{color:#ef4444;font-size:48px;margin-bottom:20px}
.card{background:#fef2f2;border-radius:8px;padding:30px}
</style></head><body>
<div class="card">
<div class="error">✕</div>
<h2>`+html.EscapeString(title)+`</h2>
<p>`+html.EscapeString(detail)+`</p>
</div></body></html>`)
}
