package event

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
)

// WebhookConfig holds configuration for the webhook server.
type WebhookConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	BaseURL     string `json:"base_url"`
	MaxBodySize int64  `json:"max_body_size"`
	RateLimit   int    `json:"rate_limit"` // max requests per minute per trigger
}

// WebhookServer is a lightweight HTTP server that receives external webhook events.
type WebhookServer struct {
	router  *Router
	server  *http.Server
	config  WebhookConfig
	limiter *rateLimiter
}

// NewWebhookServer creates a new WebhookServer.
func NewWebhookServer(router *Router, cfg WebhookConfig) *WebhookServer {
	if cfg.MaxBodySize == 0 {
		cfg.MaxBodySize = 1 << 20 // 1 MB
	}
	if cfg.RateLimit == 0 {
		cfg.RateLimit = 60
	}
	return &WebhookServer{
		router:  router,
		config:  cfg,
		limiter: newRateLimiter(cfg.RateLimit),
	}
}

// Start starts the webhook HTTP server. Blocks until the server shuts down.
func (ws *WebhookServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/hooks/", ws.handleHook)

	addr := fmt.Sprintf("%s:%d", ws.config.Host, ws.config.Port)
	ws.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	log.WithField("addr", addr).Info("Webhook server starting")
	if err := ws.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("webhook server: %w", err)
	}
	return nil
}

// Stop gracefully shuts down the webhook server.
func (ws *WebhookServer) Stop() {
	if ws.server != nil {
		ws.server.Close()
	}
}

// BaseURL returns the configured base URL for generating webhook URLs.
func (ws *WebhookServer) BaseURL() string {
	return ws.config.BaseURL
}

// handleHook routes:
//
//	POST /hooks/{triggerID}      — receive a webhook
//	GET  /hooks/{triggerID}/ping — connectivity test
func (ws *WebhookServer) handleHook(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/hooks/")
	path = strings.TrimSuffix(path, "/")

	parts := strings.SplitN(path, "/", 2)
	triggerID := parts[0]

	if triggerID == "" {
		http.Error(w, `{"error":"trigger_id required"}`, http.StatusBadRequest)
		return
	}

	// GET /hooks/{id}/ping
	if r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "ping" {
		ws.handlePing(w, triggerID)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	if !ws.limiter.allow(triggerID) {
		http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, ws.config.MaxBodySize+1))
	if err != nil {
		http.Error(w, `{"error":"failed to read body"}`, http.StatusBadRequest)
		return
	}
	if int64(len(body)) > ws.config.MaxBodySize {
		http.Error(w, `{"error":"request body too large"}`, http.StatusRequestEntityTooLarge)
		return
	}

	// Parse JSON payload
	var payload map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			// Non-JSON body: wrap in a "raw" field
			payload = map[string]any{"raw": string(body)}
		}
	}

	// Collect normalized headers
	headers := make(map[string]string)
	for k, v := range r.Header {
		if len(v) > 0 {
			headers[strings.ToLower(k)] = v[0]
		}
	}

	evt := Event{
		Type:      "webhook",
		Source:    triggerID,
		Payload:   payload,
		Headers:   headers,
		RawBody:   body,
		Timestamp: time.Now(),
	}

	result, err := ws.router.DispatchByID(triggerID, evt)
	if err != nil {
		log.WithError(err).WithField("trigger_id", triggerID).Warn("Webhook dispatch failed")
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusNotFound)
		return
	}

	if !result.OK {
		log.WithField("trigger_id", triggerID).WithField("reason", result.Error).Warn("Webhook rejected")
		status := http.StatusForbidden
		if result.Error == "trigger disabled" {
			status = http.StatusConflict
		}
		http.Error(w, fmt.Sprintf(`{"error":%q}`, result.Error), status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"ok":true,"trigger_id":%q}`, triggerID)
}

func (ws *WebhookServer) handlePing(w http.ResponseWriter, triggerID string) {
	t, err := ws.router.GetTrigger(triggerID)
	if err != nil || t == nil {
		http.Error(w, `{"error":"trigger not found"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"trigger_id":%q,"name":%q,"enabled":%t}`, t.ID, t.Name, t.Enabled)
}

// rateLimiter implements a simple per-key sliding window rate limiter.
type rateLimiter struct {
	maxPerMin int
	mu        sync.Mutex
	windows   map[string]*slidingWindow
}

type slidingWindow struct {
	timestamps []time.Time
}

func newRateLimiter(maxPerMin int) *rateLimiter {
	return &rateLimiter{
		maxPerMin: maxPerMin,
		windows:   make(map[string]*slidingWindow),
	}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-time.Minute)

	w, ok := rl.windows[key]
	if !ok {
		w = &slidingWindow{}
		rl.windows[key] = w
	}

	// Prune old entries
	i := 0
	for i < len(w.timestamps) && w.timestamps[i].Before(cutoff) {
		i++
	}
	w.timestamps = w.timestamps[i:]

	if len(w.timestamps) >= rl.maxPerMin {
		return false
	}

	w.timestamps = append(w.timestamps, now)
	return true
}
