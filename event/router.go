package event

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
)

// Message represents a message to be injected into the agent loop.
type Message struct {
	Channel      string
	ChatID       string
	SenderID     string
	Content      string
	EventSource  string // event origin: "webhook", "cron", etc.
	EventTrigger string // trigger ID that produced this message
}

// InjectFunc injects a message into the agent loop.
type InjectFunc func(msg Message)

// TriggerStore abstracts persistence for triggers.
type TriggerStore interface {
	AddTrigger(t *Trigger) error
	RemoveTrigger(id string) error
	GetTrigger(id string) (*Trigger, error)
	ListByEventType(eventType string) ([]*Trigger, error)
	ListBySender(senderID string) ([]*Trigger, error)
	UpdateEnabled(id string, enabled bool) error
	RecordFire(id string, at time.Time) error
}

// DispatchResult records the outcome of dispatching an event to one trigger.
type DispatchResult struct {
	TriggerID string
	OK        bool
	Error     string
}

// Router matches incoming events to registered triggers and injects messages.
type Router struct {
	store    TriggerStore
	injectFn InjectFunc
	mu       sync.RWMutex
}

// NewRouter creates a new Router.
func NewRouter(store TriggerStore) *Router {
	return &Router{store: store}
}

// SetInjectFunc sets the message injection function (typically agent.injectEventMessage).
func (r *Router) SetInjectFunc(fn InjectFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.injectFn = fn
}

// dispatchOne is the shared core logic for dispatching an event to a single trigger.
// It verifies the signature, renders the message template, injects, records fire, and handles one-shot.
func (r *Router) dispatchOne(t *Trigger, evt Event) DispatchResult {
	if t.Secret != "" && !verifySignature(evt, t.Secret) {
		return DispatchResult{
			TriggerID: t.ID,
			OK:        false,
			Error:     "signature verification failed",
		}
	}

	message := RenderMessage(t.MessageTpl, evt)
	r.injectFn(Message{
		Channel:      t.Channel,
		ChatID:       t.ChatID,
		SenderID:     t.SenderID,
		Content:      message,
		EventSource:  evt.Type,
		EventTrigger: t.ID,
	})

	now := time.Now()
	if err := r.store.RecordFire(t.ID, now); err != nil {
		log.WithError(err).WithField("trigger_id", t.ID).Warn("EventRouter: failed to record fire")
	}

	if t.OneShot {
		if err := r.store.UpdateEnabled(t.ID, false); err != nil {
			log.WithError(err).WithField("trigger_id", t.ID).Warn("EventRouter: failed to disable one-shot trigger")
		}
	}

	log.WithFields(log.Fields{
		"trigger_id": t.ID,
		"channel":    t.Channel,
		"chat_id":    t.ChatID,
	}).Info("EventRouter: dispatched event to trigger")

	return DispatchResult{TriggerID: t.ID, OK: true}
}

// Dispatch matches an event against registered triggers and injects messages.
func (r *Router) Dispatch(evt Event) []DispatchResult {
	r.mu.RLock()
	injectFn := r.injectFn
	r.mu.RUnlock()

	if injectFn == nil {
		log.Warn("EventRouter: injectFunc not set, dropping event")
		return nil
	}

	triggers, err := r.store.ListByEventType(evt.Type)
	if err != nil {
		log.WithError(err).Error("EventRouter: failed to list triggers")
		return nil
	}

	var results []DispatchResult

	for _, t := range triggers {
		if !t.Enabled {
			continue
		}

		result := r.dispatchOne(t, evt)
		if !result.OK {
			log.WithField("trigger_id", t.ID).Warn("EventRouter: signature mismatch")
		}
		results = append(results, result)
	}

	return results
}

// DispatchByID dispatches an event to a specific trigger by ID.
// Used by the webhook server where the trigger ID is in the URL path.
func (r *Router) DispatchByID(triggerID string, evt Event) (*DispatchResult, error) {
	r.mu.RLock()
	injectFn := r.injectFn
	r.mu.RUnlock()

	if injectFn == nil {
		return nil, fmt.Errorf("inject function not set")
	}

	t, err := r.store.GetTrigger(triggerID)
	if err != nil {
		return nil, fmt.Errorf("get trigger: %w", err)
	}
	if t == nil {
		return nil, fmt.Errorf("trigger not found: %s", triggerID)
	}
	if !t.Enabled {
		return &DispatchResult{TriggerID: t.ID, OK: false, Error: "trigger disabled"}, nil
	}

	result := r.dispatchOne(t, evt)
	return &result, nil
}

// RegisterTrigger creates a new trigger.
func (r *Router) RegisterTrigger(t *Trigger) error {
	return r.store.AddTrigger(t)
}

// RemoveTrigger deletes a trigger.
func (r *Router) RemoveTrigger(id string) error {
	return r.store.RemoveTrigger(id)
}

// GetTrigger retrieves a trigger by ID.
func (r *Router) GetTrigger(id string) (*Trigger, error) {
	return r.store.GetTrigger(id)
}

// ListTriggers lists triggers for a given sender.
func (r *Router) ListTriggers(senderID string) ([]*Trigger, error) {
	return r.store.ListBySender(senderID)
}

// EnableTrigger enables a trigger.
func (r *Router) EnableTrigger(id string) error {
	return r.store.UpdateEnabled(id, true)
}

// DisableTrigger disables a trigger.
func (r *Router) DisableTrigger(id string) error {
	return r.store.UpdateEnabled(id, false)
}

// verifySignature checks HMAC-SHA256 signature from common webhook headers.
// Priority: X-Hub-Signature-256 (GitHub) > X-Gitlab-Token (plain) > X-Webhook-Signature (generic).
func verifySignature(evt Event, secret string) bool {
	if len(evt.RawBody) == 0 {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(evt.RawBody)
	expected := hex.EncodeToString(mac.Sum(nil))

	// GitHub: X-Hub-Signature-256: sha256=<hex>
	if sig := evt.Headers["x-hub-signature-256"]; sig != "" {
		sig = strings.TrimPrefix(sig, "sha256=")
		return hmac.Equal([]byte(expected), []byte(sig))
	}

	// GitLab: X-Gitlab-Token (plain secret comparison, not HMAC)
	if token := evt.Headers["x-gitlab-token"]; token != "" {
		return hmac.Equal([]byte(secret), []byte(token))
	}

	// Generic: X-Webhook-Signature header
	if sig := evt.Headers["x-webhook-signature"]; sig != "" {
		sig = strings.TrimPrefix(sig, "sha256=")
		return hmac.Equal([]byte(expected), []byte(sig))
	}

	return false
}
