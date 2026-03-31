package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

func init() {
	if err := godotenv.Load(".env"); err != nil {
		slog.Debug("failed to load .env file, using environment variables only", "error", err)
	}
}

// OAuthConfig OAuth 配置
type OAuthConfig struct {
	Enable  bool   // 是否启用 OAuth 功能
	Host    string // OAuth 服务监听地址（默认 127.0.0.1，仅本地访问，避免暴露到所有网络接口）
	Port    int    // OAuth 服务监听端口（默认 8081）
	BaseURL string // OAuth 回调基础 URL (e.g., https://your-domain.com)
}

// SandboxConfig 沙箱配置
type SandboxConfig struct {
	Mode        string        // 沙箱模式: "none", "docker", "remote"
	RemoteMode  string        // 启用 remote 沙箱（同时保留 docker）: "remote" 表示同时启用
	DockerImage string        // Docker 镜像（如 "ubuntu:22.04"）
	HostWorkDir string        // DinD 手动覆盖：宿主机上对应 WORK_DIR 的真实路径（通常自动检测，仅在检测失败时设置）
	IdleTimeout time.Duration // 用户空闲超时，超时后自动卸载沙箱（默认 30min，设为 0 禁用）
	WSPort      int           // WebSocket 监听端口（remote 模式，默认 8080）
	AuthToken   string        // Runner 认证 token
	PublicURL   string        // 对外访问地址（用于生成 Runner 连接命令，如 "ws://example.com:8080"）
}

// QQConfig QQ 机器人渠道配置
type QQConfig struct {
	Enabled      bool
	AppID        string
	ClientSecret string
	AllowFrom    []string // 允许的 openid 列表（空则允许所有）
}

// NapCatConfig NapCat (OneBot 11) 渠道配置
type NapCatConfig struct {
	Enabled   bool
	WSUrl     string   // NapCat WebSocket URL, e.g. "ws://localhost:3001"
	Token     string   // 鉴权 token（可选）
	AllowFrom []string // 允许的 QQ 号白名单（空则允许所有）
}

// EmbeddingConfig Embedding 配置
type EmbeddingConfig struct {
	Provider  string // Embedding 提供者: "openai"(默认) 或 "ollama"
	BaseURL   string // Embedding API 基础 URL（默认回退到 LLM_BASE_URL）
	APIKey    string // Embedding API Key（默认回退到 LLM_API_KEY）
	Model     string // Embedding 模型名称（如 bge-m3、text-embedding-3-small）
	MaxTokens int    // Embedding 模型最大 token 数（默认 2048，超限时用 LLM 压缩）
}

// StartupNotifyConfig 启动通知配置
type StartupNotifyConfig struct {
	Channel string // 通知渠道: "feishu", "qq" 等，空则不发送
	ChatID  string // 通知目标 chat_id
}

// AdminConfig 管理员配置
type AdminConfig struct {
	ChatID string // 管理员会话 ID（用于 Logs 工具等敏感操作的权限控制）
}

// OSSConfig 对象存储配置
type OSSConfig struct {
	Provider       string // 存储提供者: "local" (默认) 或 "qiniu"
	QiniuAccessKey string // 七牛 AccessKey
	QiniuSecretKey string // 七牛 SecretKey
	QiniuBucket    string // 七牛空间名
	QiniuDomain    string // 七牛 CDN 域名 (e.g., "https://cdn.example.com")
	QiniuRegion    string // 七牛区域 (e.g., "z0" 华东, 默认 "z0")
}

// WebConfig Web 渠道配置
type WebConfig struct {
	Enable           bool   // 是否启用 Web 渠道
	Host             string // 监听地址（默认 0.0.0.0）
	Port             int    // 监听端口（默认 8082）
	StaticDir        string // 前端静态文件目录（可选，为空则不提供前端页面，独立部署时设置）
	UploadDir        string // 文件上传目录（可选，默认 workspace/uploads）
	PersonaIsolation bool   // 启用后每个 web 用户使用独立 persona，不回退到全局 persona
	InviteOnly       bool   // 启用后禁止自主注册，新账号只能由 admin 通过飞书命令创建
}

// Config 应用配置
type Config struct {
	Server        ServerConfig
	LLM           LLMConfig
	Embedding     EmbeddingConfig
	Log           LogConfig
	PProf         PProfConfig
	Feishu        FeishuConfig
	QQ            QQConfig
	NapCat        NapCatConfig
	Agent         AgentConfig
	OAuth         OAuthConfig
	Sandbox       SandboxConfig
	StartupNotify StartupNotifyConfig
	Admin         AdminConfig
	Web           WebConfig
	OSS           OSSConfig
}

// FeishuConfig 飞书渠道配置
type FeishuConfig struct {
	Enabled           bool
	AppID             string
	AppSecret         string
	EncryptKey        string
	VerificationToken string
	AllowFrom         []string // 允许的 open_id 列表（空则允许所有）
	Domain            string   // 飞书域名 (e.g., "xxx.feishu.cn"，用于生成文档链接)
}

// AgentConfig Agent 配置
type AgentConfig struct {
	MaxIterations  int    // 单次对话最大工具迭代次数
	MaxConcurrency int    // 最大并发处理数（不同会话并行处理上限，默认 2）
	MemoryWindow   int    // 上下文窗口（保留最近多少条消息）
	MemoryProvider string // 记忆提供者: "flat" 或 "letta"（默认 "flat"）
	WorkDir        string // 工作目录（所有文件相对此目录存放）
	PromptFile     string // 系统提示词模板文件路径（空则使用内置默认值）
	SingleUser     bool   // 单用户模式：所有消息的 SenderID 归一化为 "default"

	// MCP 会话管理配置
	MCPInactivityTimeout time.Duration // MCP 不活跃超时时间（默认 30 分钟）
	MCPCleanupInterval   time.Duration // MCP 清理扫描间隔（默认 5 分钟）
	SessionCacheTimeout  time.Duration // 会话缓存超时（默认 24 小时）

	// 上下文压缩配置
	ContextMode          string  // 上下文管理模式（空则由 EnableAutoCompress 决定）
	EnableAutoCompress   bool    // 是否启用自动上下文压缩（默认 true）
	MaxContextTokens     int     // 最大上下文 token 数（默认 100000）
	CompressionThreshold float64 // 触发压缩的 token 比例阈值（默认 0.7，即 70% 时触发）

	PurgeOldMessages bool // 压缩后清理超出 MemoryWindow 的旧消息（默认 false）

	// SubAgent 深度控制
	MaxSubAgentDepth int // SubAgent 最大嵌套深度（默认 6）

	// LLM 重试配置
	LLMRetryAttempts int           // LLM 重试次数（默认 5）
	LLMRetryDelay    time.Duration // 初始重试延迟（默认 1s）
	LLMRetryMaxDelay time.Duration // 最大重试延迟（默认 30s）
	LLMRetryTimeout  time.Duration // 单次 LLM 调用超时（默认 120s）
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Host         string
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// LLMConfig LLM 配置
type LLMConfig struct {
	Provider string // LLM 提供商: "openai" 或 "anthropic"
	BaseURL  string
	APIKey   string
	Model    string // 默认模型（API 获取失败时的回退模型）
}

// LogConfig 日志配置
type LogConfig struct {
	Level  string // debug, info, warn, error
	Format string // text, json
}

// PProfConfig pprof 配置
type PProfConfig struct {
	Enable bool   // 是否启用 pprof
	Host   string // 监听地址
	Port   int    // 监听端口
}

// XbotHome 返回 xbot 全局目录路径（$XBOT_HOME 或 ~/.xbot）。
// 目录如果不存在会自动创建。
func XbotHome() string {
	dir := os.Getenv("XBOT_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			dir = ".xbot"
		} else {
			dir = filepath.Join(home, ".xbot")
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("failed to create xbot home directory", "path", dir, "error", err)
	}
	return dir
}

// ConfigFilePath 返回全局配置文件路径。
func ConfigFilePath() string {
	return filepath.Join(XbotHome(), "config.json")
}

// DBFilePath 返回全局数据库文件路径。
func DBFilePath() string {
	return filepath.Join(XbotHome(), "xbot.db")
}

// LoadFromFile 从 JSON 文件加载配置。只覆盖文件中存在的非零值字段。
func LoadFromFile(path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("failed to parse config file, ignoring", "path", path, "error", err)
		return nil
	}
	return &cfg
}

// applyEnvOverrides 用环境变量覆盖配置中的非空值。
// 环境变量优先级最高，文件配置作为基础值。
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("SERVER_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("SERVER_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = i
		}
	}
	// ... 其余字段同理，但为避免大量重复代码，
	// 改用更简洁的方式：只覆盖 CLI 常用的关键字段

	if v := os.Getenv("LLM_PROVIDER"); v != "" {
		cfg.LLM.Provider = v
	}
	if v := os.Getenv("LLM_BASE_URL"); v != "" {
		cfg.LLM.BaseURL = v
	}
	if v := os.Getenv("LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := os.Getenv("LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}
	if v := os.Getenv("WORK_DIR"); v != "" {
		cfg.Agent.WorkDir = v
	}
	if v := os.Getenv("PROMPT_FILE"); v != "" {
		cfg.Agent.PromptFile = v
	}
	if v := os.Getenv("MEMORY_PROVIDER"); v != "" {
		cfg.Agent.MemoryProvider = v
	}
	if v := os.Getenv("AGENT_MAX_ITERATIONS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Agent.MaxIterations = i
		}
	}
	if v := os.Getenv("AGENT_MAX_CONCURRENCY"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Agent.MaxConcurrency = i
		}
	}
	if v := os.Getenv("AGENT_MEMORY_WINDOW"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Agent.MemoryWindow = i
		}
	}
	if v := os.Getenv("SANDBOX_MODE"); v != "" {
		cfg.Sandbox.Mode = v
	}
	if v := os.Getenv("LLM_EMBEDDING_PROVIDER"); v != "" {
		cfg.Embedding.Provider = v
	}
	if v := os.Getenv("LLM_EMBEDDING_BASE_URL"); v != "" {
		cfg.Embedding.BaseURL = v
	}
	if v := os.Getenv("LLM_EMBEDDING_API_KEY"); v != "" {
		cfg.Embedding.APIKey = v
	}
	if v := os.Getenv("LLM_EMBEDDING_MODEL"); v != "" {
		cfg.Embedding.Model = v
	}
}

// Load 加载配置：先从全局 config.json 读取基础值，再用环境变量覆盖。
// 这保证了：config.json 提供持久化配置，环境变量用于临时覆盖（如 CI/Docker）。
func Load() *Config {
	cfg := LoadFromFile(ConfigFilePath())
	if cfg == nil {
		cfg = &Config{}
	}
	applyEnvOverrides(cfg)

	// 填充 CLI 常用的默认值（仅在配置和环境变量都未设置时生效）
	if cfg.LLM.Provider == "" {
		cfg.LLM.Provider = "openai"
	}
	if cfg.LLM.BaseURL == "" {
		cfg.LLM.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.LLM.Model == "" {
		cfg.LLM.Model = "gpt-4o"
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = "json"
	}
	if cfg.Agent.WorkDir == "" {
		cfg.Agent.WorkDir = "."
	}
	if cfg.Agent.PromptFile == "" {
		cfg.Agent.PromptFile = "prompt.md"
	}
	if cfg.Agent.MaxIterations == 0 {
		cfg.Agent.MaxIterations = 100
	}
	if cfg.Agent.MaxConcurrency == 0 {
		cfg.Agent.MaxConcurrency = 3
	}
	if cfg.Agent.MemoryWindow == 0 {
		cfg.Agent.MemoryWindow = 50
	}
	if cfg.Agent.MCPInactivityTimeout == 0 {
		cfg.Agent.MCPInactivityTimeout = 30 * time.Minute
	}
	if cfg.Agent.MCPCleanupInterval == 0 {
		cfg.Agent.MCPCleanupInterval = 5 * time.Minute
	}
	if cfg.Agent.SessionCacheTimeout == 0 {
		cfg.Agent.SessionCacheTimeout = 24 * time.Hour
	}
	if cfg.Agent.LLMRetryAttempts == 0 {
		cfg.Agent.LLMRetryAttempts = 5
	}
	if cfg.Agent.LLMRetryDelay == 0 {
		cfg.Agent.LLMRetryDelay = 1 * time.Second
	}
	if cfg.Agent.LLMRetryMaxDelay == 0 {
		cfg.Agent.LLMRetryMaxDelay = 30 * time.Second
	}
	if cfg.Agent.LLMRetryTimeout == 0 {
		cfg.Agent.LLMRetryTimeout = 120 * time.Second
	}
	if cfg.Sandbox.Mode == "" {
		cfg.Sandbox.Mode = "docker"
	}
	if cfg.Sandbox.IdleTimeout == 0 {
		cfg.Sandbox.IdleTimeout = 30 * time.Minute
	}
	if cfg.Embedding.MaxTokens == 0 {
		cfg.Embedding.MaxTokens = 2048
	}
	if cfg.Agent.MaxContextTokens == 0 {
		cfg.Agent.MaxContextTokens = 100000
	}
	if cfg.Agent.CompressionThreshold == 0 {
		cfg.Agent.CompressionThreshold = 0.7
	}
	if cfg.Agent.MaxSubAgentDepth == 0 {
		cfg.Agent.MaxSubAgentDepth = 6
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 120 * time.Second
	}
	if cfg.Admin.ChatID == "" {
		cfg.Admin.ChatID = getAdminChatID()
	}

	return cfg
}

// getAdminChatID 获取管理员会话 ID，实现回退逻辑
// 优先读取 ADMIN_CHAT_ID，如果为空则回退到 STARTUP_NOTIFY_CHAT_ID
func getAdminChatID() string {
	if adminChatID := os.Getenv("ADMIN_CHAT_ID"); adminChatID != "" {
		return adminChatID
	}
	return os.Getenv("STARTUP_NOTIFY_CHAT_ID")
}
