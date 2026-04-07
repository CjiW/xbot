package event

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestWebhookServer(store *memTriggerStore) (*WebhookServer, *Router) {
	router := NewRouter(store)
	ws := NewWebhookServer(router, WebhookConfig{
		Host:        "localhost",
		Port:        0,
		BaseURL:     "http://localhost:8090",
		MaxBodySize: 1024,
		RateLimit:   100,
	})
	return ws, router
}

func TestWebhookServer_PostHook(t *testing.T) {
	store := newMemTriggerStore()
	ws, router := newTestWebhookServer(store)

	var injectedMsg string
	router.SetInjectFunc(func(msg Message) {
		injectedMsg = msg.Content
	})

	store.AddTrigger(&Trigger{
		ID:         "trg_wh1",
		EventType:  "webhook",
		Channel:    "feishu",
		ChatID:     "chat1",
		SenderID:   "user1",
		MessageTpl: "Received: {{.EventType}}",
		Enabled:    true,
	})

	req := httptest.NewRequest(http.MethodPost, "/hooks/trg_wh1", strings.NewReader(`{"action":"opened"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	ws.handleHook(rec, req)

	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	if injectedMsg != "Received: webhook" {
		t.Errorf("unexpected injected message: %q", injectedMsg)
	}
}

func TestWebhookServer_NotFound(t *testing.T) {
	store := newMemTriggerStore()
	ws, router := newTestWebhookServer(store)
	router.SetInjectFunc(func(msg Message) {})

	req := httptest.NewRequest(http.MethodPost, "/hooks/nonexistent", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	ws.handleHook(rec, req)

	if rec.Result().StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Result().StatusCode)
	}
}

func TestWebhookServer_Disabled(t *testing.T) {
	store := newMemTriggerStore()
	ws, router := newTestWebhookServer(store)
	router.SetInjectFunc(func(msg Message) {})

	store.AddTrigger(&Trigger{
		ID:        "trg_dis",
		EventType: "webhook",
		Enabled:   false,
	})

	req := httptest.NewRequest(http.MethodPost, "/hooks/trg_dis", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	ws.handleHook(rec, req)

	if rec.Result().StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", rec.Result().StatusCode)
	}
}

func TestWebhookServer_Ping(t *testing.T) {
	store := newMemTriggerStore()
	ws, _ := newTestWebhookServer(store)

	store.AddTrigger(&Trigger{
		ID:        "trg_ping",
		Name:      "Test Ping",
		EventType: "webhook",
		Enabled:   true,
	})

	req := httptest.NewRequest(http.MethodGet, "/hooks/trg_ping/ping", nil)
	rec := httptest.NewRecorder()

	ws.handleHook(rec, req)

	if rec.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Result().StatusCode)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "trg_ping") {
		t.Errorf("ping response should contain trigger ID: %s", body)
	}
}

func TestWebhookServer_MethodNotAllowed(t *testing.T) {
	store := newMemTriggerStore()
	ws, _ := newTestWebhookServer(store)

	req := httptest.NewRequest(http.MethodPut, "/hooks/trg_test", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	ws.handleHook(rec, req)

	if rec.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Result().StatusCode)
	}
}

func TestWebhookServer_TooLargeBody(t *testing.T) {
	store := newMemTriggerStore()
	ws, router := newTestWebhookServer(store)
	router.SetInjectFunc(func(msg Message) {})

	store.AddTrigger(&Trigger{
		ID:        "trg_big",
		EventType: "webhook",
		Enabled:   true,
		Channel:   "f",
		ChatID:    "c",
		SenderID:  "u",
	})

	largeBody := strings.Repeat("x", 2048) // exceeds 1024 max
	req := httptest.NewRequest(http.MethodPost, "/hooks/trg_big", strings.NewReader(largeBody))
	rec := httptest.NewRecorder()

	ws.handleHook(rec, req)

	if rec.Result().StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", rec.Result().StatusCode)
	}
}

func TestWebhookServer_SignatureVerification(t *testing.T) {
	store := newMemTriggerStore()
	ws, router := newTestWebhookServer(store)

	var injected bool
	router.SetInjectFunc(func(msg Message) { injected = true })

	secret := "test-secret"
	store.AddTrigger(&Trigger{
		ID:         "trg_sig",
		EventType:  "webhook",
		Channel:    "f",
		ChatID:     "c",
		SenderID:   "u",
		MessageTpl: "sig test",
		Secret:     secret,
		Enabled:    true,
	})

	body := `{"data":"test"}`
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// Valid signature
	req := httptest.NewRequest(http.MethodPost, "/hooks/trg_sig", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	rec := httptest.NewRecorder()

	ws.handleHook(rec, req)

	if rec.Result().StatusCode != http.StatusOK {
		b, _ := io.ReadAll(rec.Result().Body)
		t.Fatalf("expected 200 with valid sig, got %d: %s", rec.Result().StatusCode, string(b))
	}
	if !injected {
		t.Error("expected message to be injected with valid signature")
	}

	// Invalid signature
	injected = false
	req2 := httptest.NewRequest(http.MethodPost, "/hooks/trg_sig", strings.NewReader(body))
	req2.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	rec2 := httptest.NewRecorder()

	ws.handleHook(rec2, req2)

	if rec2.Result().StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 with invalid sig, got %d", rec2.Result().StatusCode)
	}
	if injected {
		t.Error("should not inject with invalid signature")
	}
}

func TestWebhookServer_NonJSONBody(t *testing.T) {
	store := newMemTriggerStore()
	ws, router := newTestWebhookServer(store)

	var injectedMsg string
	router.SetInjectFunc(func(msg Message) { injectedMsg = msg.Content })

	store.AddTrigger(&Trigger{
		ID:         "trg_raw",
		EventType:  "webhook",
		Channel:    "f",
		ChatID:     "c",
		SenderID:   "u",
		MessageTpl: "raw: {{.Payload.raw}}",
		Enabled:    true,
	})

	req := httptest.NewRequest(http.MethodPost, "/hooks/trg_raw", strings.NewReader("plain text body"))
	rec := httptest.NewRecorder()

	ws.handleHook(rec, req)

	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Result().StatusCode)
	}
	if injectedMsg != "raw: plain text body" {
		t.Errorf("unexpected injected message: %q", injectedMsg)
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(3)

	for i := 0; i < 3; i++ {
		if !rl.allow("key1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	if rl.allow("key1") {
		t.Error("4th request should be rate limited")
	}

	// Different key should be allowed
	if !rl.allow("key2") {
		t.Error("different key should be allowed")
	}
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	rl := newRateLimiter(1)

	if !rl.allow("k") {
		t.Fatal("first request should pass")
	}
	if rl.allow("k") {
		t.Fatal("second request should be limited")
	}

	// Manually expire the window
	rl.mu.Lock()
	rl.windows["k"].timestamps[0] = time.Now().Add(-2 * time.Minute)
	rl.mu.Unlock()

	if !rl.allow("k") {
		t.Error("request should pass after window expired")
	}
}
