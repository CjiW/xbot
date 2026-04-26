package tools

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"xbot/llm"
)

// LogsSubDir is the subdirectory relative to DataDir where logs are stored.
// Can be overridden via config if needed.
// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
const LogsSubDir = ".xbot/logs"

// LogsTool 读取 xbot 日志文件的工具（仅管理员可用）
type LogsTool struct {
	adminChatID string
}

// NewLogsTool 创建 LogsTool 实例
func NewLogsTool(adminChatID string) *LogsTool {
	return &LogsTool{
		adminChatID: adminChatID,
	}
}

func (t *LogsTool) Name() string {
	return "Logs"
}

func (t *LogsTool) Description() string {
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	return `Read xbot log files from .xbot/logs directory.
Parameters (JSON):
  - action: string, "list" (list log files) or "read" (read log content)
  - file: string, log filename (for read action, optional, defaults to latest)
  - lines: number, number of lines to read from end (for read action, optional, default 100)
  - level: string, filter by log level: debug, info, warn, error (optional)
  - grep: string, filter lines containing this text (optional)
Examples:
  {"action": "list"}
  {"action": "read", "lines": 200}
  {"action": "read", "file": "xbot-2026-03-20.log", "level": "error"}
  {"action": "read", "grep": "request_id"}`
}

func (t *LogsTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: "Action: list or read", Required: true},
		{Name: "file", Type: "string", Description: "Log filename (for read action, optional, defaults to latest)", Required: false},
		{Name: "lines", Type: "integer", Description: "Number of lines to read from end (default 100)", Required: false},
		{Name: "level", Type: "string", Description: "Filter by log level: debug, info, warn, error (optional)", Required: false},
		{Name: "grep", Type: "string", Description: "Filter lines containing this text (optional)", Required: false},
	}
}

func (t *LogsTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	// 权限检查：仅管理员可用
	if ctx.ChatID != t.adminChatID {
		return nil, fmt.Errorf("permission denied: Logs tool is only available to admin")
	}

	// 解析参数
	params, err := parseToolArgs[struct {
		Action string `json:"action"`
		File   string `json:"file"`
		Lines  int    `json:"lines"`
		Level  string `json:"level"`
		Grep   string `json:"grep"`
	}](input)
	if err != nil {
		return nil, err
	}

	if params.Action == "" {
		return nil, fmt.Errorf("action is required")
	}

	// 确定日志目录
	// 使用 DataDir 而非 WorkspaceRoot，因为日志是全局的，不是用户隔离的
	logDir := filepath.Join(ctx.DataDir, LogsSubDir)

	switch params.Action {
	case "list":
		return t.listLogs(logDir)
	case "read":
		return t.readLogs(logDir, params.File, params.Lines, params.Level, params.Grep)
	default:
		return nil, fmt.Errorf("unknown action: %s (supported: list, read)", params.Action)
	}
}

// listLogs 列出所有日志文件
func (t *LogsTool) listLogs(logDir string) (*ToolResult, error) {
	files, err := t.getLogFiles(logDir)
	if err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return NewResult(fmt.Sprintf("No log files found in %s directory.", LogsSubDir)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d log file(s) in %s:\n\n", len(files), LogsSubDir)
	for i, f := range files {
		fmt.Fprintf(&sb, "%d. %s (%s)\n", i+1, f.Name, f.Size)
	}

	return NewResult(sb.String()), nil
}

// logFileInfo 日志文件信息
type logFileInfo struct {
	Name string
	Size string
	Path string
}

// getLogFiles 获取日志文件列表（按日期倒序）
func (t *LogsTool) getLogFiles(logDir string) ([]logFileInfo, error) {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read log directory: %w", err)
	}

	var files []logFileInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, logFileInfo{
			Name: entry.Name(),
			Size: formatFileSize(info.Size()),
			Path: filepath.Join(logDir, entry.Name()),
		})
	}

	// 按文件名（日期）倒序排列
	slices.SortFunc(files, func(a, b logFileInfo) int {
		return cmp.Compare(b.Name, a.Name) // newest first
	})

	return files, nil
}

// readLogs 读取日志内容
func (t *LogsTool) readLogs(logDir, filename string, lines int, level, grep string) (*ToolResult, error) {
	if lines <= 0 {
		lines = 100
	}

	// 确定日志文件路径
	var logPath string
	if filename != "" {
		logPath = filepath.Join(logDir, filename)
		// Security: prevent path traversal
		absLogPath, err := filepath.Abs(logPath)
		if err != nil {
			return nil, fmt.Errorf("invalid log path: %w", err)
		}
		absLogDir, err := filepath.Abs(logDir)
		if err != nil {
			return nil, fmt.Errorf("invalid log directory: %w", err)
		}
		if !strings.HasPrefix(absLogPath, absLogDir+string(filepath.Separator)) && absLogPath != absLogDir {
			return nil, fmt.Errorf("path traversal not allowed")
		}
	} else {
		// 默认读取最新的日志文件
		files, err := t.getLogFiles(logDir)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("no log files found")
		}
		logPath = files[0].Path
		filename = files[0].Name
	}

	// 读取最后 N 行
	contents, err := t.readLastLines(logPath, lines)
	if err != nil {
		return nil, fmt.Errorf("failed to read log file: %w", err)
	}

	// 应用过滤
	var filtered []string
	for _, line := range contents {
		// 级别过滤
		if level != "" && !t.matchLevel(line, level) {
			continue
		}
		// grep 过滤
		if grep != "" && !strings.Contains(line, grep) {
			continue
		}
		filtered = append(filtered, line)
	}

	if len(filtered) == 0 {
		return NewResult(fmt.Sprintf("No matching lines found in %s (level=%s, grep=%s)", filename, level, grep)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "📄 **%s** (showing %d of %d lines", filename, len(filtered), len(contents))
	if level != "" {
		fmt.Fprintf(&sb, ", level=%s", level)
	}
	if grep != "" {
		fmt.Fprintf(&sb, ", grep=%s", grep)
	}
	sb.WriteString(")\n\n")

	for _, line := range filtered {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return NewResult(sb.String()), nil
}

// readLastLines 读取文件最后 N 行
func (t *LogsTool) readLastLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 使用固定大小的环形缓冲区，只保留最后 N 行，避免大文件 OOM
	type ringBuffer struct {
		data  []string
		pos   int
		count int
		size  int
	}
	buf := make([]string, n)
	rb := &ringBuffer{data: buf, size: n}

	scanner := bufio.NewScanner(f)
	scanBuf := make([]byte, 0, 64*1024)
	scanner.Buffer(scanBuf, 1024*1024) // 支持 1MB 的行

	for scanner.Scan() {
		rb.data[rb.pos] = scanner.Text()
		rb.pos = (rb.pos + 1) % rb.size
		if rb.count < rb.size {
			rb.count++
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// 从环形缓冲区中提取行（按原始顺序）
	result := make([]string, rb.count)
	if rb.count < rb.size {
		copy(result, rb.data[:rb.count])
	} else {
		copy(result, rb.data[rb.pos:])
		copy(result[rb.size-rb.pos:], rb.data[:rb.pos])
	}
	return result, nil
}

// matchLevel 检查日志行是否匹配指定级别
func (t *LogsTool) matchLevel(line, level string) bool {
	level = strings.ToLower(level)

	// 尝试解析 JSON 格式日志（logrus JSONFormatter）
	var jsonLog map[string]any
	if err := json.Unmarshal([]byte(line), &jsonLog); err == nil {
		if logLevel, ok := jsonLog["level"].(string); ok {
			return strings.ToLower(logLevel) == level
		}
	}

	// 回退到文本匹配（TextFormatter）
	// 日志格式通常为：time="..." level=xxx msg="..."
	levelPrefix := fmt.Sprintf("level=%s", level)
	if strings.Contains(strings.ToLower(line), levelPrefix) {
		return true
	}

	// 另一种常见格式：WARN: xxx, ERROR: xxx
	upperLevel := strings.ToUpper(level)
	switch upperLevel {
	case "DEBUG":
		return strings.Contains(line, "DEBUG") || strings.Contains(line, "level=debug")
	case "INFO":
		return strings.Contains(line, "INFO") || strings.Contains(line, "level=info")
	case "WARN":
		return strings.Contains(line, "WARN") || strings.Contains(line, "level=warn") || strings.Contains(line, "WARNING")
	case "ERROR":
		return strings.Contains(line, "ERROR") || strings.Contains(line, "level=error") || strings.Contains(line, "ERRO")
	default:
		return false
	}
}

// formatFileSize 格式化文件大小
func formatFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// 用于类型检查
var _ Tool = (*LogsTool)(nil)

// 初始化时记录时间（用于调试）
func init() {
	_ = time.Now() // 确保 time 包被导入
}
