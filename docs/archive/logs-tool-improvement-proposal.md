# 日志系统重构 + Admin 权限控制方案

> 中书省起草 | 第 4 版（响应门下省第 3 轮封驳）

## 一、现状分析

### 1.1 日志系统现状

| 组件 | 位置 | 现状 |
|------|------|------|
| logger 包 | `logger/logger.go` | 仅封装 logrus，无文件输出 |
| 日志配置 | `config/config.go` | 只有 Level/Format，无 OutputPath |
| 启动初始化 | `main.go:setupLogger()` | 只设置格式和级别，输出到 stdout |
| 日志目录 | 无 | 未定义统一输出位置 |

**问题**：日志分散在 stdout/stderr，LLM 无法通过工具查看历史日志。

### 1.2 配置结构现状

```go
// config/config.go
type StartupNotifyConfig struct {
    Channel string // STARTUP_NOTIFY_CHANNEL
    ChatID  string // STARTUP_NOTIFY_CHAT_ID
}

type AgentConfig struct {
    WorkDir string // WORK_DIR（默认 "."）
    // ...
}
```

**问题**：无独立的 Admin 概念，启动通知 chat_id 隐式作为 admin 标识。

### 1.3 工具系统现状

```go
// tools/interface.go
type ToolContext struct {
    WorkingDir    string // Agent 工作目录（宿主机路径）
    WorkspaceRoot string // 用户可读写工作区根目录
    ChatID        string // 当前消息来源会话
    // ...
}
```

**可利用**：`WorkingDir` 可用于构建日志路径。

---

## 二、设计方案（第 2 版）

### 2.1 日志统一输出

#### 2.1.1 日志目录
- **路径**：`${WorkDir}/.xbot/logs`
- **文件命名**：`xbot-YYYY-MM-DD.log`（按日期滚动）
- **创建逻辑**：启动时自动创建目录（如不存在）

#### 2.1.2 日志滚动实现（响应门下省问题 2）

**依赖选择**：不引入第三方库，使用标准库实现简单日期滚动。

**实现方案**（响应门下省问题 1、2、3、4）：
```go
// logger/logger.go

import (
    "fmt"
    "io"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "time"
    
    log "github.com/sirupsen/logrus"
)

type SetupConfig struct {
    WorkDir  string
    Level    string
    Format   string
}

// 全局日志文件写入器（用于程序退出时关闭）
var globalRotateFile *dailyRotateFile

// dailyRotateFile 按日期滚动的日志文件写入器
// 注意：必须通过指针使用（*dailyRotateFile）以保证并发写入安全
type dailyRotateFile struct {
    dir     string
    current *os.File
    date    string // 当前文件日期 "2006-01-02"
    mu      sync.Mutex
}

// rotate 切换到新日期的日志文件（内部方法）
func (w *dailyRotateFile) rotate(date string) error {
    if w.current != nil {
        w.current.Close()
    }
    path := filepath.Join(w.dir, "xbot-"+date+".log")
    f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
    if err != nil {
        // 失败时重置状态，保持一致性（响应问题 1）
        w.current = nil
        w.date = ""
        return err
    }
    w.date = date
    w.current = f
    return nil
}

// Write 实现 io.Writer 接口
func (w *dailyRotateFile) Write(p []byte) (n int, err error) {
    w.mu.Lock()
    defer w.mu.Unlock()
    
    today := time.Now().Format("2006-01-02")
    if today != w.date || w.current == nil {
        if err := w.rotate(today); err != nil {
            // 降级：文件创建失败，返回 0 表示未写入文件
            // 日志仍会输出到 stdout（通过 MultiWriter）
            fmt.Fprintf(os.Stderr, "WARN: failed to rotate log file: %v\n", err)
            return 0, err
        }
    }
    return w.current.Write(p)
}

// Close 关闭当前日志文件
func (w *dailyRotateFile) Close() error {
    w.mu.Lock()
    defer w.mu.Unlock()
    if w.current != nil {
        err := w.current.Close()
        w.current = nil
        w.date = ""
        return err
    }
    return nil
}

// Setup 初始化日志系统
func Setup(cfg SetupConfig) error {
    // 1. 创建日志目录
    logDir := filepath.Join(cfg.WorkDir, ".xbot", "logs")
    if err := os.MkdirAll(logDir, 0755); err != nil {
        return err
    }
    
    // 2. 创建按日期滚动的写入器
    rotateFile := &dailyRotateFile{dir: logDir}
    today := time.Now().Format("2006-01-02")
    if err := rotateFile.rotate(today); err != nil {
        // 降级：文件创建失败，仅输出到 stdout（响应问题 2）
        fmt.Fprintf(os.Stderr, "WARN: failed to create log file, falling back to stdout only: %v\n", err)
        log.SetOutput(os.Stdout)
    } else {
        // 同时输出到文件和 stdout
        log.SetOutput(io.MultiWriter(os.Stdout, rotateFile))
        // 保存全局引用，供 Close 使用（响应问题 4）
        globalRotateFile = rotateFile
    }
    
    // 3. 设置格式和级别
    switch cfg.Format {
    case "json":
        log.SetFormatter(&log.JSONFormatter{})
    default:
        log.SetFormatter(&log.TextFormatter{FullTimestamp: true})
    }
    
    level, err := log.ParseLevel(cfg.Level)
    if err != nil {
        level = log.InfoLevel
    }
    log.SetLevel(level)
    
    // 4. 清理旧日志（在日志系统初始化完成后执行，响应问题 3）
    cleanupOldLogs(logDir, 7)
    
    return nil
}

// Close 关闭日志系统（供 main.go 中 defer 调用，响应问题 4）
func Close() {
    if globalRotateFile != nil {
        globalRotateFile.Close()
        globalRotateFile = nil
    }
}

// cleanupOldLogs 清理超过保留天数的日志文件（响应门下省问题 3）
func cleanupOldLogs(dir string, maxAge int) {
    entries, err := os.ReadDir(dir)
    if err != nil {
        log.WithError(err).Warn("Failed to read log directory for cleanup")
        return
    }
    
    cutoff := time.Now().AddDate(0, 0, -maxAge)
    for _, entry := range entries {
        if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
            continue
        }
        // 解析文件名中的日期：xbot-2026-03-20.log
        dateStr := strings.TrimPrefix(entry.Name(), "xbot-")
        dateStr = strings.TrimSuffix(dateStr, ".log")
        if t, err := time.Parse("2006-01-02", dateStr); err == nil {
            if t.Before(cutoff) {
                filePath := filepath.Join(dir, entry.Name())
                if err := os.Remove(filePath); err != nil {
                    log.WithError(err).WithField("file", filePath).Warn("Failed to remove old log file")
                } else {
                    log.WithField("file", filePath).Debug("Removed old log file")
                }
            }
        }
    }
}
```

**日志保留策略**（响应门下省问题 5）：
- **默认保留 7 天**
- 启动时自动清理过期日志
- 不引入额外配置项（保持简单）

#### 2.1.3 main.go 修改（响应门下省问题 3、4）

```go
// 当前签名
func setupLogger(cfg config.LogConfig)

// 修改后签名
func setupLogger(cfg config.LogConfig, workDir string) {
    if err := log.Setup(logger.SetupConfig{
        WorkDir: workDir,
        Level:   cfg.Level,
        Format:  cfg.Format,
    }); err != nil {
        log.WithError(err).Fatal("Failed to setup logger")
    }
}

// main() 中调用
func main() {
    cfg := config.Load()
    workDir := cfg.Agent.WorkDir
    
    // 配置日志（传入 workDir）
    setupLogger(cfg.Log, workDir)
    
    // 确保程序退出时关闭日志文件（响应问题 4）
    defer log.Close()
    
    // ...
}
```

### 2.2 新增内置工具 Logs

#### 2.2.1 LogsTool 结构与注入方式（响应门下省问题 1、4、5）

```go
// tools/logs.go

import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "sort"
    "strings"
    
    "xbot/llm"
)

// LogsTool 日志查看工具（仅 Admin 可用）
type LogsTool struct {
    adminChatID string // 配置中的 ADMIN_CHAT_ID（私有字段）
}

// NewLogsTool 构造函数（依赖注入）
func NewLogsTool(adminChatID string) *LogsTool {
    return &LogsTool{adminChatID: adminChatID}
}

// logsArgs 工具参数定义
type logsArgs struct {
    Action string `json:"action"`           // "list" 或 "read"
    File   string `json:"file,omitempty"`   // 日志文件名（read 时使用）
    Lines  int    `json:"lines,omitempty"`  // 读取行数，默认 100
    Level  string `json:"level,omitempty"`  // 日志级别过滤: debug/info/warn/error
    Grep   string `json:"grep,omitempty"`   // 关键词过滤
}

const maxLogLines = 1000 // 输出长度限制（响应问题 4）

func (t *LogsTool) Name() string { return "Logs" }

func (t *LogsTool) Description() string {
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

func (t *LogsTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
    // 权限检查（响应问题 5：即使 adminChatID 为空也返回明确错误）
    if t.adminChatID == "" {
        return nil, fmt.Errorf("Logs tool not configured: ADMIN_CHAT_ID is empty")
    }
    if ctx.ChatID != t.adminChatID {
        return nil, fmt.Errorf("Logs tool is restricted to admin sessions only")
    }
    
    // 构建日志目录路径（从 context 动态获取，禁止写死）
    logDir := filepath.Join(ctx.WorkingDir, ".xbot", "logs")
    
    // 解析参数
    args, err := parseToolArgs[logsArgs](input)
    if err != nil {
        return nil, err
    }
    
    switch args.Action {
    case "list":
        return t.listLogs(logDir)
    case "read":
        return t.readLogs(logDir, args)
    default:
        return nil, fmt.Errorf("invalid action: %s (expected 'list' or 'read')", args.Action)
    }
}

// listLogs 列出日志文件
func (t *LogsTool) listLogs(logDir string) (*ToolResult, error) {
    entries, err := os.ReadDir(logDir)
    if err != nil {
        return nil, fmt.Errorf("failed to read log directory: %w", err)
    }
    
    var files []string
    for _, entry := range entries {
        if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
            files = append(files, entry.Name())
        }
    }
    
    // 按日期倒序排列
    sort.Sort(sort.Reverse(sort.StringSlice(files)))
    
    var sb strings.Builder
    sb.WriteString("Log files in .xbot/logs:\n")
    for _, f := range files {
        sb.WriteString(fmt.Sprintf("  - %s\n", f))
    }
    
    return &ToolResult{Output: sb.String()}, nil
}

// readLogs 读取日志内容
func (t *LogsTool) readLogs(logDir string, args logsArgs) (*ToolResult, error) {
    // 确定要读取的文件
    filename := args.File
    if filename == "" {
        // 默认读取最新的日志文件
        files, err := t.getLogFiles(logDir)
        if err != nil {
            return nil, err
        }
        if len(files) == 0 {
            return nil, fmt.Errorf("no log files found in %s", logDir)
        }
        filename = files[0] // 已按日期倒序
    }
    
    // 注意：变量名使用 filePath 而非 filepath，避免遮蔽包名（响应问题 2）
    filePath := filepath.Join(logDir, filename)
    file, err := os.Open(filePath)
    if err != nil {
        return nil, fmt.Errorf("failed to open log file: %w", err)
    }
    defer file.Close()
    
    // 读取行数限制
    lines := args.Lines
    if lines <= 0 {
        lines = 100
    }
    if lines > maxLogLines {
        lines = maxLogLines
    }
    
    // 从文件末尾读取指定行数
    result, err := t.readLastLines(file, lines, args.Level, args.Grep)
    if err != nil {
        return nil, err
    }
    
    return &ToolResult{Output: result}, nil
}

// getLogFiles 获取日志文件列表（按日期倒序）
func (t *LogsTool) getLogFiles(logDir string) ([]string, error) {
    entries, err := os.ReadDir(logDir)
    if err != nil {
        return nil, fmt.Errorf("failed to read log directory: %w", err)
    }
    
    var files []string
    for _, entry := range entries {
        if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
            files = append(files, entry.Name())
        }
    }
    sort.Sort(sort.Reverse(sort.StringSlice(files)))
    return files, nil
}

// readLastLines 从文件末尾读取指定行数，支持级别和关键词过滤
func (t *LogsTool) readLastLines(file *os.File, maxLines int, level, grep string) (string, error) {
    // 先读取所有行
    var lines []string
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := scanner.Text()
        
        // 级别过滤
        if level != "" && !t.matchLevel(line, level) {
            continue
        }
        
        // 关键词过滤
        if grep != "" && !strings.Contains(line, grep) {
            continue
        }
        
        lines = append(lines, line)
    }
    
    if err := scanner.Err(); err != nil && err != io.EOF {
        return "", fmt.Errorf("failed to read log file: %w", err)
    }
    
    // 取最后 N 行
    start := len(lines) - maxLines
    if start < 0 {
        start = 0
    }
    
    var sb strings.Builder
    for i := start; i < len(lines); i++ {
        sb.WriteString(lines[i])
        sb.WriteString("\n")
    }
    
    return sb.String(), nil
}

// matchLevel 检查日志行是否匹配指定级别（响应问题 5：支持 Text 和 JSON 格式）
func (t *LogsTool) matchLevel(line, level string) bool {
    levelLower := strings.ToLower(level)
    
    // 1. Text 格式：time="..." level=error msg="..."
    if strings.Contains(line, "level="+levelLower) {
        return true
    }
    
    // 2. JSON 格式：{"level":"error",...}
    var jsonEntry struct {
        Level string `json:"level"`
    }
    if err := json.Unmarshal([]byte(line), &jsonEntry); err == nil {
        return strings.ToLower(jsonEntry.Level) == levelLower
    }
    
    return false
}
```

#### 2.2.2 main.go 中注册（响应门下省问题 5）

```go
func main() {
    cfg := config.Load()
    // ...
    
    // 获取 Admin ChatID（兼容回退）
    adminChatID := cfg.Admin.ChatID
    if adminChatID == "" {
        adminChatID = cfg.StartupNotify.ChatID // 回退兼容
    }
    
    // 创建 Agent
    agentLoop := agent.New(agent.Config{...})
    
    // 注册 LogsTool（始终注册，内部处理权限错误，响应问题 5）
    logsTool := tools.NewLogsTool(adminChatID)
    agentLoop.RegisterCoreTool(logsTool)
    // ...
}
```

### 2.3 Admin 权限控制

#### 2.3.1 配置结构修改（响应门下省问题 4）

```go
// config/config.go

// AdminConfig Admin 配置
type AdminConfig struct {
    ChatID string // ADMIN_CHAT_ID（优先），回退 STARTUP_NOTIFY_CHAT_ID
}

type Config struct {
    // ...
    Admin         AdminConfig         // 新增
    StartupNotify StartupNotifyConfig // 保留，用于 Channel 配置
}

func Load() *Config {
    // ...
    adminChatID := getEnvOrDefault("ADMIN_CHAT_ID", "")
    if adminChatID == "" {
        // 向后兼容：回退到 STARTUP_NOTIFY_CHAT_ID
        adminChatID = getEnvOrDefault("STARTUP_NOTIFY_CHAT_ID", "")
    }
    
    return &Config{
        // ...
        Admin: AdminConfig{
            ChatID: adminChatID,
        },
        StartupNotify: StartupNotifyConfig{
            Channel: getEnvOrDefault("STARTUP_NOTIFY_CHANNEL", ""),
            ChatID:  adminChatID, // 复用 Admin.ChatID
        },
    }
}
```

#### 2.3.2 .env.example 更新

```bash
# --- Admin 配置 ---
# Admin 会话 chat_id（用于权限控制，如 Logs 工具）
# 如果未设置，则回退到 STARTUP_NOTIFY_CHAT_ID
# ADMIN_CHAT_ID=oc_xxxx

# --- 启动通知 ---
# 启动后自动发送上线通知
# STARTUP_NOTIFY_CHANNEL=feishu
# STARTUP_NOTIFY_CHAT_ID=oc_xxxx  # 已废弃，建议使用 ADMIN_CHAT_ID
```

### 2.4 权限检查机制

```go
func (t *LogsTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
    // 严格权限检查
    if t.adminChatID == "" {
        return nil, fmt.Errorf("Logs tool not configured: ADMIN_CHAT_ID is empty")
    }
    if ctx.ChatID != t.adminChatID {
        return nil, fmt.Errorf("Logs tool is restricted to admin sessions only")
    }
    // ...
}
```

---

## 三、子任务拆解

### Task 1: 日志系统改造（兵部）
1. `logger/logger.go`：新增 `Setup(SetupConfig)` 函数
2. `main.go`：修改 `setupLogger()` 调用

### Task 2: 配置结构修改（兵部）
1. `config/config.go`：新增 `AdminConfig`，支持 `ADMIN_CHAT_ID` 环境变量
2. `.env.example`：更新配置说明

### Task 3: Logs 工具实现（兵部）
1. `tools/logs.go`：新建文件，实现 LogsTool
2. `main.go`：注册 LogsTool

### Task 4: 测试（刑部）
1. `logger_test.go`：测试日志初始化
2. `logs_test.go`：测试 Logs 工具

---

## 四、安全约束

1. **不修改 systemd 配置**
2. **不重启服务**
3. **测试使用 `go test`**

---

## 五、待审议事项（已解决）

1. ~~日志文件滚动策略（按日期 vs 按大小）~~ → **已解决**：使用标准库实现日期滚动
2. ~~日志文件保留策略（是否自动清理）~~ → **已解决**：默认保留 7 天，启动时自动清理
3. Admin 多会话支持 → **暂不实现**：当前仅支持单 Admin，后续可扩展
4. Logs 工具大内容 offload → **暂不实现**：日志内容通常不大，后续可扩展

---

## 六、影响范围

| 文件 | 修改类型 |
|------|----------|
| `logger/logger.go` | 修改 |
| `config/config.go` | 修改 |
| `main.go` | 修改 |
| `tools/logs.go` | 新建 |
| `.env.example` | 修改 |

---

**起草时间**：2026-03-20
**修订时间**：2026-03-20（第 4 版）
**起草人**：中书省
**状态**：✅ 准奏（门下省第 4 轮审议通过）

---

## 门下省审议记录

- 第 1 轮：封驳（5 个问题）
- 第 2 轮：封驳（6 个问题）
- 第 3 轮：封驳（5 个问题）
- 第 4 轮：**准奏**

**准奏理由**：
1. 第 3 轮 5 个封驳问题已全部正确修复
2. 代码逻辑完整，边界情况处理得当
3. 安全约束全部满足
4. 架构设计简洁合理

## 附录：门下省封驳响应

### 第 1 轮封驳问题与响应

| 问题 | 响应 |
|------|------|
| Logs 工具注册方式不明确 | 新增 `NewLogsTool(adminChatID)` 构造函数，在 main.go 中依赖注入 |
| 日志滚动缺失依赖 | 使用标准库实现 `dailyRotateFile`，不引入第三方库 |
| setupLogger 签名不完整 | 明确签名变更为 `setupLogger(cfg LogConfig, workDir string)` |
| ADMIN_CHAT_ID 配置结构不完整 | 新增 `AdminConfig` 结构，在 `Load()` 中实现回退逻辑 |
| 日志保留策略未定义 | 默认保留 7 天，启动时自动清理过期日志 |

### 第 2 轮封驳问题与响应

| # | 问题 | 风险等级 | 响应 |
|---|------|---------|------|
| 1 | dailyRotateFile 初始化缺陷 | 🔴 高 | 新增 `rotate()` 方法，在 `Setup()` 中预先调用初始化当天文件 |
| 2 | 日志文件创建失败无降级 | 🔴 高 | 创建失败时降级为仅 stdout 输出，打印 WARN 到 stderr |
| 3 | cleanupOldLogs 错误静默 | 🟡 中 | 增加 `log.WithError(err).Warn()` 记录警告日志 |
| 4 | Logs 工具参数不完整 | 🟡 中 | 新增 `maxLogLines=1000` 限制，`lines` 参数默认 100 |
| 5 | Admin 空值检查位置 | 🟡 中 | 始终注册 LogsTool，内部处理空 adminChatID 返回明确错误 |
| 6 | 并发写入安全性 | 🟢 低 | 已正确实现，补充注释说明 `*dailyRotateFile` 用法 |

### 第 3 轮封驳问题与响应

| # | 问题 | 风险等级 | 响应 |
|---|------|---------|------|
| 1 | rotate() 状态不一致 | 🔴 高 | 失败时重置 `current=nil` 和 `date=""`，Write 中检查 `w.current == nil` |
| 2 | 变量名遮蔽（filepath） | 🔴 高 | 重命名为 `filePath`，避免遮蔽 `filepath` 包名 |
| 3 | cleanupOldLogs 时机 | 🟡 中 | 保持当前顺序（Setup 最后执行），日志输出正常 |
| 4 | rotateFile 无法关闭 | 🟡 中 | 新增 `globalRotateFile` 全局变量 + `Close()` 函数，main.go 中 `defer log.Close()` |
| 5 | matchLevel JSON 格式 | 🟢 低 | 增加 JSON 格式解析，支持 `{"level":"error",...}` 格式 |
