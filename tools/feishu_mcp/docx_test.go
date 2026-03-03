//go:build ignore
package feishu_mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"xbot/tools"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	docxv1 "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
)

// TestDocxWrite tests the docx write functionality
func TestDocxWrite(t *testing.T) {
	// This test requires valid OAuth credentials
	// Run with: go test -run TestDocxWrite ./tools/feishu_mcp/
	
	// Create a mock MCP for testing
	mcp := &FeishuMCP{}
	
	tool := &DocxWriteTool{MCP: mcp}
	
	// Test parameters
	params := map[string]interface{}{
		"document_id": "test_doc_id",
		"content":     "# Test Heading\n\nThis is a test paragraph.",
	}
	
	input, _ := json.Marshal(params)
	
	result, err := tool.Execute(&tools.ToolContext{
		Ctx:     context.Background(),
		Channel: "feishu",
		ChatID:  "test_chat",
	}, string(input))
	
	if err != nil {
		t.Logf("Error (expected without real credentials): %v", err)
	}
	
	t.Logf("Result: %v", result)
}

// TestConvertMarkdown tests the markdown conversion
func TestConvertMarkdown(t *testing.T) {
	// You need to set LARK_APP_ID and LARK_APP_SECRET env vars
	// and have a valid user_access_token
	
	appID := "your_app_id"
	appSecret := "your_app_secret"
	
	client := lark.NewClient(appID, appSecret)
	
	markdown := `# Test Heading

This is a paragraph.

- Item 1
- Item 2

1. Ordered 1
2. Ordered 2

> Quote

` + "```" + `
code block
` + "```"
	
	// Step 1: Convert Markdown to blocks
	convertBody := docxv1.NewConvertDocumentReqBodyBuilder().
		ContentType("markdown").
		Content(markdown).
		Build()
	
	convertReq := docxv1.NewConvertDocumentReqBuilder().
		Body(convertBody).
		Build()
	
	convertResp, err := client.Docx.V1.Document.Convert(context.Background(), convertReq)
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	
	if !convertResp.Success() {
		t.Fatalf("Convert API error: %d - %s", convertResp.Code, convertResp.Msg)
	}
	
	t.Logf("Converted %d blocks", len(convertResp.Data.Blocks))
	t.Logf("First level block IDs: %v", convertResp.Data.FirstLevelBlockIds)
	
	// Print block structure for debugging
	for i, block := range convertResp.Data.Blocks {
		blockJSON, _ := json.MarshalIndent(block, "", "  ")
		t.Logf("Block %d:\n%s", i, string(blockJSON))
	}
}
