package event

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"
)

// memTriggerStore is an in-memory TriggerStore for testing.
type memTriggerStore struct {
	mu       sync.RWMutex
	triggers map[string]*Trigger
}

func newMemTriggerStore() *memTriggerStore {
	return &memTriggerStore{triggers: make(map[string]*Trigger)}
}

func (s *memTriggerStore) AddTrigger(t *Trigger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.triggers[t.ID] = t
	return nil
}

func (s *memTriggerStore) RemoveTrigger(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.triggers, id)
	return nil
}

func (s *memTriggerStore) GetTrigger(id string) (*Trigger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.triggers[id]
	if !ok {
		return nil, nil
	}
	cp := *t
	return &cp, nil
}

func (s *memTriggerStore) ListByEventType(eventType string) ([]*Trigger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Trigger
	for _, t := range s.triggers {
		if t.EventType == eventType {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *memTriggerStore) ListBySender(senderID string) ([]*Trigger, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Trigger
	for _, t := range s.triggers {
		if t.SenderID == senderID {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *memTriggerStore) UpdateEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.triggers[id]; ok {
		t.Enabled = enabled
	}
	return nil
}

func (s *memTriggerStore) RecordFire(id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.triggers[id]; ok {
		t.LastFired = &at
		t.FireCount++
	}
	return nil
}

func TestRouter_DispatchByID(t *testing.T) {
	store := newMemTriggerStore()
	router := NewRouter(store)

	var injected []string
	router.SetInjectFunc(func(msg Message) {
			injected = append(injected, msg.Content)
		})

	trigger := &Trigger{
		ID:         "trg_test1",
		EventType:  "webhook",
		Channel:    "feishu",
		ChatID:     "chat123",
		SenderID:   "user1",
		MessageTpl: "Got event: {{.EventType}}",
		Enabled:    true,
	}
	store.AddTrigger(trigger)

	evt := Event{
		Type:      "webhook",
		Source:    "trg_test1",
		Payload:   map[string]any{"action": "test"},
		Timestamp: time.Now(),
	}

	result, err := router.DispatchByID("trg_test1", evt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.OK {
		t.Fatalf("dispatch failed: %s", result.Error)
	}
	if len(injected) != 1 {
		t.Fatalf("expected 1 injection, got %d", len(injected))
	}
	if injected[0] != "Got event: webhook" {
		t.Errorf("unexpected injected message: %q", injected[0])
	}

	// Verify fire was recorded
	updated, _ := store.GetTrigger("trg_test1")
	if updated.FireCount != 1 {
		t.Errorf("expected FireCount=1, got %d", updated.FireCount)
	}
	if updated.LastFired == nil {
		t.Error("expected LastFired to be set")
	}
}

func TestRouter_DispatchByID_NotFound(t *testing.T) {
	store := newMemTriggerStore()
	router := NewRouter(store)
	router.SetInjectFunc(func(msg Message) {})

	_, err := router.DispatchByID("nonexistent", Event{})
	if err == nil {
		t.Fatal("expected error for nonexistent trigger")
	}
}

func TestRouter_DispatchByID_Disabled(t *testing.T) {
	store := newMemTriggerStore()
	router := NewRouter(store)
	router.SetInjectFunc(func(msg Message) {})

	store.AddTrigger(&Trigger{
		ID:        "trg_dis",
		EventType: "webhook",
		Enabled:   false,
	})

	result, err := router.DispatchByID("trg_dis", Event{Type: "webhook"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.OK {
		t.Fatal("expected dispatch to fail for disabled trigger")
	}
}

func TestRouter_DispatchByID_OneShot(t *testing.T) {
	store := newMemTriggerStore()
	router := NewRouter(store)
	router.SetInjectFunc(func(msg Message) {})

	store.AddTrigger(&Trigger{
		ID:         "trg_one",
		EventType:  "webhook",
		Channel:    "feishu",
		ChatID:     "c1",
		SenderID:   "u1",
		MessageTpl: "one-shot",
		Enabled:    true,
		OneShot:    true,
	})

	_, err := router.DispatchByID("trg_one", Event{Type: "webhook", Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	// Trigger should be disabled after one-shot
	updated, _ := store.GetTrigger("trg_one")
	if updated.Enabled {
		t.Error("expected one-shot trigger to be disabled after fire")
	}
}

func TestRouter_Dispatch_MultipleMatches(t *testing.T) {
	store := newMemTriggerStore()
	router := NewRouter(store)

	var mu sync.Mutex
	var injected []string
	router.SetInjectFunc(func(msg Message) {
		mu.Lock()
		injected = append(injected, msg.ChatID+":"+msg.Content)
		mu.Unlock()
	})

	store.AddTrigger(&Trigger{ID: "t1", EventType: "webhook", Channel: "f", ChatID: "c1", SenderID: "u1", MessageTpl: "msg1", Enabled: true})
	store.AddTrigger(&Trigger{ID: "t2", EventType: "webhook", Channel: "f", ChatID: "c2", SenderID: "u2", MessageTpl: "msg2", Enabled: true})
	store.AddTrigger(&Trigger{ID: "t3", EventType: "other", Channel: "f", ChatID: "c3", SenderID: "u3", MessageTpl: "msg3", Enabled: true})

	results := router.Dispatch(Event{Type: "webhook", Timestamp: time.Now()})
	if len(results) != 2 {
		t.Fatalf("expected 2 dispatch results, got %d", len(results))
	}
	if len(injected) != 2 {
		t.Fatalf("expected 2 injections, got %d", len(injected))
	}
}

func TestRouter_Dispatch_SignatureVerification(t *testing.T) {
	store := newMemTriggerStore()
	router := NewRouter(store)

	var injected int
	router.SetInjectFunc(func(msg Message) { injected++ })

	secret := "my-secret"
	store.AddTrigger(&Trigger{
		ID:         "trg_sig",
		EventType:  "webhook",
		Channel:    "f",
		ChatID:     "c",
		SenderID:   "u",
		MessageTpl: "signed",
		Secret:     secret,
		Enabled:    true,
	})

	body := []byte(`{"test":true}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	validSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// Valid signature
	evt := Event{
		Type:      "webhook",
		Payload:   map[string]any{"test": true},
		Headers:   map[string]string{"x-hub-signature-256": validSig},
		RawBody:   body,
		Timestamp: time.Now(),
	}
	results := router.Dispatch(evt)
	if len(results) != 1 || !results[0].OK {
		t.Fatal("expected successful dispatch with valid signature")
	}
	if injected != 1 {
		t.Errorf("expected 1 injection, got %d", injected)
	}

	// Invalid signature
	evtBad := Event{
		Type:      "webhook",
		Headers:   map[string]string{"x-hub-signature-256": "sha256=invalid"},
		RawBody:   body,
		Timestamp: time.Now(),
	}
	results = router.Dispatch(evtBad)
	if len(results) != 1 || results[0].OK {
		t.Fatal("expected failed dispatch with invalid signature")
	}
	if injected != 1 {
		t.Error("should not have injected with bad signature")
	}
}

func TestRouter_NoInjectFunc(t *testing.T) {
	store := newMemTriggerStore()
	router := NewRouter(store)

	// No SetInjectFunc called
	results := router.Dispatch(Event{Type: "webhook"})
	if results != nil {
		t.Errorf("expected nil results when no injectFunc, got %v", results)
	}
}
