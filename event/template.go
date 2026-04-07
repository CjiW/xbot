package event

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

// dig recursively accesses nested map keys.
// Usage in templates: {{dig .Payload "pull_request" "title"}}
func dig(m map[string]any, keys ...string) any {
	if len(keys) == 0 {
		return nil
	}
	val, ok := m[keys[0]]
	if !ok {
		return nil
	}
	if len(keys) == 1 {
		return val
	}
	sub, ok := val.(map[string]any)
	if !ok {
		return nil
	}
	return dig(sub, keys[1:]...)
}

// templateFuncs is the function map available in message templates.
var templateFuncs = template.FuncMap{
	"dig": dig,
}

// templateData is the data passed to message templates.
type templateData struct {
	EventType string            `json:"event_type"`
	Payload   map[string]any    `json:"payload"`
	Headers   map[string]string `json:"headers"`
	Timestamp string            `json:"timestamp"`
}

// RenderMessage renders a trigger's message template with the given event data.
// If tpl is empty or rendering fails, a sensible default is returned.
//
// Template syntax:
//
//	{{.EventType}}                          — event type string
//	{{.Payload}}                            — full payload as JSON
//	{{.Payload.action}}                     — top-level payload field
//	{{dig .Payload "pull_request" "title"}} — nested payload field (use dig for nested maps)
//	{{.Headers.x-github-event}}            — HTTP header
//	{{.Timestamp}}                          — event timestamp
func RenderMessage(tpl string, evt Event) string {
	data := templateData{
		EventType: evt.Type,
		Payload:   evt.Payload,
		Headers:   evt.Headers,
		Timestamp: evt.Timestamp.Format("2006-01-02 15:04:05"),
	}

	if tpl == "" {
		return defaultMessage(data)
	}

	t, err := template.New("msg").Funcs(templateFuncs).Option("missingkey=zero").Parse(tpl)
	if err != nil {
		return defaultMessage(data)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return defaultMessage(data)
	}

	result := strings.TrimSpace(buf.String())
	if result == "" {
		return defaultMessage(data)
	}
	return result
}

func defaultMessage(data templateData) string {
	summary := summarizePayload(data.Payload, 500)
	return fmt.Sprintf("[Event: %s] %s", data.EventType, summary)
}

func summarizePayload(payload map[string]any, maxLen int) string {
	if len(payload) == 0 {
		return "(empty payload)"
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "(payload marshal error)"
	}
	s := string(b)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
