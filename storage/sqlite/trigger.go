package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"xbot/event"
)

// TriggerService provides SQLite storage for event triggers.
// It implements event.TriggerStore.
type TriggerService struct {
	db *DB
}

// NewTriggerService creates a new TriggerService.
func NewTriggerService(db *DB) *TriggerService {
	return &TriggerService{db: db}
}

// AddTrigger inserts a new event trigger.
func (s *TriggerService) AddTrigger(t *event.Trigger) error {
	conn := s.db.Conn()
	var lastFiredStr *string
	if t.LastFired != nil {
		v := t.LastFired.Format(time.RFC3339)
		lastFiredStr = &v
	}
	_, err := conn.Exec(`
		INSERT INTO event_triggers (id, name, event_type, channel, chat_id, sender_id, message_tpl, secret, enabled, one_shot, created_at, last_fired, fire_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, t.ID, t.Name, t.EventType, t.Channel, t.ChatID, t.SenderID, t.MessageTpl, t.Secret,
		t.Enabled, t.OneShot, t.CreatedAt.Format(time.RFC3339), lastFiredStr, t.FireCount)
	if err != nil {
		return fmt.Errorf("insert event trigger: %w", err)
	}
	return nil
}

// RemoveTrigger deletes an event trigger by ID.
func (s *TriggerService) RemoveTrigger(id string) error {
	conn := s.db.Conn()
	_, err := conn.Exec(`DELETE FROM event_triggers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete event trigger: %w", err)
	}
	return nil
}

// GetTrigger retrieves an event trigger by ID.
func (s *TriggerService) GetTrigger(id string) (*event.Trigger, error) {
	conn := s.db.Conn()
	row := conn.QueryRow(`
		SELECT id, name, event_type, channel, chat_id, sender_id, message_tpl, secret, enabled, one_shot, created_at, last_fired, fire_count
		FROM event_triggers WHERE id = ?
	`, id)
	return scanTrigger(row)
}

// ListByEventType lists all enabled triggers for a given event type.
func (s *TriggerService) ListByEventType(eventType string) ([]*event.Trigger, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
		SELECT id, name, event_type, channel, chat_id, sender_id, message_tpl, secret, enabled, one_shot, created_at, last_fired, fire_count
		FROM event_triggers WHERE event_type = ? AND enabled = 1 ORDER BY created_at
	`, eventType)
	if err != nil {
		return nil, fmt.Errorf("query event triggers by type: %w", err)
	}
	defer rows.Close()
	return scanTriggers(rows)
}

// ListBySender lists all triggers for a given sender.
func (s *TriggerService) ListBySender(senderID string) ([]*event.Trigger, error) {
	conn := s.db.Conn()
	rows, err := conn.Query(`
		SELECT id, name, event_type, channel, chat_id, sender_id, message_tpl, secret, enabled, one_shot, created_at, last_fired, fire_count
		FROM event_triggers WHERE sender_id = ? ORDER BY created_at
	`, senderID)
	if err != nil {
		return nil, fmt.Errorf("query event triggers by sender: %w", err)
	}
	defer rows.Close()
	return scanTriggers(rows)
}

// UpdateEnabled sets the enabled flag for a trigger.
func (s *TriggerService) UpdateEnabled(id string, enabled bool) error {
	conn := s.db.Conn()
	_, err := conn.Exec(`UPDATE event_triggers SET enabled = ? WHERE id = ?`, enabled, id)
	if err != nil {
		return fmt.Errorf("update event trigger enabled: %w", err)
	}
	return nil
}

// RecordFire updates the last_fired timestamp and increments fire_count.
func (s *TriggerService) RecordFire(id string, at time.Time) error {
	conn := s.db.Conn()
	_, err := conn.Exec(`UPDATE event_triggers SET last_fired = ?, fire_count = fire_count + 1 WHERE id = ?`,
		at.Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("record event trigger fire: %w", err)
	}
	return nil
}

// scanTrigger scans a single trigger row.
func scanTrigger(row *sql.Row) (*event.Trigger, error) {
	t := &event.Trigger{}
	var createdAt string
	var lastFiredStr *string
	err := row.Scan(&t.ID, &t.Name, &t.EventType, &t.Channel, &t.ChatID, &t.SenderID,
		&t.MessageTpl, &t.Secret, &t.Enabled, &t.OneShot, &createdAt, &lastFiredStr, &t.FireCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan event trigger: %w", err)
	}
	return parseTriggerTimes(t, createdAt, lastFiredStr)
}

// scanTriggers scans multiple trigger rows.
func scanTriggers(rows *sql.Rows) ([]*event.Trigger, error) {
	var triggers []*event.Trigger
	for rows.Next() {
		t := &event.Trigger{}
		var createdAt string
		var lastFiredStr *string
		if err := rows.Scan(&t.ID, &t.Name, &t.EventType, &t.Channel, &t.ChatID, &t.SenderID,
			&t.MessageTpl, &t.Secret, &t.Enabled, &t.OneShot, &createdAt, &lastFiredStr, &t.FireCount); err != nil {
			return nil, fmt.Errorf("scan event trigger row: %w", err)
		}
		t, err := parseTriggerTimes(t, createdAt, lastFiredStr)
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event triggers: %w", err)
	}
	return triggers, nil
}

func parseTriggerTimes(t *event.Trigger, createdAt string, lastFiredStr *string) (*event.Trigger, error) {
	var err error
	t.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	if lastFiredStr != nil {
		ts, err := time.Parse(time.RFC3339, *lastFiredStr)
		if err != nil {
			return nil, fmt.Errorf("parse last_fired %q: %w", *lastFiredStr, err)
		}
		t.LastFired = &ts
	}
	return t, nil
}
