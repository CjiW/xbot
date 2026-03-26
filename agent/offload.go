package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	"xbot/tools"

	log "xbot/logger"
)

// sessionDirReplacer 用于清理 sessionKey 中的危险路径字符，声明为包级变量避免重复创建。
var sessionDirReplacer = strings.NewReplacer("/", "_", "\\", "_", ":", "_", "\x00", "_")

// extractGoStructure 使用的正则，声明为包级变量避免每次调用重复编译。
var (
	goImportRe   = regexp.MustCompile(`"([^"]+)"`)
	goTypeRe     = regexp.MustCompile(`type\s+(\w+)\s+(struct|interface|func)\b`)
	goConstVarRe = regexp.MustCompile(`^(?:const|var)\s+\(?(\w+)\s*$`)
	goFuncRe     = regexp.MustCompile(`func\s+(?:\([^)]+\)\s+)?(\w+)\s*\(([^)]*)\)`)
)

// OffloadConfig 配置大 tool result 的 offload 行为。
type OffloadConfig struct {
	MaxResultTokens int    // 触发 offload 的 token 阈值（默认 2000）
	MaxResultBytes  int    // 触发 offload 的字节阈值（默认 10240）
	StoreDir        string // offload 文件存储根目录
	CleanupAgeDays  int    // 过期清理天数（默认 7）
	Model           string // tokenizer 使用的模型（默认 "gpt-4o"）
}

// OffloadedResult 表示一个已被 offload 的工具结果元数据。
type OffloadedResult struct {
	ID          string    `json:"id"`
	ToolName    string    `json:"tool_name"`
	Args        string    `json:"args"`
	FilePath    string    `json:"file_path"`
	TokenSize   int       `json:"token_size"`
	Timestamp   time.Time `json:"timestamp"`
	Summary     string    `json:"summary"`
	ContentHash string    `json:"content_hash"` // SHA256 of content at offload time (Read only)
	ReadPath    string    `json:"read_path"`    // Resolved file path from Read tool args
	Stale       bool      `json:"stale"`        // Whether this offload is stale
}

// offloadIndex 单个 session 的 offload 索引。
type offloadIndex struct {
	mu      sync.RWMutex
	entries []OffloadedResult
}

// offloadFile 完整 tool result 的磁盘存储格式。
type offloadFile struct {
	ID        string    `json:"id"`
	ToolName  string    `json:"tool_name"`
	Args      string    `json:"args"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// OffloadStore 管理大 tool result 的 offload 和召回。
type OffloadStore struct {
	config   OffloadConfig
	sessions sync.Map      // map[sessionKey]*offloadIndex
	sandbox  tools.Sandbox // optional sandbox for file hash computation (remote mode)
}

// NewOffloadStore 创建 OffloadStore 实例，使用默认值填充零值字段。
func NewOffloadStore(config OffloadConfig) *OffloadStore {
	if config.MaxResultTokens <= 0 {
		config.MaxResultTokens = 2000
	}
	if config.MaxResultBytes <= 0 {
		config.MaxResultBytes = 10240
	}
	if config.StoreDir == "" {
		config.StoreDir = "offload_store"
	}
	if config.CleanupAgeDays <= 0 {
		config.CleanupAgeDays = 7
	}
	if config.Model == "" {
		config.Model = "gpt-4o"
	}
	return &OffloadStore{config: config}
}

// SetSandbox sets the sandbox for file hash computation (used in remote mode
// where os.ReadFile cannot access user's machine files).
func (s *OffloadStore) SetSandbox(sb tools.Sandbox) {
	s.sandbox = sb
}

// generateID 生成 offload 短 ID: "ol_" + 8位随机 hex。
func generateID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "ol_" + hex.EncodeToString(b)
}

// getSessionDir 获取指定 session 的存储目录。
func (s *OffloadStore) getSessionDir(sessionKey string) string {
	// 清理 sessionKey 中的路径分隔符，防止目录穿越
	safe := sessionDirReplacer.Replace(sessionKey)
	return filepath.Join(s.config.StoreDir, safe)
}

// getOrCreateIndex 获取或创建指定 session 的索引。
func (s *OffloadStore) getOrCreateIndex(sessionKey string) *offloadIndex {
	if v, ok := s.sessions.Load(sessionKey); ok {
		return v.(*offloadIndex)
	}
	idx := &offloadIndex{}
	actual, _ := s.sessions.LoadOrStore(sessionKey, idx)
	return actual.(*offloadIndex)
}

// indexFilePath 返回索引文件路径。
func (s *OffloadStore) indexFilePath(sessionDir string) string {
	return filepath.Join(sessionDir, "index.json")
}

// offloadFilePath 返回单个 offload 结果文件路径。
func (s *OffloadStore) offloadFilePath(sessionDir, id string) string {
	return filepath.Join(sessionDir, id+".json")
}

// estimateTokenSize 使用 llm.CountTokens 估算 token 数，error 时 fallback 到 len(text)*2/5。
func estimateTokenSize(text string, model string) int {
	n, err := llm.CountTokens(text, model)
	if err != nil {
		return len(text) * 2 / 5
	}
	return n
}

// MaybeOffload 检测 tool result 是否超过阈值，超过则 offload 到磁盘。
// 返回 (OffloadedResult, true) 表示已 offload，content 应替换为 result.Summary。
// 返回 (zero, false) 表示无需 offload。
// workspaceRoot/sandboxWorkDir 用于 Read 工具：将 ReadPath 解析为宿主机路径后
// 读取原始文件内容计算 ContentHash，确保与 InvalidateStaleReads 的比较一致。
// sandbox 用于 remote 模式下穿越沙箱读取文件计算哈希。
func (s *OffloadStore) MaybeOffload(ctx context.Context, sessionKey, toolName, args, result, workspaceRoot, sandboxWorkDir string, userID string) (OffloadedResult, bool) {
	if result == "" {
		return OffloadedResult{}, false
	}

	// Never offload recall-type tools — their results are already retrieved content
	// and offloading them would create infinite recursion (offload → recall → offload → ...)
	switch toolName {
	case "offload_recall", "recall_masked":
		return OffloadedResult{}, false
	}

	// 检查是否超过阈值
	tokenSize := estimateTokenSize(result, s.config.Model)
	byteSize := len(result)

	if tokenSize < s.config.MaxResultTokens && byteSize < s.config.MaxResultBytes {
		return OffloadedResult{}, false
	}

	// 执行 offload
	id := generateID()
	sessionDir := s.getSessionDir(sessionKey)

	// 创建目录
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		log.WithError(err).Warn("OffloadStore: failed to create session directory")
		return OffloadedResult{}, false
	}

	// 写入完整结果文件
	of := offloadFile{
		ID:        id,
		ToolName:  toolName,
		Args:      args,
		Content:   result,
		Timestamp: time.Now(),
	}
	data, err := json.MarshalIndent(of, "", "  ")
	if err != nil {
		log.WithError(err).Warn("OffloadStore: failed to marshal offload file")
		return OffloadedResult{}, false
	}
	if err := os.WriteFile(s.offloadFilePath(sessionDir, id), data, 0o644); err != nil {
		log.WithError(err).Warn("OffloadStore: failed to write offload file")
		return OffloadedResult{}, false
	}

	// 生成摘要
	summary := generateRuleSummary(toolName, args, result)
	summaryContent := fmt.Sprintf("📂 [offload:%s] %s(%s)\n%s", id, toolName, truncateOffloadArgs(args), summary)

	// 更新内存索引
	entry := OffloadedResult{
		ID:        id,
		ToolName:  toolName,
		Args:      args,
		FilePath:  s.offloadFilePath(sessionDir, id),
		TokenSize: tokenSize,
		Timestamp: time.Now(),
		Summary:   summaryContent,
	}

	// For Read tool: resolve path and hash the raw file content.
	// In remote mode, use sandbox to read file; in local mode, resolve to host path.
	// This ensures ContentHash matches what InvalidateStaleReads computes,
	// avoiding false stale when the tool result is truncated by applyLineLimit.
	if toolName == "Read" {
		if readPath := extractJSONStringField(args, "path"); readPath != "" {
			entry.ReadPath = readPath
			if s.sandbox != nil {
				if rawData, err := s.sandbox.ReadFile(ctx, readPath, userID); err == nil {
					entry.ContentHash = fmt.Sprintf("%x", sha256.Sum256(rawData))
				}
			} else {
				hostPath := resolveReadPathToHost(readPath, workspaceRoot, sandboxWorkDir)
				if rawData, err := os.ReadFile(hostPath); err == nil {
					entry.ContentHash = fmt.Sprintf("%x", sha256.Sum256(rawData))
				}
			}
		}
	}
	// This ensures ContentHash matches what InvalidateStaleReads computes,
	// avoiding false stale when the tool result is truncated by applyLineLimit.
	if toolName == "Read" {
		if readPath := extractJSONStringField(args, "path"); readPath != "" {
			entry.ReadPath = readPath
			hostPath := resolveReadPathToHost(readPath, workspaceRoot, sandboxWorkDir)
			if rawData, err := os.ReadFile(hostPath); err == nil {
				entry.ContentHash = fmt.Sprintf("%x", sha256.Sum256(rawData))
			}
		}
	}

	idx := s.getOrCreateIndex(sessionKey)
	idx.mu.Lock()
	idx.entries = append(idx.entries, entry)
	idx.mu.Unlock()

	// 持久化索引
	s.persistIndex(sessionDir, idx)

	return entry, true
}

// Recall 按 ID 召回已 offload 的完整工具结果。

func (s *OffloadStore) Recall(sessionKey, id string) (string, error) {
	sessionDir := s.getSessionDir(sessionKey)
	fp := s.offloadFilePath(sessionDir, id)
	if _, err := os.Stat(fp); err != nil {
		return "", fmt.Errorf("offload ID %s not found in session %s", id, sessionKey)
	}

	// 读取文件
	data, err := os.ReadFile(fp)
	if err != nil {
		return "", fmt.Errorf("read offload file: %w", err)
	}

	var of offloadFile
	if err := json.Unmarshal(data, &of); err != nil {
		return "", fmt.Errorf("unmarshal offload file: %w", err)
	}

	return of.Content, nil
}

// CleanSession 清理指定 session 的所有 offload 数据。
func (s *OffloadStore) CleanSession(sessionKey string) {
	// 从内存中删除
	s.sessions.Delete(sessionKey)

	// 删除磁盘文件
	sessionDir := s.getSessionDir(sessionKey)
	if err := os.RemoveAll(sessionDir); err != nil {
		log.WithError(err).WithField("session", sessionKey).Debug("OffloadStore: failed to remove session directory")
	}
}

// CleanStale 清理超过 CleanupAgeDays 的残留 offload 数据。
func (s *OffloadStore) CleanStale() {
	cutoff := time.Now().AddDate(0, 0, -s.config.CleanupAgeDays)

	entries, err := os.ReadDir(s.config.StoreDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.WithError(err).Warn("OffloadStore: failed to list store directory")
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			dir := filepath.Join(s.config.StoreDir, entry.Name())
			if err := os.RemoveAll(dir); err != nil {
				log.WithError(err).WithField("dir", dir).Debug("OffloadStore: failed to remove stale directory")
			} else {
				log.WithField("dir", dir).Info("OffloadStore: cleaned stale session directory")
			}
		}
	}
}

// persistIndex 将 session 索引持久化到磁盘。
func (s *OffloadStore) persistIndex(sessionDir string, idx *offloadIndex) {
	idx.mu.RLock()
	entries := make([]OffloadedResult, len(idx.entries))
	copy(entries, idx.entries)
	idx.mu.RUnlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		log.WithError(err).Warn("OffloadStore: failed to marshal index")
		return
	}
	if err := os.WriteFile(s.indexFilePath(sessionDir), data, 0o644); err != nil {
		log.WithError(err).Warn("OffloadStore: failed to persist index")
	}
}

// resolveReadPathToHost converts a ReadPath (from LLM args, either sandbox absolute
// or relative) to a host filesystem path so os.ReadFile can access it.
func resolveReadPathToHost(readPath, workspaceRoot, sandboxWorkDir string) string {
	resolved := readPath
	if sandboxWorkDir != "" && workspaceRoot != "" && strings.HasPrefix(resolved, sandboxWorkDir) {
		resolved = workspaceRoot + resolved[len(sandboxWorkDir):]
	}
	if !filepath.IsAbs(resolved) && workspaceRoot != "" {
		resolved = filepath.Join(workspaceRoot, resolved)
	}
	return resolved
}

// InvalidateStaleReads checks all Read offloads in a session and marks stale ones.
// Returns IDs of newly-staled entries (previously not stale).
// workspaceRoot is the host-side workspace root (e.g. /data/users/ou_xxx/workspace).
// sandboxWorkDir is the sandbox-side workspace root (e.g. /workspace).
// Uses sandbox.ReadFile in remote mode, os.ReadFile in local mode.
func (s *OffloadStore) InvalidateStaleReads(ctx context.Context, sessionKey, workspaceRoot, sandboxWorkDir string, userID string) []string {
	idx := s.getOrCreateIndex(sessionKey)
	idx.mu.Lock()

	var newlyStale []string

	for i := range idx.entries {
		e := &idx.entries[i]
		// Only check Read offloads that are not already stale
		if e.ToolName != "Read" || e.Stale || e.ContentHash == "" || e.ReadPath == "" {
			continue
		}

		var currentData []byte
		var err error

		if s.sandbox != nil {
			// Remote mode: read via sandbox (user's machine)
			currentData, err = s.sandbox.ReadFile(ctx, e.ReadPath, userID)
		} else {
			// Local mode: resolve to host path and read directly
			resolvedPath := resolveReadPathToHost(e.ReadPath, workspaceRoot, sandboxWorkDir)
			currentData, err = os.ReadFile(resolvedPath)
		}
		if err != nil {
			if os.IsNotExist(err) {
				e.Stale = true
				newlyStale = append(newlyStale, e.ID)
			}
			continue
		}

		currentHash := fmt.Sprintf("%x", sha256.Sum256(currentData))
		if currentHash != e.ContentHash {
			e.Stale = true
			newlyStale = append(newlyStale, e.ID)
		}
	}

	// Release lock before persisting (persistIndex acquires RLock internally)
	sessionDir := s.getSessionDir(sessionKey)
	idx.mu.Unlock()

	if len(newlyStale) > 0 {
		s.persistIndex(sessionDir, idx)
	}

	return newlyStale
}

// PurgeStaleMessages removes stale offload references from messages.
// For each stale offload ID, finds the corresponding tool message and replaces
// its content with a stale marker. Returns a new slice (does not modify the original).
func (s *OffloadStore) PurgeStaleMessages(sessionKey string, messages []llm.ChatMessage) []llm.ChatMessage {
	idx := s.getOrCreateIndex(sessionKey)
	idx.mu.RLock()
	staleIDs := make(map[string]bool)
	for _, e := range idx.entries {
		if e.Stale {
			staleIDs[e.ID] = true
		}
	}
	idx.mu.RUnlock()

	if len(staleIDs) == 0 {
		return messages
	}

	result := make([]llm.ChatMessage, len(messages))
	copy(result, messages)

	for i, msg := range result {
		if msg.Role != "tool" {
			continue
		}
		for staleID := range staleIDs {
			marker := fmt.Sprintf("📂 [offload:%s]", staleID)
			if strings.Contains(msg.Content, marker) {
				result[i].Content = fmt.Sprintf("⚠️ [offload:%s] STALE — 该文件已被修改，此内容已过期。请重新 Read 获取最新内容。", staleID)
				break // only replace once per message
			}
		}
	}

	return result
}

// truncateOffloadArgs 截断工具参数用于 offload 显示。
func truncateOffloadArgs(args string) string {
	if len(args) <= 80 {
		return args
	}
	return args[:80] + "..."
}

// generateRuleSummary 按工具类型生成规则摘要（同步，无 LLM 依赖）。
func generateRuleSummary(toolName, args, content string) string {
	switch toolName {
	case "Read":
		return summarizeRead(args, content)
	case "Grep":
		return summarizeGrep(content)
	case "Shell":
		return summarizeShell(content)
	case "Glob":
		return summarizeGlob(content)
	default:
		return summarizeDefault(content)
	}
}

// summarizeRead 生成 Read 工具结果的摘要。
func summarizeRead(args, content string) string {
	// 提取文件名
	path := extractJSONStringField(args, "path")
	if path == "" {
		path = "(unknown)"
	}

	lines := strings.Split(content, "\n")
	lineCount := len(lines)

	// 单行截断保护：防止极长单行（如 JSON 序列化后的内容）撑爆 summary
	// 使用 []rune 进行 UTF-8 安全截断
	const maxLineRunes = 500
	const lineTruncSuffix = "...(truncated, %d chars)"
	for i, line := range lines {
		runes := []rune(line)
		if len(runes) > maxLineRunes {
			suffix := fmt.Sprintf(lineTruncSuffix, len(runes))
			lines[i] = string(runes[:maxLineRunes]) + suffix
		}
	}

	// 提取关键函数名
	funcNames := extractFunctionNames(content)

	var sb strings.Builder
	fmt.Fprintf(&sb, "File: %s, %d lines\n", path, lineCount)

	// 首尾各 3 行
	showLines := 3
	if lineCount > showLines*2 {
		fmt.Fprintln(&sb, "--- Head ---")
		for i := 0; i < showLines; i++ {
			fmt.Fprintf(&sb, "%s\n", lines[i])
		}
		fmt.Fprintf(&sb, "  ... (%d lines omitted) ...\n", lineCount-showLines*2)
		fmt.Fprintln(&sb, "--- Tail ---")
		for i := lineCount - showLines; i < lineCount; i++ {
			fmt.Fprintf(&sb, "%s\n", lines[i])
		}
	} else {
		for _, l := range lines {
			fmt.Fprintln(&sb, l)
		}
	}

	if len(funcNames) > 0 {
		sort.Strings(funcNames)
		fmt.Fprintf(&sb, "Key functions: %s\n", strings.Join(funcNames[:min(len(funcNames), 10)], ", "))
	}

	// 对 Go 文件，额外提取结构体信息增强摘要
	if goStruct := extractGoStructure(content); goStruct != "" {
		fmt.Fprintln(&sb, "--- Structure ---")
		fmt.Fprintln(&sb, goStruct)
	}

	summary := sb.String()

	// 总量上限保护：防止 summary 本身过大（如文件行数极多但每行都接近截断长度）
	// 使用 []rune 进行 UTF-8 安全截断
	const maxSummaryRunes = 3000
	summaryRunes := []rune(summary)
	if len(summaryRunes) > maxSummaryRunes {
		summary = string(summaryRunes[:maxSummaryRunes]) + "\n...(summary truncated)"
	}

	return summary
}

// summarizeGrep 生成 Grep 工具结果的摘要。
func summarizeGrep(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	matchCount := 0
	var matches []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 匹配格式: "file:line: content" 或 "file(line): content"
		if strings.Contains(line, ":") && !strings.HasPrefix(line, "No matches") {
			matchCount++
			if len(matches) < 3 {
				matches = append(matches, line)
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Grep: %d matches\n", matchCount)
	if len(matches) > 0 {
		fmt.Fprintln(&sb, "Top matches:")
		for _, m := range matches {
			fmt.Fprintf(&sb, "  %s\n", m)
		}
	}
	return sb.String()
}

// summarizeShell 生成 Shell 工具结果的摘要。
func summarizeShell(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) == 0 {
		return "Shell: (empty output)"
	}

	// 检查退出码
	var exitCode string
	if len(lines) > 0 {
		lastLine := lines[len(lines)-1]
		if strings.HasPrefix(lastLine, "exit code:") || strings.HasPrefix(lastLine, "Exit code:") {
			exitCode = lastLine
			lines = lines[:len(lines)-1]
		}
	}

	var sb strings.Builder
	if exitCode != "" {
		fmt.Fprintf(&sb, "Shell exit: %s\n", exitCode)
	}

	// 最后 5 行输出
	showCount := min(len(lines), 5)
	if len(lines) > showCount {
		fmt.Fprintf(&sb, "  ... (%d lines omitted) ...\n", len(lines)-showCount)
	}
	for _, l := range lines[len(lines)-showCount:] {
		fmt.Fprintf(&sb, "  %s\n", l)
	}
	return sb.String()
}

// summarizeGlob 生成 Glob 工具结果的摘要。
func summarizeGlob(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	count := 0
	var files []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		count++
		if len(files) < 5 {
			files = append(files, line)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Glob: %d files matched\n", count)
	if len(files) > 0 {
		fmt.Fprintln(&sb, "Files:")
		for _, f := range files {
			fmt.Fprintf(&sb, "  %s\n", f)
		}
	}
	if count > 5 {
		fmt.Fprintf(&sb, "  ... and %d more\n", count-5)
	}
	return sb.String()
}

// summarizeDefault 生成默认摘要。
func summarizeDefault(content string) string {
	runes := []rune(content)
	maxPreview := 300
	if len(runes) <= maxPreview {
		return fmt.Sprintf("Content: %s\n(Size: %d bytes, ~%d tokens)", content, len(content), estimateTokenSize(content, "gpt-4o"))
	}

	preview := string(runes[:maxPreview])
	tokens := estimateTokenSize(content, "gpt-4o")
	return fmt.Sprintf("Content (first %d chars): %s...\n(Size: %d bytes, ~%d tokens)", maxPreview, preview, len(content), tokens)
}

// extractJSONStringField 从 JSON 字符串中提取指定字符串字段的值。
func extractJSONStringField(jsonStr, field string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// extractFunctionNames 从代码内容中提取函数名（Go, Python, JS 等）。
func extractFunctionNames(content string) []string {
	// Go: func Name( 或 func (recv) Name(
	// Python: def Name(
	// JS: function Name(
	reGoFunc := regexp.MustCompile(`func\s+\([^)]+\)\s+(\w+)\s*\(|func\s+(\w+)\s*\(`)
	rePyFunc := regexp.MustCompile(`def\s+(\w+)\s*\(`)
	reJSFunc := regexp.MustCompile(`function\s+(\w+)\s*\(`)

	seen := make(map[string]bool)
	var names []string
	addName := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}

	for _, m := range reGoFunc.FindAllStringSubmatch(content, -1) {
		if m[1] != "" {
			addName(m[1])
		}
		if m[2] != "" {
			addName(m[2])
		}
	}
	for _, m := range rePyFunc.FindAllStringSubmatch(content, -1) {
		addName(m[1])
	}
	for _, m := range reJSFunc.FindAllStringSubmatch(content, -1) {
		addName(m[1])
	}

	return names
}

// extractGoStructure 从 Go 源码中提取结构体信息（类型、接口、常量、变量）。
// 用于增强 summarizeRead 的摘要质量，帮助 LLM 理解文件骨架。
// 使用包级正则变量（goImportRe, goTypeRe, goConstVarRe, goFuncRe），
// 单次 strings.Split 遍历完成所有提取。
func extractGoStructure(content string) string {
	// 快速检测：非 Go 文件跳过
	if !strings.Contains(content, "package ") {
		return ""
	}

	lines := strings.Split(content, "\n")
	var parts []string
	inImport := false
	var imports []string
	funcCount := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// 跳过注释行
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		// package
		if strings.HasPrefix(trimmed, "package ") {
			parts = append(parts, trimmed)
			continue
		}

		// import 块
		if trimmed == "import (" {
			inImport = true
			continue
		}
		if inImport {
			if trimmed == ")" {
				inImport = false
				continue
			}
			if m := goImportRe.FindStringSubmatch(line); len(m) > 1 {
				imports = append(imports, m[1])
			}
			continue
		}
		if strings.HasPrefix(trimmed, "import ") {
			if m := goImportRe.FindStringSubmatch(line); len(m) > 1 {
				imports = append(imports, m[1])
			}
			continue
		}

		// type 定义
		if m := goTypeRe.FindStringSubmatch(trimmed); len(m) > 0 {
			parts = append(parts, fmt.Sprintf("type %s %s", m[1], m[2]))
			continue
		}

		// const/var 组名
		if m := goConstVarRe.FindStringSubmatch(trimmed); len(m) > 0 {
			parts = append(parts, trimmed)
			continue
		}

		// func 签名（截断 15 个）
		if m := goFuncRe.FindStringSubmatch(trimmed); len(m) > 0 {
			params := strings.TrimSpace(m[2])
			if params == "" {
				params = "(no params)"
			}
			parts = append(parts, fmt.Sprintf("  %s(%s)", m[1], params))
			funcCount++
			if funcCount >= 15 {
				parts = append(parts, "  ...(more functions omitted)")
				break
			}
			continue
		}
	}

	// 汇总 import 为短名列表
	if len(imports) > 0 {
		shortNames := make([]string, len(imports))
		for i, imp := range imports {
			if idx := strings.LastIndex(imp, "/"); idx >= 0 {
				shortNames[i] = imp[idx+1:]
			} else {
				shortNames[i] = imp
			}
		}
		parts = append(parts, "Imports: "+strings.Join(shortNames, ", "))
	}

	if len(parts) <= 1 {
		return "" // 只有 package 名，没有其他结构信息
	}

	return strings.Join(parts, "\n")
}
