package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
)

// OffloadConfig 配置大 tool result 的 offload 行为。
type OffloadConfig struct {
	MaxResultTokens int    // 触发 offload 的 token 阈值（默认 2000）
	MaxResultBytes  int    // 触发 offload 的字节阈值（默认 10240）
	StoreDir        string // offload 文件存储根目录
	CleanupAgeDays  int    // 过期清理天数（默认 7）
}

// OffloadedResult 表示一个已被 offload 的工具结果元数据。
type OffloadedResult struct {
	ID        string    `json:"id"`
	ToolName  string    `json:"tool_name"`
	Args      string    `json:"args"`
	FilePath  string    `json:"file_path"`
	TokenSize int       `json:"token_size"`
	Timestamp time.Time `json:"timestamp"`
	Summary   string    `json:"summary"`
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
	sessions sync.Map // map[sessionKey]*offloadIndex
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
	return &OffloadStore{config: config}
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
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '\x00' {
			return '_'
		}
		return r
	}, sessionKey)
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

// estimateTokenSize 粗略估算文本的 token 数（约 4 字符/token）。
func estimateTokenSize(text string) int {
	// 简单估算：英文 ~4 字符/token，中文 ~1.5 字符/token
	// 使用折中方式：2.5 字符/token
	return len(text) * 2 / 5
}

// MaybeOffload 检测 tool result 是否超过阈值，超过则 offload 到磁盘。
// 返回 (OffloadedResult, true) 表示已 offload，content 应替换为 result.Summary。
// 返回 (zero, false) 表示无需 offload。
func (s *OffloadStore) MaybeOffload(sessionKey, toolName, args, result string) (OffloadedResult, bool) {
	if result == "" {
		return OffloadedResult{}, false
	}

	// 检查是否超过阈值
	tokenSize := estimateTokenSize(result)
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
	// 先从内存索引中查找，确认 ID 属于该 session
	idx := s.getOrCreateIndex(sessionKey)
	idx.mu.RLock()
	var found bool
	for _, e := range idx.entries {
		if e.ID == id {
			found = true
			break
		}
	}
	idx.mu.RUnlock()

	if !found {
		return "", fmt.Errorf("offload ID %s not found in session %s", id, sessionKey)
	}

	// 读取文件
	sessionDir := s.getSessionDir(sessionKey)
	fp := s.offloadFilePath(sessionDir, id)
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

	return sb.String()
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
		return fmt.Sprintf("Content: %s\n(Size: %d bytes, ~%d tokens)", content, len(content), estimateTokenSize(content))
	}

	preview := string(runes[:maxPreview])
	tokens := estimateTokenSize(content)
	return fmt.Sprintf("Content (first %d chars): %s...\n(Size: %d bytes, ~%d tokens)", maxPreview, preview, len(content), tokens)
}

// extractJSONStringField 从 JSON 字符串中提取指定字符串字段的值。
func extractJSONStringField(jsonStr, field string) string {
	// 简单 JSON 字段提取，避免完整解析开销
	key := `"` + field + `"`
	idx := strings.Index(jsonStr, key)
	if idx == -1 {
		return ""
	}

	// 找到冒号后的值
	rest := jsonStr[idx+len(key):]
	colonIdx := strings.Index(rest, ":")
	if colonIdx == -1 {
		return ""
	}
	rest = rest[colonIdx+1:]

	// 跳过空格
	rest = strings.TrimSpace(rest)
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]

	// 找到结束引号
	endIdx := 0
	for endIdx < len(rest) {
		if rest[endIdx] == '"' && (endIdx == 0 || rest[endIdx-1] != '\\') {
			break
		}
		endIdx++
	}

	return rest[:endIdx]
}

// extractFunctionNames 从代码内容中提取函数名（Go, Python, JS 等常见模式）。
func extractFunctionNames(content string) []string {
	// 简单正则匹配常见函数声明
	patterns := []string{
		"func ",
		"function ",
		"def ",
		"fn ",
	}

	var names []string
	lines := strings.Split(content, "\n")
	seen := make(map[string]bool)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		for _, pat := range patterns {
			if strings.HasPrefix(line, pat) {
				// 提取函数名
				rest := line[len(pat):]
				// 跳过可能的接收者（Go）
				if pat == "func " && strings.HasPrefix(rest, "(") {
					closeParen := strings.Index(rest, ")")
					if closeParen >= 0 {
						rest = strings.TrimSpace(rest[closeParen+1:])
					}
				}
				// 取到第一个括号或空格
				end := strings.IndexAny(rest, "( \t\n{")
				if end > 0 {
					name := rest[:end]
					if name != "" && !seen[name] {
						seen[name] = true
						names = append(names, name)
					}
				}
				break
			}
		}
	}

	return names
}
