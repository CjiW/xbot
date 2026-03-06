package feishu_mcp

import (
	"encoding/json"
	"strings"

	docxv1 "github.com/larksuite/oapi-sdk-go/v3/service/docx/v1"
)

// MermaidAddOnsComponentTypeID is the component_type_id for Mermaid chart AddOns blocks.
const MermaidAddOnsComponentTypeID = "blk_631fefbbae02400430b8f9f4"

// GetBlockText extracts the plain text content from a block.
// It handles all block types that contain a *Text field (page, text, headings,
// bullet, ordered, code, quote, todo, etc.) by dispatching on BlockType.
// For AddOns blocks (e.g. Mermaid charts), it extracts the data from the JSON record.
// For block types without text (e.g. image, divider, table), it returns "".
func GetBlockText(block *docxv1.Block) string {
	if block == nil || block.BlockType == nil {
		return ""
	}
	if *block.BlockType == BlockTypeAddOns {
		return GetAddOnsText(block.AddOns)
	}
	t := getBlockTextBody(block)
	if t == nil {
		return "[may have content/children, but not text-based, e.g. image, table, etc.]"
	}
	return GetTextContent(t)
}

// getBlockTextBody returns the *Text field that corresponds to the block type.
func getBlockTextBody(block *docxv1.Block) *docxv1.Text {
	switch *block.BlockType {
	case BlockTypePage:
		return block.Page
	case BlockTypeText:
		return block.Text
	case BlockTypeHeading1:
		return block.Heading1
	case BlockTypeHeading2:
		return block.Heading2
	case BlockTypeHeading3:
		return block.Heading3
	case BlockTypeHeading4:
		return block.Heading4
	case BlockTypeHeading5:
		return block.Heading5
	case BlockTypeHeading6:
		return block.Heading6
	case BlockTypeHeading7:
		return block.Heading7
	case BlockTypeHeading8:
		return block.Heading8
	case BlockTypeHeading9:
		return block.Heading9
	case BlockTypeBullet:
		return block.Bullet
	case BlockTypeOrdered:
		return block.Ordered
	case BlockTypeCode:
		return block.Code
	case BlockTypeQuote:
		return block.Quote
	case BlockTypeTodo:
		return block.Todo
	default:
		return nil
	}
}

// GetTextContent concatenates all text elements inside a Text struct into a
// single plain-text string.
func GetTextContent(t *docxv1.Text) string {
	if t == nil || len(t.Elements) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, el := range t.Elements {
		sb.WriteString(GetElementText(el))
	}
	return sb.String()
}

// GetElementText returns the plain text representation of a single TextElement.
//   - TextRun   → its Content
//   - MentionUser → "@<user_id>"
//   - MentionDoc  → doc title (or token if title is nil)
//   - Equation    → its KaTeX Content
func GetElementText(el *docxv1.TextElement) string {
	if el == nil {
		return ""
	}
	switch {
	case el.TextRun != nil:
		return derefStr(el.TextRun.Content)
	case el.MentionUser != nil:
		return "@" + derefStr(el.MentionUser.UserId)
	case el.MentionDoc != nil:
		if el.MentionDoc.Title != nil && *el.MentionDoc.Title != "" {
			return *el.MentionDoc.Title
		}
		return derefStr(el.MentionDoc.Token)
	case el.Equation != nil:
		return derefStr(el.Equation.Content)
	default:
		return ""
	}
}

// GetAddOnsText extracts text content from an AddOns block.
// For Mermaid charts, it returns the diagram source from the JSON record's "data" field.
// For other AddOns types, it returns the raw record string.
func GetAddOnsText(addOns *docxv1.AddOns) string {
	if addOns == nil || addOns.Record == nil {
		return ""
	}
	record := *addOns.Record
	// Try to extract "data" field from JSON record (used by Mermaid, etc.)
	var parsed struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(record), &parsed); err == nil && parsed.Data != "" {
		return parsed.Data
	}
	return record
}

// IsMermaidCode checks whether a code block contains Mermaid diagram syntax.
func IsMermaidCode(block *docxv1.Block) bool {
	if block == nil || block.Code == nil {
		return false
	}
	content := GetTextContent(block.Code)
	return content != "" && startsWithMermaid(content)
}

// mermaidKeywords lists all known Mermaid diagram type prefixes (lowercase).
// Longer variants (e.g. statediagram-v2) must appear before shorter ones
// so the longest match wins.
var mermaidKeywords = []string{
	// flowchart
	"flowchart",
	"graph",
	// sequence
	"sequencediagram",
	// class
	"classdiagram-v2",
	"classdiagram",
	// state
	"statediagram-v2",
	"statediagram",
	// entity-relationship
	"erdiagram",
	// gantt
	"gantt",
	// pie
	"pie",
	// user journey
	"journey",
	// git graph
	"gitgraph",
	// mindmap
	"mindmap",
	// timeline
	"timeline",
	// sankey
	"sankey-beta",
	"sankey",
	// quadrant chart
	"quadrantchart",
	// requirement diagram
	"requirementdiagram",
	// xy chart
	"xychart-beta",
	// block diagram
	"block-beta",
	// packet diagram
	"packet-beta",
	// architecture
	"architecture-beta",
	// kanban
	"kanban",
	// zenuml
	"zenuml",
	// C4 model
	"c4context",
	"c4container",
	"c4component",
	"c4dynamic",
	"c4deployment",
}

func startsWithMermaid(content string) bool {
	trimmed := strings.TrimLeft(content, " \t\n\r")
	lower := strings.ToLower(trimmed)
	for _, kw := range mermaidKeywords {
		if strings.HasPrefix(lower, kw) {
			// keyword must be followed by whitespace or end of string
			if len(trimmed) == len(kw) {
				return true
			}
			next := trimmed[len(kw)]
			if next == ' ' || next == '\n' || next == '\r' || next == '\t' {
				return true
			}
		}
	}
	return false
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
