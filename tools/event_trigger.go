package tools

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"xbot/event"
	"xbot/llm"
	log "xbot/logger"

	"github.com/google/uuid"
)

// EventTriggerTool manages event triggers (webhook subscriptions).
type EventTriggerTool struct {
	router  *event.Router
	baseURL string // webhook base URL for generating URLs
}

// NewEventTriggerTool creates a new EventTriggerTool.
func NewEventTriggerTool(router *event.Router, baseURL string) *EventTriggerTool {
	return &EventTriggerTool{router: router, baseURL: baseURL}
}

func (t *EventTriggerTool) Name() string { return "EventTrigger" }

func (t *EventTriggerTool) Description() string {
	return `Manage event triggers (webhook subscriptions) that let external services push events to the agent.
Actions: add, list, remove, enable, disable.
- add: create a webhook trigger. Returns a URL that external services can POST to. When a request arrives, the message template is rendered with event data and sent to the agent.
- list: show all triggers for the current user
- remove: delete a trigger by trigger_id
- enable/disable: toggle a trigger`
}

func (t *EventTriggerTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action: add, list, remove, enable, disable", Required: true},
		{Name: "name", Type: "string", Description: "Human-readable trigger name (for add)", Required: false},
		{Name: "message", Type: "string", Description: "Message template sent to the agent when triggered. Go template syntax: {{.EventType}}, {{.Payload}} (full JSON), {{.Payload.action}} (top-level field), {{dig .Payload \"pull_request\" \"title\"}} (nested field), {{.Headers.x-github-event}}, {{.Timestamp}}", Required: false},
		{Name: "secret", Type: "string", Description: "Webhook signing secret for HMAC-SHA256 verification. Use 'auto' to auto-generate. If omitted, no signature verification is performed.", Required: false},
		{Name: "one_shot", Type: "boolean", Description: "If true, the trigger auto-disables after the first event (default: false)", Required: false},
		{Name: "trigger_id", Type: "string", Description: "Trigger ID (for remove/enable/disable)", Required: false},
	}
}

type eventTriggerParams struct {
	Action    string `json:"action"`
	Name      string `json:"name"`
	Message   string `json:"message"`
	Secret    string `json:"secret"`
	OneShot   bool   `json:"one_shot"`
	TriggerID string `json:"trigger_id"`
}

func (t *EventTriggerTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	p, err := parseToolArgs[eventTriggerParams](input)
	if err != nil {
		return nil, err
	}

	senderID := ""
	if ctx != nil {
		senderID = ctx.SenderID
	}

	switch p.Action {
	case "add":
		return t.addTrigger(ctx, *p)
	case "list":
		return t.listTriggers(senderID)
	case "remove":
		return t.removeTrigger(p.TriggerID, senderID)
	case "enable":
		return t.setEnabled(p.TriggerID, senderID, true)
	case "disable":
		return t.setEnabled(p.TriggerID, senderID, false)
	default:
		return nil, fmt.Errorf("unknown action: %s (use add, list, remove, enable, disable)", p.Action)
	}
}

func (t *EventTriggerTool) addTrigger(ctx *ToolContext, p eventTriggerParams) (*ToolResult, error) {
	if p.Message == "" {
		return nil, fmt.Errorf("message template is required for add")
	}

	secret := p.Secret
	if secret == "auto" {
		b := make([]byte, 20)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("generate secret: %w", err)
		}
		secret = hex.EncodeToString(b)
	}

	trigger := &event.Trigger{
		ID:         fmt.Sprintf("trg_%s", uuid.New().String()),
		Name:       p.Name,
		EventType:  "webhook",
		MessageTpl: p.Message,
		Secret:     secret,
		Enabled:    true,
		OneShot:    p.OneShot,
		CreatedAt:  time.Now(),
		FireCount:  0,
	}

	if ctx != nil {
		trigger.Channel = ctx.Channel
		trigger.ChatID = ctx.ChatID
		trigger.SenderID = ctx.SenderID
	}

	if err := t.router.RegisterTrigger(trigger); err != nil {
		log.WithError(err).Error("Failed to save event trigger")
		return nil, fmt.Errorf("failed to save trigger: %w", err)
	}

	webhookURL := fmt.Sprintf("%s/hooks/%s", strings.TrimRight(t.baseURL, "/"), trigger.ID)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Trigger created: %s\n", trigger.ID)
	if trigger.Name != "" {
		fmt.Fprintf(&sb, "Name: %s\n", trigger.Name)
	}
	fmt.Fprintf(&sb, "Webhook URL: %s\n", webhookURL)
	if secret != "" {
		fmt.Fprintf(&sb, "Secret: %s\n", secret)
		fmt.Fprintf(&sb, "Signature header: X-Hub-Signature-256 (sha256=<hmac>) or X-Webhook-Signature\n")
	}
	if trigger.OneShot {
		fmt.Fprintf(&sb, "Mode: one-shot (auto-disables after first event)\n")
	}
	fmt.Fprintf(&sb, "\nConfigure the above URL in the external service's webhook settings.")

	return NewResult(sb.String()), nil
}

func (t *EventTriggerTool) listTriggers(senderID string) (*ToolResult, error) {
	triggers, err := t.router.ListTriggers(senderID)
	if err != nil {
		log.WithError(err).Error("Failed to list event triggers")
		return nil, fmt.Errorf("failed to list triggers: %w", err)
	}

	if len(triggers) == 0 {
		return NewResult("No event triggers registered."), nil
	}

	slices.SortFunc(triggers, func(a, b *event.Trigger) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "Event triggers (%d):\n\n", len(triggers))
	for _, tr := range triggers {
		status := "enabled"
		if !tr.Enabled {
			status = "disabled"
		}
		webhookURL := fmt.Sprintf("%s/hooks/%s", strings.TrimRight(t.baseURL, "/"), tr.ID)
		fmt.Fprintf(&sb, "- **%s**", tr.ID)
		if tr.Name != "" {
			fmt.Fprintf(&sb, " (%s)", tr.Name)
		}
		fmt.Fprintf(&sb, "\n  Status: %s | Fires: %d\n  URL: %s\n  Channel: %s | Chat: %s\n",
			status, tr.FireCount, webhookURL, tr.Channel, tr.ChatID)
		if tr.LastFired != nil {
			fmt.Fprintf(&sb, "  Last fired: %s\n", tr.LastFired.Format("2006-01-02 15:04:05 MST"))
		}
		sb.WriteString("\n")
	}
	return NewResult(sb.String()), nil
}

func (t *EventTriggerTool) removeTrigger(triggerID string, senderID string) (*ToolResult, error) {
	if triggerID == "" {
		return nil, fmt.Errorf("trigger_id is required for remove")
	}

	tr, err := t.router.GetTrigger(triggerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get trigger: %w", err)
	}
	if tr == nil {
		return nil, fmt.Errorf("trigger not found: %s", triggerID)
	}
	if tr.SenderID != senderID {
		return nil, fmt.Errorf("trigger not found: %s", triggerID)
	}

	if err := t.router.RemoveTrigger(triggerID); err != nil {
		return nil, fmt.Errorf("failed to remove trigger: %w", err)
	}
	return NewResult(fmt.Sprintf("Trigger removed: %s", triggerID)), nil
}

func (t *EventTriggerTool) setEnabled(triggerID string, senderID string, enabled bool) (*ToolResult, error) {
	if triggerID == "" {
		return nil, fmt.Errorf("trigger_id is required")
	}

	tr, err := t.router.GetTrigger(triggerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get trigger: %w", err)
	}
	if tr == nil {
		return nil, fmt.Errorf("trigger not found: %s", triggerID)
	}
	if tr.SenderID != senderID {
		return nil, fmt.Errorf("trigger not found: %s", triggerID)
	}

	var fn func(string) error
	if enabled {
		fn = t.router.EnableTrigger
	} else {
		fn = t.router.DisableTrigger
	}

	if err := fn(triggerID); err != nil {
		return nil, fmt.Errorf("failed to update trigger: %w", err)
	}

	action := "enabled"
	if !enabled {
		action = "disabled"
	}
	return NewResult(fmt.Sprintf("Trigger %s: %s", action, triggerID)), nil
}
