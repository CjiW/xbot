package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

func init() {
	if err := godotenv.Load(".env"); err != nil {
		slog.Debug("failed to load .env file, using environment variables only", "error", err)
	}
}

// envParseWarned 记录哪些环境变量已经打印过格式错误警告（每个变量只警告一次）
var envParseWarned sync.Map

func warnEnvParse(key, value, defaultValue string) {
	if _, loaded := envParseWarned.LoadOrStore(key, true); loaded {
		return
	}
	slog.Warn("invalid environment variable format, using default",
		"key", key,
		"value", value,
		"default", defaultValue,
	)
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

// WebConfig Web 渠道配置
type WebConfig struct {
	Enable    bool   // 是否启用 Web 渠道
	Host      string // 监听地址（默认 0.0.0.0）
	Port      int    // 监听端口（默认 8082）
	StaticDir string // 前端静态文件目录（可选，为空则不提供前端页面，独立部署时设置）
	UploadDir string // 文件上传目录（可选，默认 workspace/uploads）
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

	// 话题分区隔离
	EnableTopicIsolation     bool    // 是否启用话题分区隔离（默认 false）
	TopicMinSegmentSize      int     // 最小话题片段大小（默认 3）
	TopicSimilarityThreshold float64 // 话题相似度阈值（默认 0.3）

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

// Load 加载配置（优先从环境变量读取）
func Load() *Config {
	return &Config{
		Server: ServerConfig{
			Host:         getEnvOrDefault("SERVER_HOST", "0.0.0.0"),
			Port:         getEnvIntOrDefault("SERVER_PORT", 8080),
			ReadTimeout:  time.Duration(getEnvIntOrDefault("SERVER_READ_TIMEOUT", 30)) * time.Second,
			WriteTimeout: time.Duration(getEnvIntOrDefault("SERVER_WRITE_TIMEOUT", 120)) * time.Second,
		},
		LLM: LLMConfig{
			Provider: getEnvOrDefault("LLM_PROVIDER", "openai"),
			BaseURL:  getEnvOrDefault("LLM_BASE_URL", "https://api.openai.com/v1"),
			APIKey:   getEnvOrDefault("LLM_API_KEY", ""),
			Model:    getEnvOrDefault("LLM_MODEL", "gpt-4o"),
		},
		Log: LogConfig{
			Level:  getEnvOrDefault("LOG_LEVEL", "info"),
			Format: getEnvOrDefault("LOG_FORMAT", "json"),
		},
		PProf: PProfConfig{
			Enable: getEnvBoolOrDefault("PPROF_ENABLE", false),
			Host:   getEnvOrDefault("PPROF_HOST", "localhost"),
			Port:   getEnvIntOrDefault("PPROF_PORT", 6060),
		},
		QQ: QQConfig{
			Enabled:      getEnvBoolOrDefault("QQ_ENABLED", false),
			AppID:        getEnvOrDefault("QQ_APP_ID", ""),
			ClientSecret: getEnvOrDefault("QQ_CLIENT_SECRET", ""),
			AllowFrom:    splitEnv("QQ_ALLOW_FROM"),
		},
		NapCat: NapCatConfig{
			Enabled:   getEnvBoolOrDefault("NAPCAT_ENABLED", false),
			WSUrl:     getEnvOrDefault("NAPCAT_WS_URL", "ws://localhost:3001"),
			Token:     getEnvOrDefault("NAPCAT_TOKEN", ""),
			AllowFrom: splitEnv("NAPCAT_ALLOW_FROM"),
		},
		Feishu: FeishuConfig{
			Enabled:           getEnvBoolOrDefault("FEISHU_ENABLED", false),
			AppID:             getEnvOrDefault("FEISHU_APP_ID", ""),
			AppSecret:         getEnvOrDefault("FEISHU_APP_SECRET", ""),
			EncryptKey:        getEnvOrDefault("FEISHU_ENCRYPT_KEY", ""),
			VerificationToken: getEnvOrDefault("FEISHU_VERIFICATION_TOKEN", ""),
			AllowFrom:         splitEnv("FEISHU_ALLOW_FROM"),
			Domain:            getEnvOrDefault("FEISHU_DOMAIN", ""),
		},
		Embedding: EmbeddingConfig{
			Provider:  getEnvOrDefault("LLM_EMBEDDING_PROVIDER", ""),
			BaseURL:   getEnvOrDefault("LLM_EMBEDDING_BASE_URL", ""),
			APIKey:    getEnvOrDefault("LLM_EMBEDDING_API_KEY", ""),
			Model:     getEnvOrDefault("LLM_EMBEDDING_MODEL", ""),
			MaxTokens: getEnvIntOrDefault("LLM_EMBEDDING_MAX_TOKENS", 2048),
		},
		Agent: AgentConfig{
			MaxIterations:            getEnvIntOrDefault("AGENT_MAX_ITERATIONS", 100),
			MaxConcurrency:           getEnvIntOrDefault("AGENT_MAX_CONCURRENCY", 3),
			MemoryWindow:             getEnvIntOrDefault("AGENT_MEMORY_WINDOW", 50),
			MemoryProvider:           getEnvOrDefault("MEMORY_PROVIDER", "flat"),
			WorkDir:                  getEnvOrDefault("WORK_DIR", "."),
			PromptFile:               getEnvOrDefault("PROMPT_FILE", "prompt.md"),
			SingleUser:               getEnvBoolOrDefault("SINGLE_USER", false),
			MCPInactivityTimeout:     getEnvDurationOrDefault("MCP_INACTIVITY_TIMEOUT", 30*time.Minute),
			MCPCleanupInterval:       getEnvDurationOrDefault("MCP_CLEANUP_INTERVAL", 5*time.Minute),
			SessionCacheTimeout:      getEnvDurationOrDefault("SESSION_CACHE_TIMEOUT", 24*time.Hour),
			EnableAutoCompress:       getEnvBoolOrDefault("AGENT_ENABLE_AUTO_COMPRESS", true),
			MaxContextTokens:         getEnvIntOrDefault("AGENT_MAX_CONTEXT_TOKENS", 100000),
			CompressionThreshold:     getEnvFloatOrDefault("AGENT_COMPRESSION_THRESHOLD", 0.7),
			ContextMode:              getEnvOrDefault("AGENT_CONTEXT_MODE", ""),
			MaxSubAgentDepth:         getEnvIntOrDefault("MAX_SUBAGENT_DEPTH", 6),
			EnableTopicIsolation:     getEnvBoolOrDefault("AGENT_ENABLE_TOPIC_ISOLATION", false),
			TopicMinSegmentSize:      getEnvIntOrDefault("AGENT_TOPIC_MIN_SEGMENT_SIZE", 3),
			TopicSimilarityThreshold: getEnvFloatOrDefault("AGENT_TOPIC_SIMILARITY_THRESHOLD", 0.3),
			PurgeOldMessages:         getEnvBoolOrDefault("AGENT_PURGE_OLD_MESSAGES", false),

			LLMRetryAttempts: getEnvIntOrDefault("LLM_RETRY_ATTEMPTS", 5),
			LLMRetryDelay:    getEnvDurationOrDefault("LLM_RETRY_DELAY", 1*time.Second),
			LLMRetryMaxDelay: getEnvDurationOrDefault("LLM_RETRY_MAX_DELAY", 30*time.Second),
			LLMRetryTimeout:  getEnvDurationOrDefault("LLM_RETRY_TIMEOUT", 120*time.Second),
		},
		OAuth: OAuthConfig{
			Enable:  getEnvBoolOrDefault("OAUTH_ENABLE", false),
			Host:    getEnvOrDefault("OAUTH_HOST", "127.0.0.1"), // 默认绑定 localhost，防止暴露到所有网络接口
			Port:    getEnvIntOrDefault("OAUTH_PORT", 8081),
			BaseURL: getEnvOrDefault("OAUTH_BASE_URL", ""),
		},
		Sandbox: SandboxConfig{
			Mode:        getEnvOrDefault("SANDBOX_MODE", "docker"),
			RemoteMode:  getEnvOrDefault("SANDBOX_REMOTE_MODE", ""),
			DockerImage: getEnvOrDefault("SANDBOX_DOCKER_IMAGE", "ubuntu:22.04"),
			HostWorkDir: getEnvOrDefault("HOST_WORK_DIR", ""),
			IdleTimeout: time.Duration(getEnvIntOrDefault("SANDBOX_IDLE_TIMEOUT_MINUTES", 30)) * time.Minute,
			WSPort:      getEnvIntOrDefault("SANDBOX_WS_PORT", 8080),
			AuthToken:   getEnvOrDefault("SANDBOX_AUTH_TOKEN", ""),
			PublicURL:   getEnvOrDefault("SANDBOX_PUBLIC_URL", ""),
		},
		StartupNotify: StartupNotifyConfig{
			Channel: getEnvOrDefault("STARTUP_NOTIFY_CHANNEL", ""),
			ChatID:  getEnvOrDefault("STARTUP_NOTIFY_CHAT_ID", ""),
		},
		Admin: AdminConfig{
			ChatID: getAdminChatID(),
		},
		Web: WebConfig{
			Enable:    getEnvBoolOrDefault("WEB_ENABLED", false),
			Host:      getEnvOrDefault("WEB_HOST", "0.0.0.0"),
			Port:      getEnvIntOrDefault("WEB_PORT", 8082),
			StaticDir: getEnvOrDefault("WEB_STATIC_DIR", ""),
		},
	}

}

// getEnvOrDefault 获取环境变量，如果不存在则返回默认值
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvIntOrDefault 获取整数环境变量，如果不存在则返回默认值
func getEnvIntOrDefault(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
		warnEnvParse(key, value, strconv.Itoa(defaultValue))
	}
	return defaultValue
}

// getEnvBoolOrDefault 获取布尔环境变量，如果不存在则返回默认值
func getEnvBoolOrDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
		warnEnvParse(key, value, strconv.FormatBool(defaultValue))
	}
	return defaultValue
}

// splitEnv 获取逗号分隔的环境变量列表
func splitEnv(key string) []string {
	value := os.Getenv(key)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// getEnvDurationOrDefault 获取时长环境变量，如果不存在则返回默认值
func getEnvDurationOrDefault(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
		warnEnvParse(key, value, defaultValue.String())
	}
	return defaultValue
}

// getEnvFloatOrDefault 获取浮点数环境变量，如果不存在则返回默认值
func getEnvFloatOrDefault(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
		warnEnvParse(key, value, strconv.FormatFloat(defaultValue, 'f', -1, 64))
	}
	return defaultValue
}

// getAdminChatID 获取管理员会话 ID，实现回退逻辑
// 优先读取 ADMIN_CHAT_ID，如果为空则回退到 STARTUP_NOTIFY_CHAT_ID
func getAdminChatID() string {
	if adminChatID := os.Getenv("ADMIN_CHAT_ID"); adminChatID != "" {
		return adminChatID
	}
	// 回退到 STARTUP_NOTIFY_CHAT_ID
	return os.Getenv("STARTUP_NOTIFY_CHAT_ID")
}
