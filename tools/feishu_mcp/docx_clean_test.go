package feishu_mcp

import (
	"encoding/json"
	"fmt"
	"testing"

	docxv1 "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
)

// TestCleanBlocksForInsertion tests that blocks are properly cleaned for insertion
func TestCleanBlocksForInsertion(t *testing.T) {
	// Create a sample block as returned by Convert API
	blockType := 2 // Text block
	blockID := "temp-block-id-123"
	parentID := ""

	block := &docxv1.Block{
		BlockId:   &blockID,
		ParentId:  &parentID,
		BlockType: &blockType,
		Text: &docxv1.Text{
			Elements: []*docxv1.TextElement{
				{
					TextRun: &docxv1.TextRun{
						Content: strPtr("Hello World"),
					},
				},
			},
		},
	}

	// Clean the block
	blocks := cleanBlocksForInsertion([]*docxv1.Block{block})

	// Verify block_type is preserved (REQUIRED by API)
	if blocks[0].BlockType == nil || *blocks[0].BlockType != 2 {
		t.Errorf("block_type should be preserved, got: %v", blocks[0].BlockType)
	}

	// Verify block_id is removed (API generates new IDs)
	if blocks[0].BlockId != nil {
		t.Errorf("block_id should be nil, got: %s", *blocks[0].BlockId)
	}

	// Verify text content is preserved
	if blocks[0].Text == nil || len(blocks[0].Text.Elements) != 1 {
		t.Errorf("text elements should be preserved")
	}
}

// TestCleanTableBlock tests that table blocks have merge_info removed
func TestCleanTableBlock(t *testing.T) {
	blockType := 31 // Table block
	blockID := "table-block-id"

	// Create a table block with merge_info
	block := &docxv1.Block{
		BlockId:   &blockID,
		BlockType: &blockType,
		Table: &docxv1.Table{
			Cells: []string{"cell1", "cell2"},
			Property: &docxv1.TableProperty{
				RowSize:    intPtr(2),
				ColumnSize: intPtr(2),
				MergeInfo: []*docxv1.TableMergeInfo{
					{RowSpan: intPtr(1), ColSpan: intPtr(1)},
				},
			},
		},
	}

	// Clean the block
	cleanBlocksForInsertion([]*docxv1.Block{block})

	// Verify block_type is preserved
	if block.BlockType == nil || *block.BlockType != 31 {
		t.Errorf("block_type should be preserved for table, got: %v", block.BlockType)
	}

	// Verify block_id is removed
	if block.BlockId != nil {
		t.Errorf("block_id should be nil, got: %s", *block.BlockId)
	}

	// Verify merge_info is removed (read-only field)
	if block.Table.Property.MergeInfo != nil {
		t.Errorf("merge_info should be nil, got: %v", block.Table.Property.MergeInfo)
	}

	// Verify other table properties are preserved
	if block.Table.Property.RowSize == nil || *block.Table.Property.RowSize != 2 {
		t.Errorf("row_size should be preserved")
	}
}

// TestCleanBlockJSON tests the JSON output of cleaned blocks
func TestCleanBlockJSON(t *testing.T) {
	blockType := 3 // Heading1 block
	blockID := "heading-block-id"

	block := &docxv1.Block{
		BlockId:   &blockID,
		BlockType: &blockType,
		Heading1: &docxv1.Text{
			Elements: []*docxv1.TextElement{
				{
					TextRun: &docxv1.TextRun{
						Content: strPtr("Test Heading"),
					},
				},
			},
		},
	}

	blocks := cleanBlocksForInsertion([]*docxv1.Block{block})

	// Serialize to JSON to verify structure
	jsonBytes, err := json.Marshal(blocks[0])
	if err != nil {
		t.Fatalf("Failed to marshal block: %v", err)
	}

	jsonStr := string(jsonBytes)

	// Verify block_type is in JSON (required)
	if !contains(jsonStr, `"block_type":3`) {
		t.Errorf("JSON should contain block_type:3, got: %s", jsonStr)
	}

	// Verify block_id is NOT in JSON
	if contains(jsonStr, `"block_id"`) {
		t.Errorf("JSON should not contain block_id, got: %s", jsonStr)
	}
}

// TestCleanBlockWithMentionDoc tests that mention_doc.title is removed
func TestCleanBlockWithMentionDoc(t *testing.T) {
	blockType := 2 // Text block
	blockID := "text-block-id"
	title := "Document Title"

	block := &docxv1.Block{
		BlockId:   &blockID,
		BlockType: &blockType,
		Text: &docxv1.Text{
			Elements: []*docxv1.TextElement{
				{
					TextRun: &docxv1.TextRun{
						Content: strPtr("Hello "),
					},
				},
				{
					MentionDoc: &docxv1.MentionDoc{
						Title: &title,
					},
				},
			},
		},
	}

	cleanBlocksForInsertion([]*docxv1.Block{block})

	// Verify block_id is removed
	if block.BlockId != nil {
		t.Errorf("block_id should be nil")
	}

	// Verify mention_doc.title is removed (read-only field)
	if block.Text.Elements[1].MentionDoc != nil && block.Text.Elements[1].MentionDoc.Title != nil {
		t.Errorf("mention_doc.title should be nil, got: %s", *block.Text.Elements[1].MentionDoc.Title)
	}
}

// TestCleanBlockPreservesParentID tests that parent_id is preserved for nested structure
func TestCleanBlockPreservesParentID(t *testing.T) {
	blockType := 2
	blockID := "child-block-id"
	parentID := "parent-block-id"

	block := &docxv1.Block{
		BlockId:   &blockID,
		ParentId:  &parentID,
		BlockType: &blockType,
		Text: &docxv1.Text{
			Elements: []*docxv1.TextElement{
				{
					TextRun: &docxv1.TextRun{
						Content: strPtr("Child content"),
					},
				},
			},
		},
	}

	cleanBlocksForInsertion([]*docxv1.Block{block})

	// Verify parent_id is preserved
	if block.ParentId == nil || *block.ParentId != parentID {
		t.Errorf("parent_id should be preserved, got: %v", block.ParentId)
	}

	// Verify block_id is still removed
	if block.BlockId != nil {
		t.Errorf("block_id should be nil")
	}
}

// TestCleanMultipleBlockTypes tests cleaning various block types
func TestCleanMultipleBlockTypes(t *testing.T) {
	tests := []struct {
		name      string
		blockType int
		block     *docxv1.Block
	}{
		{
			name:      "Bullet block",
			blockType: 12,
			block: &docxv1.Block{
				BlockId:   strPtr("bullet-id"),
				BlockType: intPtr(12),
				Bullet: &docxv1.Text{
					Elements: []*docxv1.TextElement{
						{TextRun: &docxv1.TextRun{Content: strPtr("Bullet item")}},
					},
				},
			},
		},
		{
			name:      "Ordered block",
			blockType: 13,
			block: &docxv1.Block{
				BlockId:   strPtr("ordered-id"),
				BlockType: intPtr(13),
				Ordered: &docxv1.Text{
					Elements: []*docxv1.TextElement{
						{TextRun: &docxv1.TextRun{Content: strPtr("Ordered item")}},
					},
				},
			},
		},
		{
			name:      "Code block",
			blockType: 14,
			block: &docxv1.Block{
				BlockId:   strPtr("code-id"),
				BlockType: intPtr(14),
				Code: &docxv1.Text{
					Elements: []*docxv1.TextElement{
						{TextRun: &docxv1.TextRun{Content: strPtr("code here")}},
					},
				},
			},
		},
		{
			name:      "Quote block",
			blockType: 15,
			block: &docxv1.Block{
				BlockId:   strPtr("quote-id"),
				BlockType: intPtr(15),
				Quote: &docxv1.Text{
					Elements: []*docxv1.TextElement{
						{TextRun: &docxv1.TextRun{Content: strPtr("Quote text")}},
					},
				},
			},
		},
		{
			name:      "Todo block",
			blockType: 17,
			block: &docxv1.Block{
				BlockId:   strPtr("todo-id"),
				BlockType: intPtr(17),
				Todo: &docxv1.Text{
					Elements: []*docxv1.TextElement{
						{TextRun: &docxv1.TextRun{Content: strPtr("Todo item")}},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanBlocksForInsertion([]*docxv1.Block{tt.block})

			// Verify block_type is preserved
			if tt.block.BlockType == nil || *tt.block.BlockType != tt.blockType {
				t.Errorf("block_type should be preserved for %s, got: %v", tt.name, tt.block.BlockType)
			}

			// Verify block_id is removed
			if tt.block.BlockId != nil {
				t.Errorf("block_id should be nil for %s", tt.name)
			}
		})
	}
}

// TestCleanAllHeadingTypes tests that all heading types are properly cleaned
func TestCleanAllHeadingTypes(t *testing.T) {
	headingTypes := []struct {
		level     int
		blockType int
	}{
		{1, 3}, {2, 4}, {3, 5}, {4, 6}, {5, 7}, {6, 8}, {7, 9}, {8, 10}, {9, 11},
	}

	for _, ht := range headingTypes {
		t.Run(fmt.Sprintf("Heading%d", ht.level), func(t *testing.T) {
			blockID := fmt.Sprintf("heading%d-id", ht.level)
			content := fmt.Sprintf("Heading %d", ht.level)

			block := &docxv1.Block{
				BlockId:   &blockID,
				BlockType: &ht.blockType,
			}

			// Set the appropriate heading field
			text := &docxv1.Text{
				Elements: []*docxv1.TextElement{
					{TextRun: &docxv1.TextRun{Content: &content}},
				},
			}

			switch ht.level {
			case 1:
				block.Heading1 = text
			case 2:
				block.Heading2 = text
			case 3:
				block.Heading3 = text
			case 4:
				block.Heading4 = text
			case 5:
				block.Heading5 = text
			case 6:
				block.Heading6 = text
			case 7:
				block.Heading7 = text
			case 8:
				block.Heading8 = text
			case 9:
				block.Heading9 = text
			}

			cleanBlocksForInsertion([]*docxv1.Block{block})

			if block.BlockType == nil || *block.BlockType != ht.blockType {
				t.Errorf("block_type should be preserved for Heading%d", ht.level)
			}
			if block.BlockId != nil {
				t.Errorf("block_id should be nil for Heading%d", ht.level)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ========== Tests for cleanBlockForDescendant ==========

// TestCleanBlockForDescendant_KeepsBlockID tests that block_id is preserved
func TestCleanBlockForDescendant_KeepsBlockID(t *testing.T) {
	blockType := 2
	blockID := "test-block-id-123"
	content := "Hello World"

	block := &docxv1.Block{
		BlockId:   &blockID,
		BlockType: &blockType,
		Text: &docxv1.Text{
			Elements: []*docxv1.TextElement{
				{
					TextRun: &docxv1.TextRun{
						Content: &content,
					},
				},
			},
		},
	}

	cleanBlockForDescendant(block)

	// block_id should be KEPT for Descendant API
	if block.BlockId == nil || *block.BlockId != blockID {
		t.Errorf("block_id should be preserved, got: %v", block.BlockId)
	}

	// parent_id should be removed
	if block.ParentId != nil {
		t.Errorf("parent_id should be nil, got: %v", block.ParentId)
	}
}

// TestCleanBlockForDescendant_TableMergeInfo tests that table merge_info is removed
func TestCleanBlockForDescendant_TableMergeInfo(t *testing.T) {
	blockType := 31 // Table
	blockID := "table-block-id"

	block := &docxv1.Block{
		BlockId:   &blockID,
		BlockType: &blockType,
		Table: &docxv1.Table{
			Cells: []string{"cell1", "cell2"},
			Property: &docxv1.TableProperty{
				RowSize:    intPtr(2),
				ColumnSize: intPtr(2),
				MergeInfo: []*docxv1.TableMergeInfo{
					{RowSpan: intPtr(1), ColSpan: intPtr(1)},
				},
			},
		},
	}

	cleanBlockForDescendant(block)

	// block_id should be kept
	if block.BlockId == nil {
		t.Errorf("block_id should be kept")
	}

	// merge_info should be removed
	if block.Table.Property.MergeInfo != nil {
		t.Errorf("merge_info should be nil, got: %v", block.Table.Property.MergeInfo)
	}

	// Other table properties should be preserved
	if block.Table.Property.RowSize == nil || *block.Table.Property.RowSize != 2 {
		t.Errorf("row_size should be preserved")
	}
}

// TestCleanBlockForDescendant_TableCellsRemoved tests that table.cells is removed (read-only field)
func TestCleanBlockForDescendant_TableCellsRemoved(t *testing.T) {
	blockType := 31 // Table
	blockID := "table-block-id"

	block := &docxv1.Block{
		BlockId:   &blockID,
		BlockType: &blockType,
		Children:  []string{"cell1", "cell2"}, // children should be preserved
		Table: &docxv1.Table{
			Cells: []string{"cell1", "cell2"}, // read-only field, should be removed
			Property: &docxv1.TableProperty{
				RowSize:    intPtr(2),
				ColumnSize: intPtr(2),
			},
		},
	}

	cleanBlockForDescendant(block)

	// table.cells should be removed (read-only field)
	if block.Table.Cells != nil {
		t.Errorf("table.cells should be nil (read-only field), got: %v", block.Table.Cells)
	}

	// children should be preserved
	if len(block.Children) != 2 {
		t.Errorf("children should be preserved, got: %v", block.Children)
	}

	// block_id should be kept
	if block.BlockId == nil || *block.BlockId != blockID {
		t.Errorf("block_id should be preserved")
	}

	// property should be preserved
	if block.Table.Property.RowSize == nil || *block.Table.Property.RowSize != 2 {
		t.Errorf("row_size should be preserved")
	}
}

// TestCleanBlockForDescendant_TableJSON verifies the JSON structure is flat (no cells field)
func TestCleanBlockForDescendant_TableJSON(t *testing.T) {
	blockType := 31 // Table
	blockID := "table-block-id"

	block := &docxv1.Block{
		BlockId:   &blockID,
		BlockType: &blockType,
		Children:  []string{"cell1", "cell2"},
		Table: &docxv1.Table{
			Cells: []string{"cell1", "cell2"}, // should be removed
			Property: &docxv1.TableProperty{
				RowSize:    intPtr(2),
				ColumnSize: intPtr(2),
			},
		},
	}

	cleanBlockForDescendant(block)

	// Serialize to JSON to verify structure
	jsonBytes, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("Failed to marshal block: %v", err)
	}

	jsonStr := string(jsonBytes)

	// Verify cells field is NOT in JSON
	if contains(jsonStr, `"cells"`) {
		t.Errorf("JSON should not contain 'cells' field (read-only), got: %s", jsonStr)
	}

	// Verify children field IS in JSON
	if !contains(jsonStr, `"children"`) {
		t.Errorf("JSON should contain 'children' field, got: %s", jsonStr)
	}

	// Verify block_type is in JSON (required)
	if !contains(jsonStr, `"block_type":31`) {
		t.Errorf("JSON should contain block_type:31, got: %s", jsonStr)
	}
}

// TestCleanBlockForDescendant_KeepsChildren tests that children are preserved
func TestCleanBlockForDescendant_KeepsChildren(t *testing.T) {
	blockType := 31 // Table
	blockID := "table-block-id"
	parentID := "parent-id"
	children := []string{"child1", "child2", "child3"}

	block := &docxv1.Block{
		BlockId:   &blockID,
		ParentId:  &parentID,
		Children:  children,
		BlockType: &blockType,
		Table: &docxv1.Table{
			Property: &docxv1.TableProperty{},
		},
	}

	cleanBlockForDescendant(block)

	// block_id should be kept
	if block.BlockId == nil || *block.BlockId != blockID {
		t.Errorf("block_id should be preserved")
	}

	// children should be kept
	if len(block.Children) != 3 {
		t.Errorf("children should be preserved, got %d children", len(block.Children))
	}

	// parent_id should be removed
	if block.ParentId != nil {
		t.Errorf("parent_id should be nil")
	}
}

// TestCleanBlockForDescendant_MentionDoc tests that mention_doc.title is removed
func TestCleanBlockForDescendant_MentionDoc(t *testing.T) {
	blockType := 2
	blockID := "text-block-id"
	title := "Document Title"

	block := &docxv1.Block{
		BlockId:   &blockID,
		BlockType: &blockType,
		Text: &docxv1.Text{
			Elements: []*docxv1.TextElement{
				{
					TextRun: &docxv1.TextRun{
						Content: strPtr("Hello "),
					},
				},
				{
					MentionDoc: &docxv1.MentionDoc{
						Title: &title,
					},
				},
			},
		},
	}

	cleanBlockForDescendant(block)

	// block_id should be kept
	if block.BlockId == nil {
		t.Errorf("block_id should be kept")
	}

	// mention_doc.title should be removed (read-only)
	if block.Text.Elements[1].MentionDoc != nil && block.Text.Elements[1].MentionDoc.Title != nil {
		t.Errorf("mention_doc.title should be nil")
	}
}

// ========== Tests for findRootBlockIDs ==========

// TestFindRootBlockIDs_Basic tests finding root blocks
func TestFindRootBlockIDs_Basic(t *testing.T) {
	textType := 2
	headingType := 3
	tableType := 31
	tableCellType := 32

	blocks := []*docxv1.Block{
		{
			BlockId:   strPtr("text-1"),
			BlockType: &textType,
		},
		{
			BlockId:   strPtr("heading-1"),
			BlockType: &headingType,
		},
		{
			BlockId:   strPtr("table-1"),
			BlockType: &tableType,
		},
		{
			BlockId:   strPtr("cell-1"),
			BlockType: &tableCellType, // NOT a root block
		},
	}

	rootIDs := findRootBlockIDs(blocks)

	// Should have 3 root blocks (text, heading, table)
	if len(rootIDs) != 3 {
		t.Errorf("expected 3 root blocks, got %d: %v", len(rootIDs), rootIDs)
	}

	// Verify the root IDs
	expectedIDs := map[string]bool{
		"text-1":    true,
		"heading-1": true,
		"table-1":   true,
	}
	for _, id := range rootIDs {
		if !expectedIDs[id] {
			t.Errorf("unexpected root ID: %s", id)
		}
	}
}

// TestFindRootBlockIDs_AllBlockTypes tests various block types
func TestFindRootBlockIDs_AllBlockTypes(t *testing.T) {
	tests := []struct {
		name      string
		blockType int
		isRoot    bool
	}{
		{"Page", 1, true},
		{"Text", 2, true},
		{"Heading1", 3, true},
		{"Heading2", 4, true},
		{"Bullet", 12, true},
		{"Ordered", 13, true},
		{"Code", 14, true},
		{"Quote", 15, true},
		{"Todo", 17, true},
		{"Divider", 18, true},
		{"Image", 19, true},
		{"Table", 31, true},
		{"TableCell", 32, false},  // NOT a root block
		{"GridColumn", 24, false}, // NOT a root block
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := []*docxv1.Block{
				{
					BlockId:   strPtr("block-" + tt.name),
					BlockType: &tt.blockType,
				},
			}

			rootIDs := findRootBlockIDs(blocks)

			if tt.isRoot {
				if len(rootIDs) != 1 {
					t.Errorf("%s (type %d) should be a root block", tt.name, tt.blockType)
				}
			} else {
				if len(rootIDs) != 0 {
					t.Errorf("%s (type %d) should NOT be a root block, but got: %v", tt.name, tt.blockType, rootIDs)
				}
			}
		})
	}
}

// TestFindRootBlockIDs_NilBlockID tests handling nil block_id
func TestFindRootBlockIDs_NilBlockID(t *testing.T) {
	textType := 2

	blocks := []*docxv1.Block{
		{
			BlockId:   nil, // nil block_id
			BlockType: &textType,
		},
		{
			BlockId:   strPtr("valid-block"),
			BlockType: &textType,
		},
	}

	rootIDs := findRootBlockIDs(blocks)

	// Should only have 1 root block (the valid one)
	if len(rootIDs) != 1 {
		t.Errorf("expected 1 root block, got %d", len(rootIDs))
	}
	if len(rootIDs) > 0 && rootIDs[0] != "valid-block" {
		t.Errorf("expected 'valid-block', got %s", rootIDs[0])
	}
}

// TestFindRootBlockIDs_MixedHierarchy tests a realistic table hierarchy
func TestFindRootBlockIDs_MixedHierarchy(t *testing.T) {
	tableType := 31
	tableCellType := 32
	textType := 2

	// Simulates: Table -> TableCells -> Text
	// The children array defines the parent-child relationships
	blocks := []*docxv1.Block{
		{
			BlockId:   strPtr("table-1"),
			BlockType: &tableType,
			Children:  []string{"cell-1", "cell-2"}, // table has cells as children
		},
		{
			BlockId:   strPtr("cell-1"),
			BlockType: &tableCellType,
			Children:  []string{"text-1"}, // cell has text as child
		},
		{
			BlockId:   strPtr("cell-2"),
			BlockType: &tableCellType,
			Children:  []string{"text-2"}, // cell has text as child
		},
		{
			BlockId:   strPtr("text-1"),
			BlockType: &textType, // Content inside cell (child of cell-1)
		},
		{
			BlockId:   strPtr("text-2"),
			BlockType: &textType, // Content inside cell (child of cell-2)
		},
	}

	rootIDs := findRootBlockIDs(blocks)

	// Should have 1 root block: table-1
	// - cell-1, cell-2 are children of table-1 (not roots, also structural blocks)
	// - text-1, text-2 are children of cells (not roots)
	if len(rootIDs) != 1 {
		t.Errorf("expected 1 root block (table), got %d: %v", len(rootIDs), rootIDs)
	}

	// Verify the only root ID is table-1
	if len(rootIDs) > 0 && rootIDs[0] != "table-1" {
		t.Errorf("expected root ID 'table-1', got: %v", rootIDs)
	}
}
