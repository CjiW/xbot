package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
)

// feishuOnlyTool wraps a Tool to restrict it to Feishu channel only.
type feishuOnlyTool struct {
	Tool
}

func (t *feishuOnlyTool) SupportedChannels() []string {
	return []string{"feishu"}
}

// NewCardTools returns all card-related tools for startup registration.
// All card tools are restricted to Feishu channel only (CLI uses AskUser).
func NewCardTools(builder *CardBuilder) []Tool {
	wrap := func(t Tool) Tool { return &feishuOnlyTool{Tool: t} }
	return []Tool{
		wrap(&CardCreateTool{builder: builder}),
		wrap(&CardAddContentTool{builder: builder}),
		wrap(&CardAddInteractiveTool{builder: builder}),
		wrap(&CardAddContainerTool{builder: builder}),
		wrap(&CardPreviewTool{builder: builder}),
		wrap(&CardSendTool{builder: builder}),
	}
}

// ============================================================
// 1. card_create
// ============================================================

type CardCreateTool struct {
	builder *CardBuilder
}

func NewCardCreateTool(builder *CardBuilder) *CardCreateTool {
	return &CardCreateTool{builder: builder}
}

func (t *CardCreateTool) Name() string { return "card_create" }

func (t *CardCreateTool) Description() string {
	return `Create a new Feishu interactive card. Returns a card_id for subsequent card_add_*, card_preview, and card_send calls.`
}

func (t *CardCreateTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "title", Type: "string", Description: "Card header title (optional)", Required: false},
		{Name: "subtitle", Type: "string", Description: "Card header subtitle (optional)", Required: false},
		{Name: "template", Type: "string", Description: "Header color: blue, turquoise, green, yellow, orange, red, purple, indigo, wathet, grey (optional)", Required: false},
	}
}

func (t *CardCreateTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args struct {
		Title    string `json:"title"`
		Subtitle string `json:"subtitle"`
		Template string `json:"template"`
	}
	if input != "" && input != "{}" {
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return nil, fmt.Errorf("parse arguments: %w", err)
		}
	}

	session := t.builder.CreateSession(ctx.Channel, ctx.ChatID, ctx.SendFunc)
	session.SetHeader(args.Title, args.Subtitle, args.Template)

	return NewResult(fmt.Sprintf(`Card created: %s

Now use these tools to build the card:
- card_add_content: Add display elements (type: markdown, div, image, divider, table, chart, person, person_list, img_combination)
- card_add_interactive: Add interactive elements (type: button, input, select_static, multi_select_static, select_person, multi_select_person, date_picker, picker_time, picker_datetime, overflow, checker, select_img)
- card_add_container: Add layout containers (type: column_set, form, collapsible_panel, interactive_container) — returns container_id for nesting
- card_preview: Preview current card structure
- card_send: Build and send the card`, session.ID)), nil
}

// ============================================================
// 2. card_add_content
// ============================================================

type CardAddContentTool struct {
	builder *CardBuilder
}

func (t *CardAddContentTool) Name() string { return "card_add_content" }

func (t *CardAddContentTool) Description() string {
	return `Add a display component to a card.

Supported types:
- markdown: Rich text. Params: content (required), properties: {text_align, text_size, icon}
- div: Plain text. Params: content (required), properties: {text_align, text_size, icon}
- image: Image. Params: img_key (required), properties: {alt, mode, compact_width, preview}
- img_combination: Multi-image layout. Params: img_key (comma-separated keys), properties: {combination_mode}
- divider: Horizontal line. No extra params.
- table: Table. Params: columns_def (JSON array of column defs), rows_data (JSON array of row objects)
- chart: Chart (VChart). Params: chart_spec (VChart JSON spec)
- person: User profile. Params: user_ids (single user ID string), properties: {size}
- person_list: User list. Params: user_ids (JSON array of user ID strings), properties: {size, lines}`
}

func (t *CardAddContentTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "card_id", Type: "string", Description: "Card session ID from card_create", Required: true},
		{Name: "type", Type: "string", Description: "Component type: markdown, div, image, img_combination, divider, table, chart, person, person_list", Required: true},
		{Name: "content", Type: "string", Description: "Text content (for markdown/div)", Required: false},
		{Name: "img_key", Type: "string", Description: "Image key (for image) or comma-separated keys (for img_combination)", Required: false},
		{Name: "columns_def", Type: "string", Description: `Table columns JSON. Example: [{"name":"col1","display_name":"Name","data_type":"text"},{"name":"col2","display_name":"Score","data_type":"number"}]`, Required: false},
		{Name: "rows_data", Type: "string", Description: `Table rows JSON. Example: [{"col1":"Alice","col2":95},{"col1":"Bob","col2":88}]`, Required: false},
		{Name: "chart_spec", Type: "string", Description: "VChart specification JSON (for chart)", Required: false},
		{Name: "user_ids", Type: "string", Description: "User ID (person) or JSON array of user IDs (person_list)", Required: false},
		{Name: "properties", Type: "string", Description: "Additional type-specific properties as JSON", Required: false},
		{Name: "parent_id", Type: "string", Description: "Parent container ID for nesting inside a container", Required: false},
	}
}

func (t *CardAddContentTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args struct {
		CardID     string `json:"card_id"`
		Type       string `json:"type"`
		Content    string `json:"content"`
		ImgKey     string `json:"img_key"`
		ColumnsDef string `json:"columns_def"`
		RowsData   string `json:"rows_data"`
		ChartSpec  string `json:"chart_spec"`
		UserIDs    string `json:"user_ids"`
		Properties string `json:"properties"`
		ParentID   string `json:"parent_id"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	session, ok := t.builder.GetSession(args.CardID)
	if !ok {
		return nil, fmt.Errorf("card session '%s' not found. Create one with card_create first", args.CardID)
	}

	props, err := ParseProperties(args.Properties)
	if err != nil {
		return nil, err
	}

	var elem *CardElement
	typeName := args.Type

	switch typeName {
	case "markdown":
		if args.Content == "" {
			return nil, fmt.Errorf("content is required for markdown")
		}
		elem = BuildMarkdown(args.Content, props)

	case "div":
		if args.Content == "" {
			return nil, fmt.Errorf("content is required for div")
		}
		elem = BuildDiv(args.Content, props)

	case "image":
		if args.ImgKey == "" {
			return nil, fmt.Errorf("img_key is required for image")
		}
		elem = BuildImage(args.ImgKey, props)

	case "img_combination":
		if args.ImgKey == "" {
			return nil, fmt.Errorf("img_key is required for img_combination (comma-separated keys)")
		}
		keys := strings.Split(args.ImgKey, ",")
		for i := range keys {
			keys[i] = strings.TrimSpace(keys[i])
		}
		elem = BuildImgCombination(keys, props)

	case "divider":
		elem = BuildDivider()

	case "table":
		if args.ColumnsDef == "" || args.RowsData == "" {
			return nil, fmt.Errorf("columns_def and rows_data are required for table")
		}
		var colDef []map[string]any
		if err := json.Unmarshal([]byte(args.ColumnsDef), &colDef); err != nil {
			return nil, fmt.Errorf("invalid columns_def JSON: %w", err)
		}
		var rowData []map[string]any
		if err := json.Unmarshal([]byte(args.RowsData), &rowData); err != nil {
			return nil, fmt.Errorf("invalid rows_data JSON: %w", err)
		}
		if len(rowData) > 50 {
			return nil, fmt.Errorf("table rows_data exceeds 50-row limit (got %d). Split into multiple tables or reduce data", len(rowData))
		}
		elem = BuildTable(colDef, rowData, props)

	case "chart":
		if args.ChartSpec == "" {
			return nil, fmt.Errorf("chart_spec is required for chart")
		}
		var spec map[string]any
		if err := json.Unmarshal([]byte(args.ChartSpec), &spec); err != nil {
			return nil, fmt.Errorf("invalid chart_spec JSON: %w", err)
		}
		elem = BuildChart(spec)

	case "person":
		if args.UserIDs == "" {
			return nil, fmt.Errorf("user_ids is required for person (single user ID)")
		}
		elem = BuildPerson(args.UserIDs, props)

	case "person_list":
		if args.UserIDs == "" {
			return nil, fmt.Errorf("user_ids is required for person_list (JSON array)")
		}
		var ids []string
		if err := json.Unmarshal([]byte(args.UserIDs), &ids); err != nil {
			ids = []string{args.UserIDs}
		}
		elem = BuildPersonList(ids, props)

	default:
		return nil, fmt.Errorf("unsupported content type '%s'. Supported: markdown, div, image, img_combination, divider, table, chart, person, person_list", typeName)
	}

	elem.ID = session.NextElementID(typeName)
	if err := session.AddElement(args.ParentID, elem); err != nil {
		return nil, err
	}

	return NewResult(fmt.Sprintf("Added %s element (id: %s) to card %s", typeName, elem.ID, args.CardID)), nil
}

// ============================================================
// 3. card_add_interactive
// ============================================================

type CardAddInteractiveTool struct {
	builder *CardBuilder
}

func (t *CardAddInteractiveTool) Name() string { return "card_add_interactive" }

// formRequiredTypes are interactive types that MUST be placed inside a form container.
// Placing them outside a form causes each interaction to trigger individual callbacks,
// which leads to Feishu restoring the card to its original template state (since the
// callback response may not include a Card field for non-form interactions).
var formRequiredTypes = map[string]bool{
	"input":               true,
	"select_static":       true,
	"multi_select_static": true,
	"select_person":       true,
	"multi_select_person": true,
	"date_picker":         true,
	"picker_time":         true,
	"picker_datetime":     true,
	"checker":             true,
	"select_img":          true,
}

func (t *CardAddInteractiveTool) Description() string {
	return `Add an interactive component to a card.

Supported types:
- button: Params: text (required), properties: {button_type (primary/danger/default), url, name, confirm: {title,text}, size, value}
  IMPORTANT: For form submit, add a button INSIDE a form container. Buttons inside forms automatically trigger form submission with all input/select values.
- input: Params: name (required), properties: {label, placeholder, default_value, max_length, rows} — MUST be inside a form container
- select_static: Single select. Params: name (required), options (JSON array, required). Options format: ["Label1","Label2"] or [{"text":"Label","value":"val"}]. properties: {placeholder, initial_option} — MUST be inside a form container
- multi_select_static: Multi select. Same as select_static. properties: {placeholder, initial_options} — MUST be inside a form container
- select_person: Person picker single. Params: name (required). properties: {placeholder} — MUST be inside a form container
- multi_select_person: Person picker multi. Params: name (required). properties: {placeholder} — MUST be inside a form container
- date_picker: Params: name (required). properties: {placeholder, initial_date} — MUST be inside a form container
- picker_time: Params: name (required). properties: {placeholder, initial_time} — MUST be inside a form container
- picker_datetime: Params: name (required). properties: {placeholder, initial_datetime} — MUST be inside a form container
- overflow: Folded button group. Params: name (required), options (JSON array of {text,value}).
- checker: Checkbox/task. Params: name (required), text (required). properties: {checked, overall} — MUST be inside a form container
- select_img: Image picker. Params: name (required), options (JSON array of {img_key,value}) — MUST be inside a form container

IMPORTANT: input, select_*, person picker, date/time picker, checker, and select_img MUST be placed inside a form container (use card_add_container type=form first, then set parent_id to the form's ID). If you need to collect information from the user, always use a form. If no user input is needed, use pure markdown cards with buttons only.`
}

func (t *CardAddInteractiveTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "card_id", Type: "string", Description: "Card session ID", Required: true},
		{Name: "type", Type: "string", Description: "Component type: button, input, select_static, multi_select_static, select_person, multi_select_person, date_picker, picker_time, picker_datetime, overflow, checker, select_img", Required: true},
		{Name: "name", Type: "string", Description: "Element name for callbacks (required for all except button)", Required: false},
		{Name: "text", Type: "string", Description: "Display text (button label, checker text)", Required: false},
		{Name: "options", Type: "string", Description: `Options JSON array. Simple: ["A","B","C"]. Full: [{"text":"A","value":"a"}]`, Required: false},
		{Name: "url", Type: "string", Description: "Link URL (for button with link action)", Required: false},
		{Name: "value", Type: "string", Description: "Callback value JSON (for button)", Required: false},
		{Name: "properties", Type: "string", Description: "Additional type-specific properties JSON", Required: false},
		{Name: "parent_id", Type: "string", Description: "Parent container ID for nesting", Required: false},
	}
}

func (t *CardAddInteractiveTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args struct {
		CardID     string `json:"card_id"`
		Type       string `json:"type"`
		Name       string `json:"name"`
		Text       string `json:"text"`
		Options    string `json:"options"`
		URL        string `json:"url"`
		Value      string `json:"value"`
		Properties string `json:"properties"`
		ParentID   string `json:"parent_id"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	session, ok := t.builder.GetSession(args.CardID)
	if !ok {
		return nil, fmt.Errorf("card session '%s' not found", args.CardID)
	}

	props, err := ParseProperties(args.Properties)
	if err != nil {
		return nil, err
	}

	typeName := args.Type

	// Enforce: form-required interactive types must be inside a form container.
	// Placing them at root level causes per-interaction callbacks that don't
	// return a Card field, causing Feishu to restore the card to original state.
	if formRequiredTypes[typeName] {
		if args.ParentID == "" {
			return nil, fmt.Errorf(
				"type '%s' MUST be placed inside a form container. "+
					"First create a form with card_add_container(type='form'), "+
					"then use the returned container ID as parent_id. "+
					"If you need to collect user input, always use a form. "+
					"If no input is needed, use pure markdown cards with buttons only",
				typeName,
			)
		}
		if parent, ok := session.Containers[args.ParentID]; !ok || parent.Tag != "form" {
			return nil, fmt.Errorf(
				"type '%s' must be inside a form container, but parent_id '%s' is not a form. "+
					"First create a form with card_add_container(type='form'), "+
					"then use the returned form ID as parent_id",
				typeName, args.ParentID,
			)
		}
	}

	var elem *CardElement
	elemID := session.NextElementID(typeName)

	switch typeName {
	case "button":
		if args.Text == "" {
			return nil, fmt.Errorf("text is required for button")
		}
		btnType, _ := props["button_type"].(string)
		if btnType == "" {
			btnType = "default"
		}
		delete(props, "button_type")

		if args.URL != "" {
			props["url"] = args.URL
		}
		if args.Value != "" {
			var val any
			if err := json.Unmarshal([]byte(args.Value), &val); err != nil {
				val = args.Value
			}
			if valMap, ok := val.(map[string]any); ok {
				valMap["card_id"] = args.CardID
				props["value"] = valMap
			} else {
				props["value"] = map[string]any{"card_id": args.CardID, "data": val}
			}
		} else {
			props["value"] = map[string]any{"card_id": args.CardID}
		}
		if args.Name != "" {
			props["name"] = args.Name
		} else {
			props["name"] = elemID
		}
		elem = BuildButton(args.Text, btnType, props)

	case "input":
		if args.Name == "" {
			return nil, fmt.Errorf("name is required for input")
		}
		elem = BuildInput(args.Name, props)

	case "select_static":
		if args.Name == "" {
			return nil, fmt.Errorf("name is required for select_static")
		}
		opts, err := ParseSelectOptions(args.Options)
		if err != nil {
			return nil, fmt.Errorf("select_static: %w", err)
		}
		elem = BuildSelectStatic(args.Name, opts, props)

	case "multi_select_static":
		if args.Name == "" {
			return nil, fmt.Errorf("name is required for multi_select_static")
		}
		opts, err := ParseSelectOptions(args.Options)
		if err != nil {
			return nil, fmt.Errorf("multi_select_static: %w", err)
		}
		elem = BuildMultiSelectStatic(args.Name, opts, props)

	case "select_person":
		if args.Name == "" {
			return nil, fmt.Errorf("name is required for select_person")
		}
		elem = BuildSelectPerson(args.Name, props)

	case "multi_select_person":
		if args.Name == "" {
			return nil, fmt.Errorf("name is required for multi_select_person")
		}
		elem = BuildMultiSelectPerson(args.Name, props)

	case "date_picker":
		if args.Name == "" {
			return nil, fmt.Errorf("name is required for date_picker")
		}
		elem = BuildDatePicker(args.Name, props)

	case "picker_time":
		if args.Name == "" {
			return nil, fmt.Errorf("name is required for picker_time")
		}
		elem = BuildTimePicker(args.Name, props)

	case "picker_datetime":
		if args.Name == "" {
			return nil, fmt.Errorf("name is required for picker_datetime")
		}
		elem = BuildDateTimePicker(args.Name, props)

	case "overflow":
		if args.Name == "" {
			return nil, fmt.Errorf("name is required for overflow")
		}
		opts, err := ParseSelectOptions(args.Options)
		if err != nil {
			return nil, fmt.Errorf("overflow: %w", err)
		}
		elem = BuildOverflow(args.Name, opts, props)

	case "checker":
		if args.Name == "" || args.Text == "" {
			return nil, fmt.Errorf("name and text are required for checker")
		}
		elem = BuildChecker(args.Name, args.Text, props)

	case "select_img":
		if args.Name == "" {
			return nil, fmt.Errorf("name is required for select_img")
		}
		opts, err := ParseImgSelectOptions(args.Options)
		if err != nil {
			return nil, err
		}
		elem = BuildSelectImg(args.Name, opts, props)

	default:
		return nil, fmt.Errorf("unsupported interactive type '%s'. Supported: button, input, select_static, multi_select_static, select_person, multi_select_person, date_picker, picker_time, picker_datetime, overflow, checker, select_img", typeName)
	}

	elem.ID = elemID
	if err := session.AddElement(args.ParentID, elem); err != nil {
		return nil, err
	}

	return NewResult(fmt.Sprintf("Added %s element (id: %s) to card %s", typeName, elem.ID, args.CardID)), nil
}

// ============================================================
// 4. card_add_container
// ============================================================

type CardAddContainerTool struct {
	builder *CardBuilder
}

func (t *CardAddContainerTool) Name() string { return "card_add_container" }

func (t *CardAddContainerTool) Description() string {
	return `Add a layout container to a card. Returns container_id(s) to use as parent_id in subsequent add calls.

Supported types:
- column_set: Multi-column layout. properties: {column_count (required, int), flex_mode, background_style, horizontal_spacing, column_widths (array of weight ints), column_vertical_aligns (array of "top"/"center"/"bottom")}. Returns column IDs for each column.
- form: Form container. properties: {name (required)}. Add inputs/selects inside, then add a submit button INSIDE the form. Buttons inside forms automatically trigger form submission. WITHOUT placing the button inside the form, form data will NOT be submitted when the button is clicked.
- collapsible_panel: Foldable section. properties: {title (required), expanded (bool)}
- interactive_container: Clickable container. properties: {width, height, background_style, has_border, corner_radius, padding, behaviors}

Containers can be nested (except form and table cannot be inside other containers).`
}

func (t *CardAddContainerTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "card_id", Type: "string", Description: "Card session ID", Required: true},
		{Name: "type", Type: "string", Description: "Container type: column_set, form, collapsible_panel, interactive_container", Required: true},
		{Name: "properties", Type: "string", Description: "Type-specific properties JSON (see description for each type)", Required: false},
		{Name: "parent_id", Type: "string", Description: "Parent container ID for nesting containers", Required: false},
	}
}

func (t *CardAddContainerTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args struct {
		CardID     string `json:"card_id"`
		Type       string `json:"type"`
		Properties string `json:"properties"`
		ParentID   string `json:"parent_id"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	session, ok := t.builder.GetSession(args.CardID)
	if !ok {
		return nil, fmt.Errorf("card session '%s' not found", args.CardID)
	}

	props, err := ParseProperties(args.Properties)
	if err != nil {
		return nil, err
	}

	typeName := args.Type

	switch typeName {
	case "column_set":
		colCount := 2
		if c, ok := props["column_count"].(float64); ok {
			colCount = int(c)
		}
		if colCount < 1 || colCount > 6 {
			return nil, fmt.Errorf("column_count must be 1-6, got %d", colCount)
		}
		delete(props, "column_count")

		elem, colIDs := BuildColumnSet(colCount, props)
		elem.ID = session.NextElementID("column_set")
		// Fix column IDs to use the actual element ID prefix
		for i := range colIDs {
			colIDs[i] = fmt.Sprintf("%s_col_%d", elem.ID, i)
			elem.Children[i].ID = colIDs[i]
			session.RegisterContainer(elem.Children[i])
		}

		if err := session.AddElement(args.ParentID, elem); err != nil {
			return nil, err
		}
		session.RegisterContainer(elem)

		return NewResult(fmt.Sprintf("Added column_set (id: %s) with %d columns to card %s.\nColumn IDs: %s\nUse these column IDs as parent_id to add content into each column.",
			elem.ID, colCount, args.CardID, strings.Join(colIDs, ", "))), nil

	case "form":
		name, _ := props["name"].(string)
		if name == "" {
			name = "form_" + session.NextElementID("form")
		}
		elem := BuildForm(name)
		elem.ID = session.NextElementID("form")

		if err := session.AddElement(args.ParentID, elem); err != nil {
			return nil, err
		}
		session.RegisterContainer(elem)

		return NewResult(fmt.Sprintf("Added form container (id: %s, name: %s) to card %s.\nAdd input/select components with parent_id=%s, then add a submit button inside the form.",
			elem.ID, name, args.CardID, elem.ID)), nil

	case "collapsible_panel":
		title, _ := props["title"].(string)
		if title == "" {
			return nil, fmt.Errorf("properties.title is required for collapsible_panel")
		}
		delete(props, "title")

		elem := BuildCollapsiblePanel(title, props)
		elem.ID = session.NextElementID("collapsible")

		if err := session.AddElement(args.ParentID, elem); err != nil {
			return nil, err
		}
		session.RegisterContainer(elem)

		return NewResult(fmt.Sprintf("Added collapsible_panel (id: %s, title: %q) to card %s.\nUse parent_id=%s to add content inside.",
			elem.ID, title, args.CardID, elem.ID)), nil

	case "interactive_container":
		elem := BuildInteractiveContainer(props)
		elem.ID = session.NextElementID("ic")

		if err := session.AddElement(args.ParentID, elem); err != nil {
			return nil, err
		}
		session.RegisterContainer(elem)

		return NewResult(fmt.Sprintf("Added interactive_container (id: %s) to card %s.\nUse parent_id=%s to add content inside.",
			elem.ID, args.CardID, elem.ID)), nil

	default:
		return nil, fmt.Errorf("unsupported container type '%s'. Supported: column_set, form, collapsible_panel, interactive_container", typeName)
	}
}

// ============================================================
// 5. card_preview
// ============================================================

type CardPreviewTool struct {
	builder *CardBuilder
}

func (t *CardPreviewTool) Name() string { return "card_preview" }

func (t *CardPreviewTool) Description() string {
	return "Preview the current structure of a card being built. Shows elements hierarchy without the full JSON."
}

func (t *CardPreviewTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "card_id", Type: "string", Description: "Card session ID", Required: true},
	}
}

func (t *CardPreviewTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args struct {
		CardID string `json:"card_id"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	session, ok := t.builder.GetSession(args.CardID)
	if !ok {
		return nil, fmt.Errorf("card session '%s' not found", args.CardID)
	}

	return NewResult(session.PreviewSummary()), nil
}

// ============================================================
// 6. card_send
// ============================================================

type CardSendTool struct {
	builder *CardBuilder
}

func (t *CardSendTool) Name() string { return "card_send" }

func (t *CardSendTool) Description() string {
	return "Build the final card JSON and send it to the current chat. Set wait_response to 'true' if the card contains interactive elements and you need to wait for user response."
}

func (t *CardSendTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "card_id", Type: "string", Description: "Card session ID", Required: true},
		{Name: "wait_response", Type: "string", Description: "Set to 'true' to wait for user interaction before continuing", Required: false},
	}
}

func (t *CardSendTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args struct {
		CardID       string `json:"card_id"`
		WaitResponse string `json:"wait_response"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	session, ok := t.builder.GetSession(args.CardID)
	if !ok {
		return nil, fmt.Errorf("card session '%s' not found", args.CardID)
	}

	if len(session.Elements) == 0 {
		return nil, fmt.Errorf("card has no elements. Add content before sending")
	}

	cardJSON, err := session.BuildJSON()
	if err != nil {
		return nil, fmt.Errorf("build card JSON: %w", err)
	}

	// Collect and save card metadata for callback handling
	session.CollectExpectedInteractions()
	t.builder.SaveDescription(session.ID, session.Description())
	t.builder.SaveExpectedInteractions(session.ID, session.ExpectedInteractions)
	t.builder.SaveElementOptions(session.ID, session.CollectElementOptions())

	if session.SendFunc != nil {
		if err := session.SendFunc(session.Channel, session.ChatID, "__FEISHU_CARD__:"+session.ID+":"+string(cardJSON)); err != nil {
			return nil, fmt.Errorf("send card: %w", err)
		}
	}

	waiting := strings.EqualFold(args.WaitResponse, "true")
	if waiting {
		// Only track active card when waiting for user response (form cards).
		// Non-form cards (pure markdown/buttons without wait_response) should behave
		// like regular messages and should NOT be patched when user replies with text.
		if session.ChatID != "" {
			t.builder.SaveActiveCard(session.ChatID, session.ID)
		}
		// Don't remove session or metadata — callback needs Description/ExpectedInteractions
		t.builder.MarkCardWaiting(args.CardID)
		return NewResultWithUserResponse(fmt.Sprintf("Card %s sent successfully. Waiting for user interaction...", args.CardID)), nil
	}

	t.builder.RemoveSession(args.CardID)

	return NewResult(fmt.Sprintf("Card %s sent successfully.", args.CardID)), nil
}
