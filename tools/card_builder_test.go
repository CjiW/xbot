package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCardBuilderSessionLifecycle(t *testing.T) {
	b := NewCardBuilder()

	if b.ActiveCount() != 0 {
		t.Fatal("expected 0 active sessions")
	}

	s := b.CreateSession("test", "chat1", nil)
	if s.ID == "" {
		t.Fatal("session ID should not be empty")
	}
	if b.ActiveCount() != 1 {
		t.Fatal("expected 1 active session")
	}

	got, ok := b.GetSession(s.ID)
	if !ok || got.ID != s.ID {
		t.Fatal("should find session by ID")
	}

	_, ok = b.GetSession("nonexistent")
	if ok {
		t.Fatal("should not find nonexistent session")
	}

	b.RemoveSession(s.ID)
	if b.ActiveCount() != 0 {
		t.Fatal("expected 0 active sessions after removal")
	}
}

func TestCardSessionBuildJSON_Empty(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)

	data, err := s.BuildJSON()
	if err != nil {
		t.Fatalf("BuildJSON failed: %v", err)
	}

	var card map[string]any
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if card["schema"] != "2.0" {
		t.Error("expected schema 2.0")
	}
	body, ok := card["body"].(map[string]any)
	if !ok {
		t.Fatal("expected body")
	}
	elements, ok := body["elements"].([]any)
	if !ok {
		t.Fatal("expected elements array")
	}
	if len(elements) != 0 {
		t.Error("expected empty elements")
	}
}

func TestCardSessionBuildJSON_WithHeader(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)
	s.SetHeader("Test Title", "Subtitle", "blue")

	data, err := s.BuildJSON()
	if err != nil {
		t.Fatalf("BuildJSON failed: %v", err)
	}

	var card map[string]any
	json.Unmarshal(data, &card)

	header, ok := card["header"].(map[string]any)
	if !ok {
		t.Fatal("expected header")
	}
	title := header["title"].(map[string]any)
	if title["content"] != "Test Title" {
		t.Errorf("expected title 'Test Title', got %v", title["content"])
	}
	if header["template"] != "blue" {
		t.Errorf("expected template 'blue', got %v", header["template"])
	}
}

func TestBuildMarkdown(t *testing.T) {
	elem := BuildMarkdown("**hello**", map[string]any{"text_align": "center"})
	if elem.Tag != "markdown" {
		t.Errorf("expected tag 'markdown', got '%s'", elem.Tag)
	}
	if elem.Properties["content"] != "**hello**" {
		t.Error("wrong content")
	}
	if elem.Properties["text_align"] != "center" {
		t.Error("text_align not set")
	}
}

func TestBuildDiv(t *testing.T) {
	elem := BuildDiv("plain text", nil)
	if elem.Tag != "div" {
		t.Errorf("expected tag 'div', got '%s'", elem.Tag)
	}
	text := elem.Properties["text"].(map[string]any)
	if text["content"] != "plain text" {
		t.Error("wrong text content")
	}
}

func TestBuildImage(t *testing.T) {
	elem := BuildImage("img_key_123", map[string]any{"alt": "photo", "mode": "crop_center"})
	if elem.Tag != "img" {
		t.Errorf("expected tag 'img', got '%s'", elem.Tag)
	}
	if elem.Properties["img_key"] != "img_key_123" {
		t.Error("wrong img_key")
	}
	alt := elem.Properties["alt"].(map[string]any)
	if alt["content"] != "photo" {
		t.Error("wrong alt text")
	}
	if elem.Properties["mode"] != "crop_center" {
		t.Error("mode not set")
	}
}

func TestBuildDivider(t *testing.T) {
	elem := BuildDivider()
	if elem.Tag != "hr" {
		t.Errorf("expected tag 'hr', got '%s'", elem.Tag)
	}
}

func TestBuildTable(t *testing.T) {
	cols := []map[string]any{
		{"name": "name", "display_name": "Name", "data_type": "text"},
		{"name": "score", "display_name": "Score", "data_type": "number"},
	}
	rows := []map[string]any{
		{"name": "Alice", "score": 95},
		{"name": "Bob", "score": 88},
	}
	elem := BuildTable(cols, rows, nil)
	if elem.Tag != "table" {
		t.Errorf("expected tag 'table', got '%s'", elem.Tag)
	}
	if elem.Properties["columns"] == nil {
		t.Error("expected columns in properties")
	}
}

func TestBuildButton(t *testing.T) {
	elem := BuildButton("Click me", "primary", map[string]any{
		"url":  "https://example.com",
		"name": "btn1",
	})
	if elem.Tag != "button" {
		t.Errorf("expected tag 'button', got '%s'", elem.Tag)
	}
	text := elem.Properties["text"].(map[string]any)
	if text["content"] != "Click me" {
		t.Error("wrong button text")
	}
	if elem.Properties["type"] != "primary" {
		t.Error("wrong button type")
	}
	if elem.Properties["url"] != "https://example.com" {
		t.Error("url not set")
	}
}

func TestEnsureFormSubmitButtons_MarksActionType(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test_card", "chat1", nil)
	s.Header = map[string]any{"title": map[string]any{"tag": "plain_text", "content": "Test"}}
	form := &CardElement{
		ID:  "form_1",
		Tag: "form",
		Properties: map[string]any{
			"name": "my_form",
		},
		Children: []*CardElement{
			{
				ID:  "btn_1",
				Tag: "button",
				Properties: map[string]any{
					"text": map[string]any{"tag": "plain_text", "content": "提交"},
					"type": "primary",
					"name": "btn_1",
				},
			},
		},
	}
	s.Elements = append(s.Elements, form)
	s.ensureFormSubmitButtons()

	// The existing button should now have action_type=form_submit
	btn := form.Children[0]
	if btn.Properties["action_type"] != "form_submit" {
		t.Errorf("expected action_type='form_submit', got '%v'", btn.Properties["action_type"])
	}
}

func TestEnsureFormSubmitButtons_AutoInjects(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test_card", "chat1", nil)
	s.Header = map[string]any{"title": map[string]any{"tag": "plain_text", "content": "Test"}}
	form := &CardElement{
		ID:  "form_1",
		Tag: "form",
		Properties: map[string]any{
			"name": "my_form",
		},
		Children: []*CardElement{
			{ID: "input_1", Tag: "input", Properties: map[string]any{"name": "field1"}},
		},
	}
	s.Elements = append(s.Elements, form)
	s.ensureFormSubmitButtons()

	// Should auto-inject a submit button
	if len(form.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(form.Children))
	}
	autoBtn := form.Children[1]
	if autoBtn.Tag != "button" {
		t.Errorf("expected auto-injected button, got '%s'", autoBtn.Tag)
	}
	if autoBtn.Properties["action_type"] != "form_submit" {
		t.Errorf("expected action_type='form_submit' on auto-injected button, got '%v'", autoBtn.Properties["action_type"])
	}
}

func TestBuildInput(t *testing.T) {
	elem := BuildInput("field1", map[string]any{"label": "Name", "placeholder": "Enter name"})
	if elem.Tag != "input" {
		t.Errorf("expected tag 'input', got '%s'", elem.Tag)
	}
	if elem.Properties["name"] != "field1" {
		t.Error("wrong name")
	}
	label := elem.Properties["label"].(map[string]any)
	if label["content"] != "Name" {
		t.Error("wrong label")
	}
}

func TestBuildSelectStatic(t *testing.T) {
	opts := []map[string]any{
		{"text": map[string]any{"tag": "plain_text", "content": "A"}, "value": "a"},
		{"text": map[string]any{"tag": "plain_text", "content": "B"}, "value": "b"},
	}
	elem := BuildSelectStatic("sel1", opts, map[string]any{"placeholder": "Choose"})
	if elem.Tag != "select_static" {
		t.Errorf("expected tag 'select_static', got '%s'", elem.Tag)
	}
	if elem.Properties["name"] != "sel1" {
		t.Error("wrong name")
	}
}

func TestBuildColumnSet(t *testing.T) {
	elem, colIDs := BuildColumnSet(3, nil)
	if elem.Tag != "column_set" {
		t.Errorf("expected tag 'column_set', got '%s'", elem.Tag)
	}
	if len(colIDs) != 3 {
		t.Errorf("expected 3 column IDs, got %d", len(colIDs))
	}
	if len(elem.Children) != 3 {
		t.Errorf("expected 3 children, got %d", len(elem.Children))
	}
	for _, child := range elem.Children {
		if child.Tag != "column" {
			t.Errorf("expected child tag 'column', got '%s'", child.Tag)
		}
	}
}

func TestBuildForm(t *testing.T) {
	elem := BuildForm("my_form")
	if elem.Tag != "form" {
		t.Errorf("expected tag 'form', got '%s'", elem.Tag)
	}
	if elem.Properties["name"] != "my_form" {
		t.Error("wrong form name")
	}
}

func TestBuildCollapsiblePanel(t *testing.T) {
	elem := BuildCollapsiblePanel("Details", map[string]any{"expanded": true})
	if elem.Tag != "collapsible_panel" {
		t.Errorf("expected tag 'collapsible_panel', got '%s'", elem.Tag)
	}
	header := elem.Properties["header"].(map[string]any)
	title := header["title"].(map[string]any)
	if title["content"] != "Details" {
		t.Error("wrong panel title")
	}
	if elem.Properties["expanded"] != true {
		t.Error("expanded not set")
	}
}

func TestParseSelectOptions_SimpleStrings(t *testing.T) {
	opts, err := ParseSelectOptions(`["Apple","Banana","Cherry"]`)
	if err != nil {
		t.Fatalf("ParseSelectOptions failed: %v", err)
	}
	if len(opts) != 3 {
		t.Fatalf("expected 3 options, got %d", len(opts))
	}
	text := opts[0]["text"].(map[string]any)
	if text["content"] != "Apple" {
		t.Errorf("expected 'Apple', got %v", text["content"])
	}
	if opts[0]["value"] != "Apple" {
		t.Errorf("expected value 'Apple', got %v", opts[0]["value"])
	}
}

func TestParseSelectOptions_Objects(t *testing.T) {
	opts, err := ParseSelectOptions(`[{"text":"A","value":"a"},{"text":"B","value":"b"}]`)
	if err != nil {
		t.Fatalf("ParseSelectOptions failed: %v", err)
	}
	if len(opts) != 2 {
		t.Fatalf("expected 2 options, got %d", len(opts))
	}
}

func TestParseSelectOptions_Invalid(t *testing.T) {
	_, err := ParseSelectOptions("")
	if err == nil {
		t.Error("expected error for empty options")
	}
	_, err = ParseSelectOptions("not json")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	_, err = ParseSelectOptions("[]")
	if err == nil {
		t.Error("expected error for empty array")
	}
}

func TestContainerNesting(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)

	// Add a column_set container
	cs, colIDs := BuildColumnSet(2, nil)
	cs.ID = s.NextElementID("cs")
	for i, child := range cs.Children {
		child.ID = colIDs[i]
		s.RegisterContainer(child)
	}
	s.RegisterContainer(cs)
	s.AddElement("", cs)

	// Add markdown to first column
	md := BuildMarkdown("## Column 1", nil)
	md.ID = s.NextElementID("md")
	err := s.AddElement(colIDs[0], md)
	if err != nil {
		t.Fatalf("AddElement to column failed: %v", err)
	}

	// Add button to second column
	btn := BuildButton("OK", "primary", nil)
	btn.ID = s.NextElementID("btn")
	err = s.AddElement(colIDs[1], btn)
	if err != nil {
		t.Fatalf("AddElement to column failed: %v", err)
	}

	// Verify structure
	if len(s.Elements) != 1 {
		t.Fatalf("expected 1 top-level element, got %d", len(s.Elements))
	}
	if len(cs.Children[0].Children) != 1 {
		t.Fatal("expected 1 child in first column")
	}
	if len(cs.Children[1].Children) != 1 {
		t.Fatal("expected 1 child in second column")
	}

	// Build JSON and verify structure
	data, err := s.BuildJSON()
	if err != nil {
		t.Fatalf("BuildJSON failed: %v", err)
	}

	var card map[string]any
	json.Unmarshal(data, &card)

	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if len(elements) != 1 {
		t.Fatal("expected 1 top-level element in JSON")
	}

	csJSON := elements[0].(map[string]any)
	if csJSON["tag"] != "column_set" {
		t.Error("expected column_set tag")
	}

	columns := csJSON["columns"].([]any)
	if len(columns) != 2 {
		t.Fatal("expected 2 columns")
	}

	col0 := columns[0].(map[string]any)
	col0Elems := col0["elements"].([]any)
	if len(col0Elems) != 1 {
		t.Fatal("expected 1 element in column 0")
	}
	if col0Elems[0].(map[string]any)["tag"] != "markdown" {
		t.Error("expected markdown in column 0")
	}
}

func TestAddElementToNonexistentParent(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)

	md := BuildMarkdown("test", nil)
	md.ID = "test"
	err := s.AddElement("nonexistent_parent", md)
	if err == nil {
		t.Error("expected error for nonexistent parent")
	}
}

func TestNewCardTools(t *testing.T) {
	b := NewCardBuilder()
	registry := NewRegistry()

	for _, tool := range NewCardTools(b) {
		registry.Register(tool)
	}

	expectedTools := []string{"card_create", "card_add_content", "card_add_interactive", "card_add_container", "card_preview", "card_send"}
	for _, name := range expectedTools {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("tool '%s' should be registered", name)
		}
	}
}

func TestCardCreateTool(t *testing.T) {
	b := NewCardBuilder()
	tool := NewCardCreateTool(b)

	ctx := &ToolContext{Channel: "test", ChatID: "chat1"}
	result, err := tool.Execute(ctx, `{"title":"Report","template":"blue"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Summary, "card_") {
		t.Error("expected card_id in result")
	}
}

func TestCardAddContentTool_Markdown(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)
	tool := &CardAddContentTool{builder: b}

	ctx := &ToolContext{Channel: "test", ChatID: "chat1"}
	input := `{"card_id":"` + s.ID + `","type":"markdown","content":"# Hello"}`
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Summary, "markdown") {
		t.Error("expected markdown in result")
	}
	if len(s.Elements) != 1 {
		t.Fatal("expected 1 element")
	}
}

func TestCardAddContentTool_MissingContent(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)
	tool := &CardAddContentTool{builder: b}

	ctx := &ToolContext{}
	_, err := tool.Execute(ctx, `{"card_id":"`+s.ID+`","type":"markdown"}`)
	if err == nil {
		t.Error("expected error for missing content")
	}
}

func TestCardAddContentTool_InvalidSession(t *testing.T) {
	b := NewCardBuilder()
	tool := &CardAddContentTool{builder: b}

	ctx := &ToolContext{}
	_, err := tool.Execute(ctx, `{"card_id":"nonexistent","type":"markdown","content":"test"}`)
	if err == nil {
		t.Error("expected error for invalid session")
	}
}

func TestCardAddInteractiveTool_Button(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)
	tool := &CardAddInteractiveTool{builder: b}

	ctx := &ToolContext{}
	input := `{"card_id":"` + s.ID + `","type":"button","text":"Click","properties":"{\"button_type\":\"primary\"}"}`
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Summary, "button") {
		t.Error("expected button in result")
	}

	// Verify card_id injected into value
	elem := s.Elements[0]
	val := elem.Properties["value"].(map[string]any)
	if val["card_id"] != s.ID {
		t.Error("card_id should be injected into button value")
	}
}

func TestCardAddInteractiveTool_SelectStatic(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)

	// Create a form container first (select_static must be inside a form)
	containerTool := &CardAddContainerTool{builder: b}
	ctx := &ToolContext{}
	formInput := `{"card_id":"` + s.ID + `","type":"form","properties":"{\"name\":\"test_form\"}"}`
	formResult, err := containerTool.Execute(ctx, formInput)
	if err != nil {
		t.Fatalf("Create form failed: %v", err)
	}
	// Extract form container ID from result
	parts := strings.SplitN(formResult.Summary, "id: ", 2)
	if len(parts) < 2 {
		t.Fatal("expected form ID in result")
	}
	formID := strings.SplitN(parts[1], ",", 2)[0]

	// Add select_static inside the form
	tool := &CardAddInteractiveTool{builder: b}
	input := `{"card_id":"` + s.ID + `","type":"select_static","name":"color","options":"[\"Red\",\"Blue\",\"Green\"]","parent_id":"` + formID + `"}`
	_, err = tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	// Element should be inside the form container, not at root
	if len(s.Elements) != 1 {
		t.Errorf("expected 1 root element (form), got %d", len(s.Elements))
	}
	if s.Elements[0].Tag != "form" {
		t.Errorf("expected root tag 'form', got '%s'", s.Elements[0].Tag)
	}
	if len(s.Elements[0].Children) != 1 { // select_static (submit button injected at BuildJSON time)
		t.Errorf("expected 1 child in form, got %d", len(s.Elements[0].Children))
	}
}

func TestCardAddInteractiveTool_SelectStatic_OutsideForm(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)
	tool := &CardAddInteractiveTool{builder: b}

	ctx := &ToolContext{}
	input := `{"card_id":"` + s.ID + `","type":"select_static","name":"color","options":"[\"Red\",\"Blue\",\"Green\"]"}`
	_, err := tool.Execute(ctx, input)
	if err == nil {
		t.Fatal("expected error when adding select_static outside form")
	}
	if !strings.Contains(err.Error(), "MUST be placed inside a form container") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCardAddContainerTool_ColumnSet(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)
	tool := &CardAddContainerTool{builder: b}

	ctx := &ToolContext{}
	input := `{"card_id":"` + s.ID + `","type":"column_set","properties":"{\"column_count\":3}"}`
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Summary, "column_set") {
		t.Error("expected column_set in result")
	}
	if !strings.Contains(result.Summary, "_col_") {
		t.Error("expected column IDs in result")
	}
	if len(s.Containers) != 4 { // 1 column_set + 3 columns
		t.Errorf("expected 4 containers, got %d", len(s.Containers))
	}
}

func TestCardAddContainerTool_Form(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)
	tool := &CardAddContainerTool{builder: b}

	ctx := &ToolContext{}
	input := `{"card_id":"` + s.ID + `","type":"form","properties":"{\"name\":\"my_form\"}"}`
	result, err := tool.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Summary, "form") {
		t.Error("expected form in result")
	}
}

func TestCardSendTool(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)

	// Add an element
	md := BuildMarkdown("test", nil)
	md.ID = "test_md"
	s.AddElement("", md)

	var sentContent string
	s.SendFunc = func(ch, chatID, content string) error {
		sentContent = content
		return nil
	}

	tool := &CardSendTool{builder: b}

	ctx := &ToolContext{}
	result, err := tool.Execute(ctx, `{"card_id":"`+s.ID+`"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Summary, "sent successfully") {
		t.Error("expected success message")
	}

	// Verify the sent content: format is __FEISHU_CARD__:card_id:{json}
	if !strings.HasPrefix(sentContent, "__FEISHU_CARD__:") {
		t.Error("expected __FEISHU_CARD__ prefix")
	}

	payload := strings.TrimPrefix(sentContent, "__FEISHU_CARD__:")
	jsonStart := strings.Index(payload, ":{")
	if jsonStart < 0 {
		t.Fatal("expected card_id:{json} format")
	}
	cardJSON := payload[jsonStart+1:]
	var card map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatalf("sent content is not valid JSON: %v", err)
	}
	if card["schema"] != "2.0" {
		t.Error("expected schema 2.0")
	}

	// Session should be removed
	_, ok := b.GetSession(s.ID)
	if ok {
		t.Error("session should be removed after send")
	}
}

func TestCardSendTool_WaitResponse(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)
	md := BuildMarkdown("test", nil)
	md.ID = "test_md"
	s.AddElement("", md)
	s.SendFunc = func(ch, chatID, content string) error { return nil }

	tool := &CardSendTool{builder: b}
	ctx := &ToolContext{}
	result, err := tool.Execute(ctx, `{"card_id":"`+s.ID+`","wait_response":"true"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !result.WaitingUser {
		t.Error("expected WaitingUser to be true")
	}
}

func TestCardSendTool_EmptyCard(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)

	tool := &CardSendTool{builder: b}
	ctx := &ToolContext{}
	_, err := tool.Execute(ctx, `{"card_id":"`+s.ID+`"}`)
	if err == nil {
		t.Error("expected error for empty card")
	}
}

func TestCardPreviewTool(t *testing.T) {
	b := NewCardBuilder()
	s := b.CreateSession("test", "chat1", nil)
	s.SetHeader("Test", "", "")

	md := BuildMarkdown("hello", nil)
	md.ID = "md1"
	s.AddElement("", md)

	tool := &CardPreviewTool{builder: b}
	ctx := &ToolContext{}
	result, err := tool.Execute(ctx, `{"card_id":"`+s.ID+`"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Summary, "Test") {
		t.Error("expected title in preview")
	}
	if !strings.Contains(result.Summary, "markdown") {
		t.Error("expected markdown in preview")
	}
}

func TestFullCardBuildFlow(t *testing.T) {
	b := NewCardBuilder()
	registry := NewRegistry()
	createTool := NewCardCreateTool(b)

	// Step 1: Create card
	ctx := &ToolContext{Registry: registry, Channel: "feishu", ChatID: "chat_001"}
	result, err := createTool.Execute(ctx, `{"title":"Weekly Report","template":"turquoise"}`)
	if err != nil {
		t.Fatalf("card_create failed: %v", err)
	}

	// Extract card_id from result
	cardID := ""
	for _, s := range strings.Split(result.Summary, "\n") {
		if strings.HasPrefix(s, "Card created: ") {
			cardID = strings.TrimPrefix(s, "Card created: ")
			break
		}
	}
	if cardID == "" {
		t.Fatal("could not extract card_id")
	}

	// Step 2: Add content
	contentTool := &CardAddContentTool{builder: b}
	_, err = contentTool.Execute(ctx, `{"card_id":"`+cardID+`","type":"markdown","content":"## Summary\nThis week was productive."}`)
	if err != nil {
		t.Fatalf("card_add_content failed: %v", err)
	}

	// Step 3: Add divider
	_, err = contentTool.Execute(ctx, `{"card_id":"`+cardID+`","type":"divider"}`)
	if err != nil {
		t.Fatalf("card_add_content divider failed: %v", err)
	}

	// Step 4: Add table
	_, err = contentTool.Execute(ctx, `{"card_id":"`+cardID+`","type":"table","columns_def":"[{\"name\":\"task\",\"display_name\":\"Task\",\"data_type\":\"text\"},{\"name\":\"status\",\"display_name\":\"Status\",\"data_type\":\"text\"}]","rows_data":"[{\"task\":\"Feature A\",\"status\":\"Done\"},{\"task\":\"Bug fix\",\"status\":\"In Progress\"}]"}`)
	if err != nil {
		t.Fatalf("card_add_content table failed: %v", err)
	}

	// Step 5: Add button
	interactiveTool := &CardAddInteractiveTool{builder: b}
	_, err = interactiveTool.Execute(ctx, `{"card_id":"`+cardID+`","type":"button","text":"View Details","url":"https://example.com","properties":"{\"button_type\":\"primary\"}"}`)
	if err != nil {
		t.Fatalf("card_add_interactive failed: %v", err)
	}

	// Step 6: Preview
	previewTool := &CardPreviewTool{builder: b}
	previewResult, err := previewTool.Execute(ctx, `{"card_id":"`+cardID+`"}`)
	if err != nil {
		t.Fatalf("card_preview failed: %v", err)
	}
	if !strings.Contains(previewResult.Summary, "Weekly Report") {
		t.Error("preview should contain title")
	}
	if !strings.Contains(previewResult.Summary, "4 top-level") {
		t.Error("preview should show 4 top-level elements")
	}

	// Step 7: Send
	var sentJSON string
	session, _ := b.GetSession(cardID)
	session.SendFunc = func(ch, chatID, content string) error {
		sentJSON = content
		return nil
	}

	sendTool := &CardSendTool{builder: b}
	_, err = sendTool.Execute(ctx, `{"card_id":"`+cardID+`"}`)
	if err != nil {
		t.Fatalf("card_send failed: %v", err)
	}

	// Validate output JSON: format is __FEISHU_CARD__:card_id:{json}
	rawPayload := strings.TrimPrefix(sentJSON, "__FEISHU_CARD__:")
	jsonIdx := strings.Index(rawPayload, ":{")
	if jsonIdx < 0 {
		t.Fatal("expected card_id:{json} format")
	}
	raw := rawPayload[jsonIdx+1:]
	var card map[string]any
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatalf("invalid card JSON: %v", err)
	}

	if card["schema"] != "2.0" {
		t.Error("schema should be 2.0")
	}

	header := card["header"].(map[string]any)
	title := header["title"].(map[string]any)
	if title["content"] != "Weekly Report" {
		t.Errorf("expected title 'Weekly Report', got %v", title["content"])
	}

	body := card["body"].(map[string]any)
	elements := body["elements"].([]any)
	if len(elements) != 4 {
		t.Errorf("expected 4 elements, got %d", len(elements))
	}

	// Check element tags
	expectedTags := []string{"markdown", "hr", "table", "button"}
	for i, e := range elements {
		elem := e.(map[string]any)
		if elem["tag"] != expectedTags[i] {
			t.Errorf("element %d: expected tag '%s', got '%s'", i, expectedTags[i], elem["tag"])
		}
	}
}
