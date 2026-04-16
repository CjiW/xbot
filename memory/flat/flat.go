package flat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/storage/vectordb"
)

const (
	// memoryFileName is the core memory file (auto-injected into system prompt).
	memoryFileName = "MEMORY.md"
	// historyFileName is the event history file (appended during Memorize).
	historyFileName = "HISTORY.md"
	// maxMemoryChars limits MEMORY.md size for system prompt injection.
	maxMemoryChars = 1000
)

// FlatMemory file-based memory provider.
// Stores per-user global memory as markdown files under ~/.xbot/memory/{tenantKey}/.
// MEMORY.md: core memory (≤1000 chars, injected into system prompt)
// HISTORY.md: event timeline (appended during Memorize)
// knowledge/: personal knowledge files (read on demand)
type FlatMemory struct {
	tenantID    int64
	baseDir     string // ~/.xbot/memory/{tenantKey}/
	toolIndex   []memory.ToolIndexEntry
	toolIndexMu sync.RWMutex
}

var _ memory.MemoryProvider = (*FlatMemory)(nil)
var _ memory.ToolIndexer = (*FlatMemory)(nil)

// New creates a FlatMemory instance with file-based storage.
// baseDir is the per-tenant memory directory (e.g. ~/.xbot/memory/cli:direct/).
func New(tenantID int64, baseDir string) *FlatMemory {
	os.MkdirAll(baseDir, 0o755)
	return &FlatMemory{
		tenantID: tenantID,
		baseDir:  baseDir,
	}
}

// NewFromLegacy creates a FlatMemory with the old SQLite-based signature.
// Kept for backward compatibility during migration.
func NewFromLegacy(tenantID int64, _ *vectordb.ToolIndexService) *FlatMemory {
	home := os.Getenv("XBOT_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".xbot")
		} else {
			home = ".xbot"
		}
	}
	baseDir := filepath.Join(home, "memory", fmt.Sprintf("tenant_%d", tenantID))
	os.MkdirAll(baseDir, 0o755)
	return &FlatMemory{
		tenantID: tenantID,
		baseDir:  baseDir,
	}
}

// BaseDir returns the memory directory path.
func (m *FlatMemory) BaseDir() string {
	return m.baseDir
}

// Recall reads MEMORY.md and lists knowledge/ subdirectory files.
// Returns formatted text for system prompt injection.
func (m *FlatMemory) Recall(ctx context.Context, _ string) (string, error) {
	memoryPath := filepath.Join(m.baseDir, memoryFileName)
	content, err := os.ReadFile(memoryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read memory file: %w", err)
	}

	text := strings.TrimSpace(string(content))
	if text == "" {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("### Core Memory\n")
	if len([]rune(text)) > maxMemoryChars {
		runes := []rune(text)
		sb.WriteString(string(runes[:maxMemoryChars]))
		sb.WriteString("\n...(truncated, use memory_read for full content)")
	} else {
		sb.WriteString(text)
	}

	// List knowledge files for on-demand access
	knowledgeDir := filepath.Join(m.baseDir, "knowledge")
	entries, err := os.ReadDir(knowledgeDir)
	if err == nil && len(entries) > 0 {
		sb.WriteString("\n\n### Knowledge Files (read on demand with memory_read)\n")
		for _, e := range entries {
			if e.IsDir() {
				fmt.Fprintf(&sb, "- `%s/` (directory)\n", e.Name())
			} else {
				fmt.Fprintf(&sb, "- `%s`\n", e.Name())
			}
		}
	}

	return sb.String(), nil
}

// Memorize consolidates conversation messages into memory files.
// Uses LLM to generate history_entry, memory_update, and optional knowledge_updates.
func (m *FlatMemory) Memorize(ctx context.Context, input memory.MemorizeInput) (memory.MemorizeResult, error) {
	messages := input.Messages
	lastConsolidated := input.LastConsolidated
	archiveAll := input.ArchiveAll
	if !archiveAll {
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: true}, nil
	}

	log.WithField("tenant_id", m.tenantID).Infof("Memory consolidation (archive_all): %d messages", len(messages))

	// Format old messages as text
	var lines []string
	for _, msg := range messages {
		if msg.Content == "" {
			continue
		}
		role := strings.ToUpper(msg.Role)
		toolHint := ""
		if msg.Role == "tool" && msg.ToolName != "" {
			toolHint = fmt.Sprintf(" [tool: %s]", msg.ToolName)
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			names := make([]string, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				names[i] = tc.Name
			}
			toolHint = fmt.Sprintf(" [tools: %s]", strings.Join(names, ", "))
		}
		ts := time.Now().Format("2006-01-02 15:04")
		content := msg.Content
		if len([]rune(content)) > 500 {
			content = string([]rune(content)[:500]) + "..."
		}
		lines = append(lines, fmt.Sprintf("[%s] %s%s: %s", ts, role, toolHint, content))
	}

	if len(lines) == 0 {
		return memory.MemorizeResult{NewLastConsolidated: 0, OK: true}, nil
	}

	// Read current MEMORY.md
	currentMemory, _ := os.ReadFile(filepath.Join(m.baseDir, memoryFileName))
	memoryDisplay := string(currentMemory)
	if memoryDisplay == "" {
		memoryDisplay = "(empty)"
	}

	// Read existing knowledge files for context
	var knowledgeCtx strings.Builder
	knowledgeDir := filepath.Join(m.baseDir, "knowledge")
	if entries, err := os.ReadDir(knowledgeDir); err == nil && len(entries) > 0 {
		knowledgeCtx.WriteString("\n\n## Existing Knowledge Files in memory/knowledge/\n")
		for _, e := range entries {
			if e.IsDir() {
				fmt.Fprintf(&knowledgeCtx, "- %s/ (directory)\n", e.Name())
			} else {
				fpath := filepath.Join(knowledgeDir, e.Name())
				data, err := os.ReadFile(fpath)
				if err == nil {
					preview := string(data)
					if len([]rune(preview)) > 200 {
						preview = string([]rune(preview)[:200]) + "..."
					}
					fmt.Fprintf(&knowledgeCtx, "- %s: %.200s\n", e.Name(), preview)
				}
			}
		}
	}

	prompt := fmt.Sprintf(`Process this conversation and call the save_memory tool with your consolidation.

## Current Core Memory (MEMORY.md, keep under 1000 chars)
%s

## Existing Knowledge Files
(These are personal non-project notes. Update if new relevant information was learned.)%s

## Conversation to Process
%s

## Instructions
1. Update MEMORY.md with core facts (keep concise, ≤1000 chars, bullet points).
2. Add a history_entry summarizing key events/decisions (start with [YYYY-MM-DD HH:MM]).
3. If any new cross-project knowledge was learned (pitfalls, preferences, patterns), include knowledge_updates.
4. If existing knowledge files need updates, include them in knowledge_updates with action "update".
5. Do NOT include project-specific knowledge here — use knowledge_write tool for that during the conversation.`, memoryDisplay, knowledgeCtx.String(), strings.Join(lines, "\n"))

	resp, err := input.LLMClient.Generate(ctx, input.Model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a memory consolidation agent. Call the save_memory tool with your consolidation of the conversation."),
		llm.NewUserMessage(prompt),
	}, saveMemoryTool, "")
	if err != nil {
		log.WithError(err).Error("Memory consolidation LLM call failed")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
	}

	if !resp.HasToolCalls() {
		log.Warn("Memory consolidation: LLM did not call save_memory, skipping")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
	}

	var args saveMemoryArgs
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Arguments), &args); err != nil {
		log.WithError(err).Error("Memory consolidation: failed to parse save_memory arguments")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
	}

	// Append to HISTORY.md
	if args.HistoryEntry != "" {
		historyPath := filepath.Join(m.baseDir, historyFileName)
		f, err := os.OpenFile(historyPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.WithError(err).Error("Failed to open HISTORY.md for append")
		} else {
			fmt.Fprintf(f, "%s\n", args.HistoryEntry)
			f.Close()
		}
	}

	// Write MEMORY.md atomically (tmp + rename to prevent corruption on crash)
		if args.MemoryUpdate != "" && args.MemoryUpdate != strings.TrimSpace(string(currentMemory)) {
			memoryPath := filepath.Join(m.baseDir, memoryFileName)
			tmpPath := memoryPath + ".tmp"
			if err := os.WriteFile(tmpPath, []byte(args.MemoryUpdate), 0o644); err != nil {
				log.WithError(err).Error("Failed to write MEMORY.md temp file")
			} else if err := os.Rename(tmpPath, memoryPath); err != nil {
				log.WithError(err).Error("Failed to rename MEMORY.md temp file")
				os.Remove(tmpPath) // cleanup temp file
			}
		}

	// Process knowledge_updates
	for _, ku := range args.KnowledgeUpdates {
		if ku.Path == "" || ku.Content == "" {
			continue
		}
		if strings.Contains(ku.Path, "..") {
			log.WithField("path", ku.Path).Warn("Skipping knowledge update with path traversal")
			continue
		}
		knowledgePath := filepath.Join(m.baseDir, "knowledge", ku.Path)
		os.MkdirAll(filepath.Dir(knowledgePath), 0o755)
		switch ku.Action {
		case "append":
			f, err := os.OpenFile(knowledgePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				log.WithError(err).WithField("path", ku.Path).Error("Failed to open knowledge file for append")
			} else {
				fmt.Fprintf(f, "%s\n", ku.Content)
				f.Close()
			}
		default: // "create", "update", ""
			if err := os.WriteFile(knowledgePath, []byte(ku.Content), 0o644); err != nil {
				log.WithError(err).WithField("path", ku.Path).Error("Failed to write knowledge file")
			} else {
				log.WithField("path", ku.Path).Info("Knowledge file updated during consolidation")
			}
		}
	}

	log.WithField("tenant_id", m.tenantID).Infof("Memory consolidation done: lastConsolidated=%d", len(messages))
	return memory.MemorizeResult{NewLastConsolidated: len(messages), OK: true}, nil
}

// Close 释放资源（FlatMemory 无需清理）。
func (m *FlatMemory) Close() error {
	return nil
}

// IndexTools implements memory.ToolIndexer.
func (m *FlatMemory) IndexTools(_ context.Context, tools []memory.ToolIndexEntry) error {
	m.toolIndexMu.Lock()
	defer m.toolIndexMu.Unlock()
	m.toolIndex = make([]memory.ToolIndexEntry, len(tools))
	copy(m.toolIndex, tools)
	log.WithField("tenant_id", m.tenantID).Infof("Indexed %d tools (flat mode)", len(tools))
	return nil
}

// SearchTools implements memory.ToolIndexer.
// Flat mode uses simple text matching (substring match on name or description).
func (m *FlatMemory) SearchTools(_ context.Context, query string, topK int) ([]memory.ToolIndexEntry, error) {
	m.toolIndexMu.RLock()
	defer m.toolIndexMu.RUnlock()

	if topK <= 0 {
		topK = 5
	}

	queryLower := strings.ToLower(query)
	var matched []memory.ToolIndexEntry

	for _, tool := range m.toolIndex {
		// Score based on substring match
		nameLower := strings.ToLower(tool.Name)
		descLower := strings.ToLower(tool.Description)

		score := 0
		if strings.Contains(nameLower, queryLower) {
			score = 100
		} else if strings.Contains(queryLower, nameLower) {
			score = 80
		} else if strings.Contains(descLower, queryLower) {
			score = 60
		}

		if score > 0 {
			matched = append(matched, tool)
		}
	}

	// Sort by score descending (simple bubble sort for small lists)
	for i := 0; i < len(matched)-1; i++ {
		for j := i + 1; j < len(matched); j++ {
			// Re-score to compare
			q := strings.ToLower(query)
			scoreI := stringsScore(matched[i].Name, matched[i].Description, q)
			scoreJ := stringsScore(matched[j].Name, matched[j].Description, q)
			if scoreJ > scoreI {
				matched[i], matched[j] = matched[j], matched[i]
			}
		}
	}

	if len(matched) > topK {
		matched = matched[:topK]
	}

	return matched, nil
}

// stringsScore returns a simple relevance score
func stringsScore(name, desc, query string) int {
	nameLower := strings.ToLower(name)
	descLower := strings.ToLower(desc)
	queryLower := strings.ToLower(query)

	score := 0
	if strings.Contains(nameLower, queryLower) {
		score = 100
	} else if strings.Contains(queryLower, nameLower) {
		score = 80
	} else if strings.Contains(descLower, queryLower) {
		score = 60
	}
	return score
}

// --- save_memory tool definition ---

var saveMemoryTool = []llm.ToolDefinition{&saveMemoryToolDef{}}

type saveMemoryToolDef struct{}

func (t *saveMemoryToolDef) Name() string { return "save_memory" }
func (t *saveMemoryToolDef) Description() string {
	return "Save the memory consolidation result to persistent storage."
}
func (t *saveMemoryToolDef) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "history_entry",
			Type:        "string",
			Description: "A paragraph summarizing key events/decisions. Recommended: 50-200 chars. Start with [YYYY-MM-DD HH:MM]. Include detail useful for grep search. Keep concise.",
			Required:    true,
		},
		{
			Name:        "memory_update",
			Type:        "string",
			Description: "Full updated core memory as markdown. Recommended: 200-1000 chars. Include all existing facts plus new ones. Use bullet points. Return unchanged if nothing new.",
			Required:    true,
		},
		{
			Name:        "knowledge_updates",
			Type:        "array",
			Description: "Optional: list of knowledge file updates. Each has path (relative to knowledge/), action (create/update/append), and content.",
			Required:    false,
			Items: &llm.ToolParamItems{
				Type: "object",
				Properties: map[string]any{
					"path":    map[string]string{"type": "string", "description": "File path relative to knowledge/ directory (e.g. \"gotchas.md\")"},
					"action":  map[string]string{"type": "string", "description": "Action: \"create\" (new file), \"update\" (overwrite), \"append\" (add to end)"},
					"content": map[string]string{"type": "string", "description": "File content in markdown"},
				},
				Required: []string{"path", "content"},
			},
		},
	}
}

type saveMemoryArgs struct {
	HistoryEntry     string            `json:"history_entry"`
	MemoryUpdate     string            `json:"memory_update"`
	KnowledgeUpdates []knowledgeUpdate `json:"knowledge_updates,omitempty"`
}

type knowledgeUpdate struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Content string `json:"content"`
}
