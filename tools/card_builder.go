package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CardBuilder manages card building sessions. Singleton shared across all tools.
type CardBuilder struct {
	mu       sync.RWMutex
	sessions map[string]*CardSession
	counter  atomic.Int64

	// descriptions: card_id -> description string, 在 RemoveSession 时清理（activeCards 除外）
	// activeCards 无法自动清理，但 chat_id 数量有限，内存影响可控
	descriptions sync.Map

	// expectedInteractions stores which interaction types a card expects (persists after session removal)
	expectedInteractions sync.Map // card_id -> []string
	// activeCards tracks chat_id -> card_id for skip handling
	activeCards sync.Map // chat_id -> card_id
	// elementOptions stores card_id -> map[elementName]optionsDescription for callback context
	elementOptions sync.Map // card_id -> map[string]string
}

// NewCardBuilder creates a CardBuilder instance.
func NewCardBuilder() *CardBuilder {
	return &CardBuilder{
		sessions: make(map[string]*CardSession),
	}
}

// CardSession holds the state of a card being built.
type CardSession struct {
	ID         string
	Header     map[string]any
	Config     map[string]any
	Elements   []*CardElement
	Containers map[string]*CardElement // id -> container element for parent_id lookup
	Channel    string
	ChatID     string
	SendFunc   func(channel, chatID, content string) error
	CreatedAt  time.Time

	// ExpectedInteractions tracks which interaction types this card should handle
	// e.g., "button", "select_static", "multi_select_static", "form_submit"
	ExpectedInteractions []string
}

// CardElement represents a single component in the card tree.
type CardElement struct {
	ID         string
	Tag        string
	Properties map[string]any
	Children   []*CardElement
}

// CreateSession creates a new card building session.
// Note: expired session cleanup is triggered lazily when CreateSession is called,
// not on a background timer. This is acceptable because card sessions are short-lived
// (users build cards interactively) and stale sessions consume minimal memory.
func (b *CardBuilder) CreateSession(channel, chatID string, sendFunc func(string, string, string) error) *CardSession {
	id := fmt.Sprintf("card_%d", b.counter.Add(1))
	s := &CardSession{
		ID:         id,
		Config:     map[string]any{"wide_screen_mode": true, "update_multi": true},
		Containers: make(map[string]*CardElement),
		Channel:    channel,
		ChatID:     chatID,
		SendFunc:   sendFunc,
		CreatedAt:  time.Now(),
	}
	b.mu.Lock()
	b.sessions[id] = s
	b.mu.Unlock()
	return s
}

// GetSession retrieves an existing session.
func (b *CardBuilder) GetSession(id string) (*CardSession, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	s, ok := b.sessions[id]
	return s, ok
}

// RemoveSession removes a session and cleans up associated sync.Map entries.
func (b *CardBuilder) RemoveSession(id string) {
	b.mu.Lock()
	delete(b.sessions, id)
	b.mu.Unlock()
	// Clean up associated sync.Map entries to mitigate memory growth.
	b.descriptions.Delete(id)
	b.expectedInteractions.Delete(id)
	b.elementOptions.Delete(id)
}

// ActiveCount returns number of active sessions.
func (b *CardBuilder) ActiveCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.sessions)
}

// SaveDescription stores a card's description for callback context (persists after session removal).
func (b *CardBuilder) SaveDescription(cardID, desc string) {
	b.descriptions.Store(cardID, desc)
}

// GetDescription retrieves a card's description by ID.
func (b *CardBuilder) GetDescription(cardID string) string {
	if v, ok := b.descriptions.Load(cardID); ok {
		return v.(string)
	}
	return ""
}

// SaveExpectedInteractions stores interaction types for callback routing (persists after session removal).
func (b *CardBuilder) SaveExpectedInteractions(cardID string, interactions []string) {
	b.expectedInteractions.Store(cardID, interactions)
}

// GetExpectedInteractions retrieves expected interaction types for a card.
func (b *CardBuilder) GetExpectedInteractions(cardID string) []string {
	if v, ok := b.expectedInteractions.Load(cardID); ok {
		return v.([]string)
	}
	return nil
}

// SaveActiveCard stores chat_id -> card_id mapping for skip handling.
// Only one active card per chat is tracked (the most recent one).
func (b *CardBuilder) SaveActiveCard(chatID, cardID string) {
	b.activeCards.Store(chatID, cardID)
}

// GetActiveCardID retrieves the active card ID for a chat.
func (b *CardBuilder) GetActiveCardID(chatID string) (string, bool) {
	if v, ok := b.activeCards.Load(chatID); ok {
		return v.(string), true
	}
	return "", false
}

// ClearActiveCard removes the active card mapping for a chat.
func (b *CardBuilder) ClearActiveCard(chatID string) {
	b.activeCards.Delete(chatID)
}

// SaveElementOptions stores element name -> options description mapping for a card.
func (b *CardBuilder) SaveElementOptions(cardID string, options map[string]string) {
	b.elementOptions.Store(cardID, options)
}

// GetElementOptions retrieves element options for a card by element name.
func (b *CardBuilder) GetElementOptions(cardID, elementName string) string {
	if v, ok := b.elementOptions.Load(cardID); ok {
		if opts, ok := v.(map[string]string); ok {
			return opts[elementName]
		}
	}
	return ""
}

// GetAllElementOptions retrieves all element options for a card.
func (b *CardBuilder) GetAllElementOptions(cardID string) map[string]string {
	if v, ok := b.elementOptions.Load(cardID); ok {
		if opts, ok := v.(map[string]string); ok {
			return opts
		}
	}
	return nil
}

// ---------- CardSession methods ----------

// SetHeader sets the card header.
func (s *CardSession) SetHeader(title, subtitle, template string) {
	if title == "" {
		return
	}
	h := map[string]any{
		"title": map[string]any{
			"tag":     "plain_text",
			"content": title,
		},
	}
	if subtitle != "" {
		h["subtitle"] = map[string]any{
			"tag":     "plain_text",
			"content": subtitle,
		}
	}
	if template != "" {
		h["template"] = template
	}
	s.Header = h
}

// AddElement adds an element to the card or to a parent container.
func (s *CardSession) AddElement(parentID string, elem *CardElement) error {
	if parentID == "" {
		s.Elements = append(s.Elements, elem)
		return nil
	}
	parent, ok := s.Containers[parentID]
	if !ok {
		return fmt.Errorf("parent container '%s' not found (available: %s)", parentID, s.containerIDs())
	}
	parent.Children = append(parent.Children, elem)
	return nil
}

// RegisterContainer registers an element as a container so children can reference it.
func (s *CardSession) RegisterContainer(elem *CardElement) {
	s.Containers[elem.ID] = elem
}

func (s *CardSession) containerIDs() string {
	if len(s.Containers) == 0 {
		return "none"
	}
	ids := ""
	for id := range s.Containers {
		if ids != "" {
			ids += ", "
		}
		ids += id
	}
	return ids
}

// NextElementID generates a unique element ID within this session.
func (s *CardSession) NextElementID(prefix string) string {
	return fmt.Sprintf("%s_%s_%d", s.ID, prefix, len(s.Containers)+len(s.Elements)+1)
}

// BuildJSON generates the complete Feishu card JSON 2.0 structure.
func (s *CardSession) BuildJSON() ([]byte, error) {
	s.ensureFormSubmitButtons()
	s.deduplicateNames()

	card := map[string]any{
		"schema": "2.0",
	}
	if s.Header != nil {
		card["header"] = s.Header
	}
	if s.Config != nil {
		card["config"] = s.Config
	}

	elements := make([]map[string]any, 0, len(s.Elements))
	for _, elem := range s.Elements {
		elements = append(elements, renderElement(elem))
	}
	card["body"] = map[string]any{
		"elements": elements,
	}

	return json.Marshal(card)
}

// deduplicateNames ensures all interactive element names are unique across the card.
// Feishu requires globally unique names for all interactive elements.
func (s *CardSession) deduplicateNames() {
	used := make(map[string]int)
	for _, elem := range s.Elements {
		deduplicateNamesInTree(elem, used)
	}
}

func deduplicateNamesInTree(elem *CardElement, used map[string]int) {
	if name, ok := elem.Properties["name"].(string); ok && name != "" {
		used[name]++
		if used[name] > 1 {
			newName := fmt.Sprintf("%s_%d", name, used[name])
			elem.Properties["name"] = newName
		}
	}
	for _, child := range elem.Children {
		deduplicateNamesInTree(child, used)
	}
}

// ensureFormSubmitButtons checks all form containers and auto-injects a submit
// button if none exists. Feishu requires at least one button with
// action_type=form_submit inside every form container.
func (s *CardSession) ensureFormSubmitButtons() {
	for _, elem := range s.Elements {
		ensureSubmitInTree(elem, s.ID)
	}
}

func ensureSubmitInTree(elem *CardElement, sessionID string) {
	if elem.Tag == "form" && !hasSubmitButton(elem) {
		formName, _ := elem.Properties["name"].(string)
		submitID := fmt.Sprintf("%s_submit_auto", elem.ID)
		submit := &CardElement{
			ID:  submitID,
			Tag: "button",
			Properties: map[string]any{
				"text":        map[string]any{"tag": "plain_text", "content": "提交"},
				"type":        "primary",
				"action_type": "form_submit",
				"name":        submitID,
				"value":       map[string]any{"card_id": sessionID, "form_name": formName},
			},
		}
		elem.Children = append(elem.Children, submit)
	}
	for _, child := range elem.Children {
		ensureSubmitInTree(child, sessionID)
	}
}

func hasSubmitButton(elem *CardElement) bool {
	if elem.Tag == "button" {
		if at, ok := elem.Properties["action_type"].(string); ok && at == "form_submit" {
			return true
		}
	}
	for _, child := range elem.Children {
		if hasSubmitButton(child) {
			return true
		}
	}
	return false
}

// PreviewSummary returns a human-readable summary of the card structure.
func (s *CardSession) PreviewSummary() string {
	summary := fmt.Sprintf("Card [%s]", s.ID)
	if s.Header != nil {
		if t, ok := s.Header["title"].(map[string]any); ok {
			summary += fmt.Sprintf(" title=%q", t["content"])
		}
	}
	summary += fmt.Sprintf("\nElements (%d top-level):", len(s.Elements))
	for i, e := range s.Elements {
		summary += "\n" + previewElement(e, i, 1)
	}
	return summary
}

// Description generates a rich context description of the card for LLM understanding.
// This is stored when the card is sent and injected into card action callbacks.
func (s *CardSession) Description() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Card [%s]", s.ID)
	if s.Header != nil {
		if t, ok := s.Header["title"].(map[string]any); ok {
			fmt.Fprintf(&sb, " — %s", t["content"])
		}
	}
	sb.WriteString("\nStructure:\n")
	for _, e := range s.Elements {
		describeElement(&sb, e, 1)
	}
	return sb.String()
}

// CollectExpectedInteractions scans all elements and records which interaction types
// this card should handle. This is called before sending the card.
// - Buttons are always handled
// - Form submissions are always handled
// - Standalone selects (not inside a form) are handled immediately on change
func (s *CardSession) CollectExpectedInteractions() {
	interactions := make(map[string]bool)

	// Track if we're inside a form
	var collectFromElement func(elem *CardElement, insideForm bool)
	collectFromElement = func(elem *CardElement, insideForm bool) {
		switch elem.Tag {
		case "button":
			// Buttons are always handled
			interactions["button"] = true
			// Check if this is a form submit button
			if at, ok := elem.Properties["action_type"].(string); ok && at == "form_submit" {
				interactions["form_submit"] = true
			}

		case "select_static", "multi_select_static",
			"select_person", "multi_select_person",
			"date_picker", "picker_time", "picker_datetime",
			"overflow", "checker", "select_img":
			// Only handle these immediately if NOT inside a form
			// Inside a form, they're collected on submit
			if !insideForm {
				interactions[elem.Tag] = true
			}

		case "form":
			// Mark that we're now inside a form
			for _, child := range elem.Children {
				collectFromElement(child, true)
			}
			return // Don't process children again

		case "input":
			// Input changes are not handled immediately; only on form submit
			// No action needed here
		}

		// Recurse into children
		for _, child := range elem.Children {
			collectFromElement(child, insideForm)
		}
	}

	for _, elem := range s.Elements {
		collectFromElement(elem, false)
	}

	// Convert to slice
	s.ExpectedInteractions = make([]string, 0, len(interactions))
	for interaction := range interactions {
		s.ExpectedInteractions = append(s.ExpectedInteractions, interaction)
	}
}

// CollectElementOptions collects options for all interactive elements that have options.
// Returns a map of element name -> options description for use in callbacks.
func (s *CardSession) CollectElementOptions() map[string]string {
	options := make(map[string]string)

	var collectFromElement func(elem *CardElement)
	collectFromElement = func(elem *CardElement) {
		name, _ := elem.Properties["name"].(string)

		switch elem.Tag {
		case "select_static", "multi_select_static", "overflow":
			if name != "" {
				if opts := describeOptionsWithValues(elem.Properties["options"]); opts != "" {
					options[name] = opts
				}
			}
		case "select_img":
			if name != "" {
				if opts, ok := elem.Properties["options"].([]map[string]any); ok {
					parts := make([]string, 0, len(opts))
					for _, opt := range opts {
						if v, ok := opt["value"].(string); ok {
							parts = append(parts, v)
						}
					}
					if len(parts) > 0 {
						options[name] = strings.Join(parts, ", ")
					}
				}
			}
		}

		// Recurse into children
		for _, child := range elem.Children {
			collectFromElement(child)
		}
	}

	for _, elem := range s.Elements {
		collectFromElement(elem)
	}

	return options
}

func describeElement(sb *strings.Builder, e *CardElement, depth int) {
	indent := strings.Repeat("  ", depth)
	switch e.Tag {
	case "markdown":
		content, _ := e.Properties["content"].(string)
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		fmt.Fprintf(sb, "%s- Text: %s\n", indent, content)
	case "div":
		if t, ok := e.Properties["text"].(map[string]any); ok {
			content, _ := t["content"].(string)
			if len(content) > 80 {
				content = content[:80] + "..."
			}
			fmt.Fprintf(sb, "%s- Text: %s\n", indent, content)
		}
	case "button":
		label := ""
		if t, ok := e.Properties["text"].(map[string]any); ok {
			label, _ = t["content"].(string)
		}
		name, _ := e.Properties["name"].(string)
		action, _ := e.Properties["action_type"].(string)
		if action != "" {
			fmt.Fprintf(sb, "%s- Button \"%s\" name=%s action=%s\n", indent, label, name, action)
		} else {
			fmt.Fprintf(sb, "%s- Button \"%s\" name=%s\n", indent, label, name)
		}
	case "input":
		name, _ := e.Properties["name"].(string)
		desc := ""
		if l, ok := e.Properties["label"].(map[string]any); ok {
			desc, _ = l["content"].(string)
		}
		if ph, ok := e.Properties["placeholder"].(map[string]any); ok {
			if desc == "" {
				desc, _ = ph["content"].(string)
			}
		}
		if desc != "" {
			fmt.Fprintf(sb, "%s- Input name=%s (%s)\n", indent, name, desc)
		} else {
			fmt.Fprintf(sb, "%s- Input name=%s\n", indent, name)
		}
	case "select_static", "multi_select_static":
		name, _ := e.Properties["name"].(string)
		opts := describeOptions(e.Properties["options"])
		fmt.Fprintf(sb, "%s- Select name=%s options: [%s]\n", indent, name, opts)
	case "select_person", "multi_select_person":
		name, _ := e.Properties["name"].(string)
		fmt.Fprintf(sb, "%s- Person picker name=%s\n", indent, name)
	case "date_picker", "picker_time", "picker_datetime":
		name, _ := e.Properties["name"].(string)
		fmt.Fprintf(sb, "%s- %s name=%s\n", indent, e.Tag, name)
	case "checker":
		name, _ := e.Properties["name"].(string)
		text := ""
		if t, ok := e.Properties["text"].(map[string]any); ok {
			text, _ = t["content"].(string)
		}
		fmt.Fprintf(sb, "%s- Checkbox name=%s \"%s\"\n", indent, name, text)
	case "overflow":
		name, _ := e.Properties["name"].(string)
		opts := describeOptions(e.Properties["options"])
		fmt.Fprintf(sb, "%s- Overflow name=%s options: [%s]\n", indent, name, opts)
	case "form":
		name, _ := e.Properties["name"].(string)
		fmt.Fprintf(sb, "%s- Form name=%s:\n", indent, name)
	case "column_set":
		fmt.Fprintf(sb, "%s- Columns:\n", indent)
	case "table":
		cols := ""
		if c, ok := e.Properties["columns"].([]map[string]any); ok {
			names := make([]string, 0, len(c))
			for _, col := range c {
				if dn, ok := col["display_name"].(string); ok {
					names = append(names, dn)
				} else if n, ok := col["name"].(string); ok {
					names = append(names, n)
				}
			}
			cols = strings.Join(names, ", ")
		}
		fmt.Fprintf(sb, "%s- Table columns: [%s]\n", indent, cols)
	case "img", "img_combination", "hr", "chart", "person", "person_list":
		fmt.Fprintf(sb, "%s- %s\n", indent, e.Tag)
	default:
		fmt.Fprintf(sb, "%s- [%s]\n", indent, e.Tag)
	}
	for _, child := range e.Children {
		describeElement(sb, child, depth+1)
	}
}

func describeOptions(opts any) string {
	arr, ok := opts.([]map[string]any)
	if !ok {
		return ""
	}
	labels := make([]string, 0, len(arr))
	for _, opt := range arr {
		if t, ok := opt["text"].(map[string]any); ok {
			if content, ok := t["content"].(string); ok {
				labels = append(labels, content)
				continue
			}
		}
		if v, ok := opt["value"].(string); ok {
			labels = append(labels, v)
		}
	}
	return strings.Join(labels, ", ")
}

// describeOptionsWithValues returns a detailed description of options including both text and value.
// This is useful for callback context where we need complete option information.
func describeOptionsWithValues(opts any) string {
	arr, ok := opts.([]map[string]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(arr))
	for _, opt := range arr {
		text := ""
		if t, ok := opt["text"].(map[string]any); ok {
			text, _ = t["content"].(string)
		}
		value, _ := opt["value"].(string)
		if text != "" && value != "" && text != value {
			parts = append(parts, fmt.Sprintf("%s (value: %s)", text, value))
		} else if text != "" {
			parts = append(parts, text)
		} else if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, ", ")
}

func previewElement(e *CardElement, idx, depth int) string {
	indent := ""
	for i := 0; i < depth; i++ {
		indent += "  "
	}
	line := fmt.Sprintf("%s%d. [%s] id=%s", indent, idx+1, e.Tag, e.ID)
	if len(e.Children) > 0 {
		line += fmt.Sprintf(" (%d children)", len(e.Children))
		for ci, child := range e.Children {
			line += "\n" + previewElement(child, ci, depth+1)
		}
	}
	return line
}

// renderElement recursively converts a CardElement tree to Feishu JSON.
func renderElement(e *CardElement) map[string]any {
	result := map[string]any{"tag": e.Tag}

	for k, v := range e.Properties {
		result[k] = v
	}

	if len(e.Children) > 0 {
		children := make([]map[string]any, 0, len(e.Children))
		for _, child := range e.Children {
			children = append(children, renderElement(child))
		}
		// Different containers use different keys for children
		switch e.Tag {
		case "column_set":
			result["columns"] = children
		case "column":
			result["elements"] = children
		case "form":
			result["elements"] = children
		case "collapsible_panel":
			result["elements"] = children
		case "interactive_container":
			result["elements"] = children
		case "action":
			result["actions"] = children
		default:
			result["elements"] = children
		}
	}

	return result
}

// ---------- Component builders ----------

// BuildMarkdown creates a markdown element.
func BuildMarkdown(content string, props map[string]any) *CardElement {
	p := map[string]any{"content": content}
	mergeProps(p, props, "text_align", "text_size", "icon")
	return &CardElement{Tag: "markdown", Properties: p}
}

// BuildDiv creates a plain text (div) element.
func BuildDiv(content string, props map[string]any) *CardElement {
	p := map[string]any{
		"text": map[string]any{
			"tag":     "plain_text",
			"content": content,
		},
	}
	mergeProps(p, props, "icon", "text_align", "text_size")
	return &CardElement{Tag: "div", Properties: p}
}

// BuildImage creates an image element.
func BuildImage(imgKey string, props map[string]any) *CardElement {
	p := map[string]any{
		"img_key": imgKey,
		"alt":     map[string]any{"tag": "plain_text", "content": ""},
	}
	if alt, ok := props["alt"].(string); ok && alt != "" {
		p["alt"] = map[string]any{"tag": "plain_text", "content": alt}
	}
	mergeProps(p, props, "mode", "compact_width", "preview", "custom_width")
	return &CardElement{Tag: "img", Properties: p}
}

// BuildImgCombination creates a multi-image layout element.
func BuildImgCombination(imgKeys []string, props map[string]any) *CardElement {
	imgList := make([]map[string]any, len(imgKeys))
	for i, key := range imgKeys {
		imgList[i] = map[string]any{"img_key": key}
	}
	p := map[string]any{"img_list": imgList}
	mergeProps(p, props, "combination_mode")
	return &CardElement{Tag: "img_combination", Properties: p}
}

// BuildDivider creates a horizontal rule element.
func BuildDivider() *CardElement {
	return &CardElement{Tag: "hr", Properties: map[string]any{}}
}

// BuildTable creates a table element. Feishu cards limit tables to 50 rows.
func BuildTable(columnsDef []map[string]any, rowsData []map[string]any, props map[string]any) *CardElement {
	if len(rowsData) > 50 {
		rowsData = rowsData[:50]
	}
	p := map[string]any{
		"page_size":    len(rowsData),
		"columns":      columnsDef,
		"rows":         rowsData,
		"row_height":   "low",
		"header_style": map[string]any{"bold": true, "text_align": "left"},
	}
	mergeProps(p, props, "page_size", "row_height", "header_style")
	return &CardElement{Tag: "table", Properties: p}
}

// BuildChart creates a chart element.
func BuildChart(chartSpec map[string]any) *CardElement {
	p := map[string]any{"chart_spec": chartSpec}
	return &CardElement{Tag: "chart", Properties: p}
}

// BuildPerson creates a person element.
func BuildPerson(userID string, props map[string]any) *CardElement {
	p := map[string]any{"user_id": userID, "size": "medium"}
	mergeProps(p, props, "size")
	return &CardElement{Tag: "person", Properties: p}
}

// BuildPersonList creates a person_list element.
func BuildPersonList(userIDs []string, props map[string]any) *CardElement {
	persons := make([]map[string]any, len(userIDs))
	for i, id := range userIDs {
		persons[i] = map[string]any{"id": id}
	}
	p := map[string]any{"persons": persons, "size": "medium", "lines": 1}
	mergeProps(p, props, "size", "show_name", "show_avatar", "lines")
	return &CardElement{Tag: "person_list", Properties: p}
}

// BuildButton creates a button element.
func BuildButton(text, btnType string, props map[string]any) *CardElement {
	p := map[string]any{
		"text": map[string]any{"tag": "plain_text", "content": text},
		"type": btnType,
	}
	if url, ok := props["url"].(string); ok && url != "" {
		p["url"] = url
	}
	if val, ok := props["value"]; ok {
		p["value"] = val
	}
	if name, ok := props["name"].(string); ok && name != "" {
		p["name"] = name
	}
	if actionType, ok := props["action_type"].(string); ok && actionType != "" {
		p["action_type"] = actionType
	}
	if confirm, ok := props["confirm"]; ok {
		p["confirm"] = confirm
	}
	mergeProps(p, props, "size", "icon", "complex_interaction", "width", "disabled")
	return &CardElement{Tag: "button", Properties: p}
}

// BuildInput creates an input element.
func BuildInput(name string, props map[string]any) *CardElement {
	p := map[string]any{"name": name}
	if label, ok := props["label"].(string); ok && label != "" {
		p["label"] = map[string]any{"tag": "plain_text", "content": label}
	}
	if ph, ok := props["placeholder"].(string); ok && ph != "" {
		p["placeholder"] = map[string]any{"tag": "plain_text", "content": ph}
	}
	mergeProps(p, props, "default_value", "max_length", "rows", "auto_resize", "max_rows", "width")
	return &CardElement{Tag: "input", Properties: p}
}

// BuildSelectStatic creates a single-select dropdown.
func BuildSelectStatic(name string, options []map[string]any, props map[string]any) *CardElement {
	p := map[string]any{"name": name, "options": options}
	if ph, ok := props["placeholder"].(string); ok && ph != "" {
		p["placeholder"] = map[string]any{"tag": "plain_text", "content": ph}
	}
	mergeProps(p, props, "initial_option", "width")
	return &CardElement{Tag: "select_static", Properties: p}
}

// BuildMultiSelectStatic creates a multi-select dropdown.
func BuildMultiSelectStatic(name string, options []map[string]any, props map[string]any) *CardElement {
	p := map[string]any{"name": name, "options": options}
	if ph, ok := props["placeholder"].(string); ok && ph != "" {
		p["placeholder"] = map[string]any{"tag": "plain_text", "content": ph}
	}
	mergeProps(p, props, "initial_options", "width")
	return &CardElement{Tag: "multi_select_static", Properties: p}
}

// BuildSelectPerson creates a single-select person picker.
func BuildSelectPerson(name string, props map[string]any) *CardElement {
	p := map[string]any{"name": name}
	if ph, ok := props["placeholder"].(string); ok && ph != "" {
		p["placeholder"] = map[string]any{"tag": "plain_text", "content": ph}
	}
	mergeProps(p, props, "width")
	return &CardElement{Tag: "select_person", Properties: p}
}

// BuildMultiSelectPerson creates a multi-select person picker.
func BuildMultiSelectPerson(name string, props map[string]any) *CardElement {
	p := map[string]any{"name": name}
	if ph, ok := props["placeholder"].(string); ok && ph != "" {
		p["placeholder"] = map[string]any{"tag": "plain_text", "content": ph}
	}
	mergeProps(p, props, "width")
	return &CardElement{Tag: "multi_select_person", Properties: p}
}

// BuildDatePicker creates a date picker element.
func BuildDatePicker(name string, props map[string]any) *CardElement {
	p := map[string]any{"name": name}
	if ph, ok := props["placeholder"].(string); ok && ph != "" {
		p["placeholder"] = map[string]any{"tag": "plain_text", "content": ph}
	}
	mergeProps(p, props, "initial_date", "width")
	return &CardElement{Tag: "date_picker", Properties: p}
}

// BuildTimePicker creates a time picker element.
func BuildTimePicker(name string, props map[string]any) *CardElement {
	p := map[string]any{"name": name}
	if ph, ok := props["placeholder"].(string); ok && ph != "" {
		p["placeholder"] = map[string]any{"tag": "plain_text", "content": ph}
	}
	mergeProps(p, props, "initial_time", "width")
	return &CardElement{Tag: "picker_time", Properties: p}
}

// BuildDateTimePicker creates a date-time picker element.
func BuildDateTimePicker(name string, props map[string]any) *CardElement {
	p := map[string]any{"name": name}
	if ph, ok := props["placeholder"].(string); ok && ph != "" {
		p["placeholder"] = map[string]any{"tag": "plain_text", "content": ph}
	}
	mergeProps(p, props, "initial_datetime", "width")
	return &CardElement{Tag: "picker_datetime", Properties: p}
}

// BuildOverflow creates an overflow button group element.
func BuildOverflow(name string, options []map[string]any, props map[string]any) *CardElement {
	p := map[string]any{"name": name, "options": options}
	mergeProps(p, props, "width")
	return &CardElement{Tag: "overflow", Properties: p}
}

// BuildChecker creates a checker (checkbox) element.
func BuildChecker(name, text string, props map[string]any) *CardElement {
	p := map[string]any{
		"name": name,
		"text": map[string]any{"tag": "plain_text", "content": text},
	}
	mergeProps(p, props, "checked", "overall", "button_area", "checked_style", "margin", "padding")
	return &CardElement{Tag: "checker", Properties: p}
}

// BuildSelectImg creates an image picker element.
func BuildSelectImg(name string, options []map[string]any, props map[string]any) *CardElement {
	p := map[string]any{"name": name, "options": options}
	mergeProps(p, props, "multi_select", "layout", "style", "can_preview")
	return &CardElement{Tag: "select_img", Properties: p}
}

// BuildColumnSet creates a column_set container with columns as children.
func BuildColumnSet(columnCount int, props map[string]any) (*CardElement, []string) {
	cs := &CardElement{
		Tag:        "column_set",
		Properties: map[string]any{},
		Children:   make([]*CardElement, columnCount),
	}
	mergeProps(cs.Properties, props, "flex_mode", "background_style", "horizontal_spacing", "horizontal_align", "margin", "action")

	columnIDs := make([]string, columnCount)
	for i := 0; i < columnCount; i++ {
		colID := fmt.Sprintf("%s_col_%d", cs.ID, i)
		col := &CardElement{
			ID:         colID,
			Tag:        "column",
			Properties: map[string]any{"width": "weighted", "weight": 1},
		}
		mergeProps(col.Properties, props, "") // columns get individual props via parent_id later
		if widths, ok := props["column_widths"].([]any); ok && i < len(widths) {
			if w, ok := widths[i].(float64); ok {
				col.Properties["weight"] = int(w)
			}
		}
		if valigns, ok := props["column_vertical_aligns"].([]any); ok && i < len(valigns) {
			if v, ok := valigns[i].(string); ok {
				col.Properties["vertical_align"] = v
			}
		}
		cs.Children[i] = col
		columnIDs[i] = colID
	}
	return cs, columnIDs
}

// BuildForm creates a form container element.
func BuildForm(name string) *CardElement {
	return &CardElement{
		Tag:        "form",
		Properties: map[string]any{"name": name},
	}
}

// BuildCollapsiblePanel creates a collapsible panel container.
func BuildCollapsiblePanel(title string, props map[string]any) *CardElement {
	p := map[string]any{
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": title,
			},
		},
	}
	if expanded, ok := props["expanded"]; ok {
		p["expanded"] = expanded
	}
	mergeProps(p, props, "border", "vertical_align", "background_style")
	return &CardElement{Tag: "collapsible_panel", Properties: p}
}

// BuildInteractiveContainer creates an interactive container element.
func BuildInteractiveContainer(props map[string]any) *CardElement {
	p := map[string]any{}
	mergeProps(p, props, "width", "height", "background_style", "has_border", "corner_radius", "padding", "behaviors", "disabled", "header")
	return &CardElement{Tag: "interactive_container", Properties: p}
}

// ---------- Helpers ----------

// mergeProps copies allowed keys from src to dst.
func mergeProps(dst, src map[string]any, keys ...string) {
	if src == nil {
		return
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		if v, ok := src[k]; ok {
			dst[k] = v
		}
	}
}

// ParseSelectOptions parses a JSON string into option elements for select components.
// Accepts: [{"text":"Label","value":"val"},...] or ["Label1","Label2",...]
func ParseSelectOptions(optionsJSON string) ([]map[string]any, error) {
	if optionsJSON == "" {
		return nil, fmt.Errorf("options is required")
	}

	var raw []any
	if err := json.Unmarshal([]byte(optionsJSON), &raw); err != nil {
		return nil, fmt.Errorf("invalid options JSON: %w", err)
	}

	if len(raw) == 0 {
		return nil, fmt.Errorf("options array is empty")
	}

	result := make([]map[string]any, len(raw))
	for i, item := range raw {
		switch v := item.(type) {
		case string:
			result[i] = map[string]any{
				"text":  map[string]any{"tag": "plain_text", "content": v},
				"value": v,
			}
		case map[string]any:
			opt := map[string]any{}
			if text, ok := v["text"].(string); ok {
				opt["text"] = map[string]any{"tag": "plain_text", "content": text}
			} else if textObj, ok := v["text"].(map[string]any); ok {
				opt["text"] = textObj
			} else {
				opt["text"] = map[string]any{"tag": "plain_text", "content": fmt.Sprintf("Option %d", i+1)}
			}
			if val, ok := v["value"]; ok {
				opt["value"] = val
			} else if text, ok := v["text"].(string); ok {
				opt["value"] = text
			}
			if icon, ok := v["icon"]; ok {
				opt["icon"] = icon
			}
			result[i] = opt
		default:
			return nil, fmt.Errorf("invalid option at index %d: expected string or object", i)
		}
	}
	return result, nil
}

// ParseImgSelectOptions parses options for image picker.
func ParseImgSelectOptions(optionsJSON string) ([]map[string]any, error) {
	if optionsJSON == "" {
		return nil, fmt.Errorf("options is required for select_img")
	}
	var raw []map[string]any
	if err := json.Unmarshal([]byte(optionsJSON), &raw); err != nil {
		return nil, fmt.Errorf("invalid select_img options JSON: %w", err)
	}
	for i, opt := range raw {
		if _, ok := opt["img_key"]; !ok {
			return nil, fmt.Errorf("option %d: missing img_key", i)
		}
		if _, ok := opt["value"]; !ok {
			return nil, fmt.Errorf("option %d: missing value", i)
		}
	}
	return raw, nil
}

// ParseProperties parses the optional properties JSON parameter.
func ParseProperties(propsJSON string) (map[string]any, error) {
	if propsJSON == "" {
		return map[string]any{}, nil
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(propsJSON), &props); err != nil {
		return nil, fmt.Errorf("invalid properties JSON: %w", err)
	}
	return props, nil
}
