package feishu_mcp

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
	"xbot/tools"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	bitablev1 "github.com/larksuite/oapi-sdk-go/v3/service/bitable/v1"
)

// bitableRecordArgs holds arguments for bitable record operations.
type bitableRecordArgs struct {
	Action   string         `json:"action"`
	AppToken string         `json:"app_token"`
	TableID  string         `json:"table_id"`
	Filter   map[string]any `json:"filter"`
	Fields   map[string]any `json:"fields"`
	RecordID string         `json:"record_id"`
}

// BitableFieldsTool lists fields in a Bitable table.
type BitableFieldsTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *BitableFieldsTool) Name() string { return "feishu_bitable_fields" }

func (t *BitableFieldsTool) Description() string {
	return "List all fields in a Feishu Bitable table."
}

func (t *BitableFieldsTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "app_token",
			Type:        "string",
			Description: "Bitable app token (from the URL, e.g., bascxxxxx)",
			Required:    true,
		},
		{
			Name:        "table_id",
			Type:        "string",
			Description: "Table ID (from the URL, e.g., tblxxxxx)",
			Required:    true,
		},
	}
}

func (t *BitableFieldsTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		AppToken string `json:"app_token"`
		TableID  string `json:"table_id"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	req := bitablev1.NewListAppTableFieldReqBuilder().
		AppToken(args.AppToken).
		TableId(args.TableID).
		Build()

	resp, err := client.Client().Bitable.AppTableField.List(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("list fields: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	// Format result
	var result []map[string]any
	for _, item := range resp.Data.Items {
		field := map[string]any{
			"field_name": item.FieldName,
			"type":       item.Type,
			"ui_type":    item.UiType,
			"desc":       item.Description,
		}
		result = append(result, field)
	}

	summary, _ := json.MarshalIndent(result, "", "  ")
	return tools.NewResultWithTips(
		fmt.Sprintf("Fields: %s", summary),
		"Use feishu_bitable_record with action='search' to query records, or action='create' to add new records.",
	), nil
}

// BitableRecordTool searches, creates, or updates records in a Bitable table.
type BitableRecordTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *BitableRecordTool) Name() string { return "feishu_bitable_record" }

func (t *BitableRecordTool) Description() string {
	return "Query, create, or update records in Feishu Bitable."
}

func (t *BitableRecordTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "action",
			Type:        "string",
			Description: "Action to perform: search, create, or update",
			Required:    true,
		},
		{
			Name:        "app_token",
			Type:        "string",
			Description: "Bitable app token",
			Required:    true,
		},
		{
			Name:        "table_id",
			Type:        "string",
			Description: "Table ID",
			Required:    true,
		},
		{
			Name:        "filter",
			Type:        "object",
			Description: "Search filter for search action (JSON object with conjunction, conditions)",
			Required:    false,
		},
		{
			Name:        "fields",
			Type:        "object",
			Description: "Record fields for create/update (JSON object with field_name: value pairs)",
			Required:    false,
		},
		{
			Name:        "record_id",
			Type:        "string",
			Description: "Record ID for update action",
			Required:    false,
		},
	}
}

func (t *BitableRecordTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args bitableRecordArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	switch args.Action {
	case "search":
		return t.searchRecords(ctx, client, args)
	case "create":
		return t.createRecord(ctx, client, args)
	case "update":
		return t.updateRecord(ctx, client, args)
	default:
		return nil, fmt.Errorf("unknown action: %s", args.Action)
	}
}

func (t *BitableRecordTool) searchRecords(ctx *tools.ToolContext, client *Client, args bitableRecordArgs) (*tools.ToolResult, error) {
	bodyBuilder := bitablev1.NewSearchAppTableRecordReqBodyBuilder()
	if args.Filter != nil {
		// Convert filter map to FilterInfo structure
		filterJSON, _ := json.Marshal(args.Filter)
		var filter bitablev1.FilterInfo
		if err := json.Unmarshal(filterJSON, &filter); err == nil {
			bodyBuilder.Filter(&filter)
		}
	}

	req := bitablev1.NewSearchAppTableRecordReqBuilder().
		AppToken(args.AppToken).
		TableId(args.TableID).
		Body(bodyBuilder.Build()).
		Build()

	resp, err := client.Client().Bitable.AppTableRecord.Search(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("search records: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	if len(resp.Data.Items) == 0 {
		return tools.NewResultWithTips("No records found", "Try adjusting your filter criteria or use feishu_bitable_fields to check available fields."), nil
	}

	summary := fmt.Sprintf("Found %d record(s)", len(resp.Data.Items))
	detail, _ := json.MarshalIndent(resp.Data.Items, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}

func (t *BitableRecordTool) createRecord(ctx *tools.ToolContext, client *Client, args bitableRecordArgs) (*tools.ToolResult, error) {
	if args.Fields == nil {
		return nil, fmt.Errorf("fields required for create action")
	}

	req := bitablev1.NewCreateAppTableRecordReqBuilder().
		AppToken(args.AppToken).
		TableId(args.TableID).
		AppTableRecord(&bitablev1.AppTableRecord{Fields: args.Fields}).
		Build()

	resp, err := client.Client().Bitable.AppTableRecord.Create(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("create record: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	recordID := ""
	if resp.Data.Record.RecordId != nil {
		recordID = *resp.Data.Record.RecordId
	}
	summary := fmt.Sprintf("Record created with ID: %s", recordID)
	detail, _ := json.MarshalIndent(resp.Data.Record, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}

func (t *BitableRecordTool) updateRecord(ctx *tools.ToolContext, client *Client, args bitableRecordArgs) (*tools.ToolResult, error) {
	if args.RecordID == "" {
		return nil, fmt.Errorf("record_id required for update action")
	}
	if args.Fields == nil {
		return nil, fmt.Errorf("fields required for update action")
	}

	req := bitablev1.NewUpdateAppTableRecordReqBuilder().
		AppToken(args.AppToken).
		TableId(args.TableID).
		RecordId(args.RecordID).
		AppTableRecord(&bitablev1.AppTableRecord{Fields: args.Fields}).
		Build()

	resp, err := client.Client().Bitable.AppTableRecord.Update(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("update record: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	summary := fmt.Sprintf("Record updated: %s", args.RecordID)
	detail, _ := json.MarshalIndent(resp.Data.Record, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}

// BitableListTool lists all tables in a Bitable app.
type BitableListTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *BitableListTool) Name() string { return "feishu_bitable_list" }

func (t *BitableListTool) Description() string {
	return "List all tables in a Feishu Bitable app."
}

func (t *BitableListTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "app_token",
			Type:        "string",
			Description: "Bitable app token",
			Required:    true,
		},
	}
}
func (t *BitableListTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		AppToken string `json:"app_token"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	req := bitablev1.NewListAppTableReqBuilder().
		AppToken(args.AppToken).
		Build()

	resp, err := client.Client().Bitable.AppTable.List(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	var result []map[string]string
	for _, item := range resp.Data.Items {
		tableID := ""
		name := ""
		if item.TableId != nil {
			tableID = *item.TableId
		}
		if item.Name != nil {
			name = *item.Name
		}
		result = append(result, map[string]string{
			"table_id":   tableID,
			"table_name": name,
		})
	}

	summary, _ := json.MarshalIndent(result, "", "  ")
	return tools.NewResultWithTips(
		fmt.Sprintf("Tables: %s", summary),
		"Use feishu_bitable_fields to list fields in a table, then feishu_bitable_record to query or modify records.",
	), nil
}

// BatchCreateAppTableRecordTool batch creates records in a Bitable table.
type BatchCreateAppTableRecordTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *BatchCreateAppTableRecordTool) Name() string { return "feishu_bitable_batch_create" }

func (t *BatchCreateAppTableRecordTool) Description() string {
	return "Batch create records in a Feishu Bitable table (up to 500 at once)."
}

func (t *BatchCreateAppTableRecordTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "app_token",
			Type:        "string",
			Description: "Bitable app token",
			Required:    true,
		},
		{
			Name:        "table_id",
			Type:        "string",
			Description: "Table ID",
			Required:    true,
		},
		{
			Name:        "records",
			Type:        "array",
			Description: "Array of record objects, each with fields property",
			Required:    true,
			Items: &llm.ToolParamItems{
				Type:       "object",
				Properties: map[string]any{},
			},
		},
	}
}

func (t *BatchCreateAppTableRecordTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	var args struct {
		AppToken string           `json:"app_token"`
		TableID  string           `json:"table_id"`
		Records  []map[string]any `json:"records"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if len(args.Records) == 0 {
		return nil, fmt.Errorf("records required")
	}
	if len(args.Records) > 500 {
		return nil, fmt.Errorf("too many records, max 500")
	}

	client, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Convert records to AppTableRecord
	records := make([]*bitablev1.AppTableRecord, len(args.Records))
	for i, r := range args.Records {
		records[i] = &bitablev1.AppTableRecord{Fields: r}
	}

	body := &bitablev1.BatchCreateAppTableRecordReqBody{
		Records: records,
	}

	req := bitablev1.NewBatchCreateAppTableRecordReqBuilder().
		AppToken(args.AppToken).
		TableId(args.TableID).
		Body(body).
		Build()

	resp, err := client.Client().Bitable.AppTableRecord.BatchCreate(ctx.Ctx, req,
		larkcore.WithUserAccessToken(client.AccessToken()))
	if err != nil {
		return nil, fmt.Errorf("batch create records: %w", err)
	}
	if !resp.Success() {
		return nil, NewAPIError(resp.CodeError)
	}

	summary := fmt.Sprintf("Created %d records", len(resp.Data.Records))
	detail, _ := json.MarshalIndent(resp.Data.Records, "", "  ")
	return tools.NewResultWithDetail(summary, string(detail)), nil
}

// ListAllBitablesTool lists all Bitables (multidimensional tables) the user has access to.
// This tool does NOT require an app_token parameter - it lists all accessible Bitables.
type ListAllBitablesTool struct {
	FeishuToolBase
	MCP *FeishuMCP
}

func (t *ListAllBitablesTool) Name() string { return "feishu_list_all_bitables" }

func (t *ListAllBitablesTool) Description() string {
	return "List all Feishu Bitables (multidimensional tables) that you have access to."
}

func (t *ListAllBitablesTool) Parameters() []llm.ToolParam {
	// No parameters required - OAuth will be triggered if needed
	return []llm.ToolParam{}
}

func (t *ListAllBitablesTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	// This call triggers OAuth check - will return TokenNeededError if not authorized
	_, err := t.MCP.GetClient(ctx.Ctx, ctx.Channel, ctx.ChatID)
	if err != nil {
		return nil, err
	}

	// Feishu API doesn't provide a direct "list all bitables" endpoint
	// Return helpful guidance for the user
	return tools.NewResultWithTips(
		"✅ OAuth 授权成功！\n\n飞书 API 不支持直接列出所有可访问的多维表格。\n\n请提供你要访问的多维表格的 app_token（如：bascxxxxx）。",
		"Use feishu_bitable_list with the app_token to list tables, then feishu_bitable_fields and feishu_bitable_record to work with data.",
	), nil
}
