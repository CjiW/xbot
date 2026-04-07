package event

import "time"

// Trigger defines a subscription that maps an external event to an agent context.
// When a matching event arrives, the router renders the message template and
// injects it into the agent loop targeting (Channel, ChatID).
type Trigger struct {
	ID         string     `json:"id"`          // "trg_" + 8-char UUID
	Name       string     `json:"name"`        // human-readable name
	EventType  string     `json:"event_type"`  // match key: "webhook"
	Channel    string     `json:"channel"`     // target IM channel
	ChatID     string     `json:"chat_id"`     // target chat
	SenderID   string     `json:"sender_id"`   // creator (access control)
	MessageTpl string     `json:"message_tpl"` // Go text/template with {{.payload}} etc.
	Secret     string     `json:"secret"`      // HMAC-SHA256 signing secret (may be encrypted at rest)
	Enabled    bool       `json:"enabled"`
	OneShot    bool       `json:"one_shot"` // auto-disable after first fire
	CreatedAt  time.Time  `json:"created_at"`
	LastFired  *time.Time `json:"last_fired,omitempty"`
	FireCount  int64      `json:"fire_count"`
}
