package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

func init() {
	_ = godotenv.Load(".env")
}

// OAuthConfig OAuth 配置
type OAuthConfig struct {
	Enable  bool   // 是否启用 OAuth 功能
	Port    int    // OAuth 服务监听端口（默认 8081）
	BaseURL string // OAuth 回调基础 URL (e.g., https://your-domain.com)
}

// SandboxConfig 沙箱配置
type SandboxConfig struct {
	Mode                  string // 沙箱模式: "none", "docker"
	DockerImage           string // Docker 镜像（如 "ubuntu:22.04"）
	HostWorkDir           string // DinD 手动覆盖：宿主机上对应 WORK_DIR 的真实路径（通常自动检测，仅在检测失败时设置）
	CommitSquashThreshold int    // commit 达到此阈值时，用 export+import 扁平化镜像（默认 5，设为 0 禁用）
}

// QQConfig QQ 机器人渠道配置
type QQConfig struct {
	Enabled      bool
	AppID        string
	ClientSecret string
	AllowFrom    []string // 允许的 openid 列表（空则允许所有）
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

// Config 应用配置
type Config struct {
	Server        ServerConfig
	LLM           LLMConfig
	Embedding     EmbeddingConfig
	Log           LogConfig
	PProf         PProfConfig
	Feishu        FeishuConfig
	QQ            QQConfig
	Agent         AgentConfig
	OAuth         OAuthConfig
	Sandbox       SandboxConfig
	StartupNotify StartupNotifyConfig
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
	EnableAutoCompress   bool    // 是否启用自动上下文压缩（默认 true）
	MaxContextTokens     int     // 最大上下文 token 数（默认 100000）
	CompressionThreshold float64 // 触发压缩的 token 比例阈值（默认 0.8，即 80% 时触发）
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
	Provider string // LLM 提供商: "openai"、"codebuddy" 或 "anthropic"
	BaseURL  string
	APIKey   string
	Model    string // 默认模型（API 获取失败时的回退模型）
	// CodeBuddy 专用配置
	UserID       string // X-User-Id
	EnterpriseID string // X-Enterprise-Id / X-Tenant-Id
	Domain       string // X-Domain
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
			// DeepSeek 配置（OpenAI 兼容）
			// Provider: getEnvOrDefault("LLM_PROVIDER", "openai"),
			// BaseURL:  getEnvOrDefault("LLM_BASE_URL", "https://api.deepseek.com"),
			// APIKey:   getEnvOrDefault("LLM_API_KEY", ""),
			// Model:    getEnvOrDefault("LLM_MODEL", "deepseek-chat"),

			// CodeBuddy 配置
			Provider:     getEnvOrDefault("LLM_PROVIDER", "codebuddy"),
			BaseURL:      getEnvOrDefault("LLM_BASE_URL", "https://copilot.tencent.com/v2/chat/completions"),
			APIKey:       getEnvOrDefault("LLM_API_KEY", ""),
			Model:        getEnvOrDefault("LLM_MODEL", ""),
			UserID:       getEnvOrDefault("LLM_USER_ID", ""),
			EnterpriseID: getEnvOrDefault("LLM_ENTERPRISE_ID", ""),
			Domain:       getEnvOrDefault("LLM_DOMAIN", ""),
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
		Feishu: FeishuConfig{
			Enabled:           getEnvBoolOrDefault("FEISHU_ENABLED", false),
			AppID:             getEnvOrDefault("FEISHU_APP_ID", ""),
			AppSecret:         getEnvOrDefault("FEISHU_APP_SECRET", ""),
			EncryptKey:        getEnvOrDefault("FEISHU_ENCRYPT_KEY", ""),
			VerificationToken: getEnvOrDefault("FEISHU_VERIFICATION_TOKEN", ""),
			AllowFrom:         splitEnv("FEISHU_ALLOW_FROM"),
		},
		Embedding: EmbeddingConfig{
			Provider:  getEnvOrDefault("LLM_EMBEDDING_PROVIDER", ""),
			BaseURL:   getEnvOrDefault("LLM_EMBEDDING_BASE_URL", ""),
			APIKey:    getEnvOrDefault("LLM_EMBEDDING_API_KEY", ""),
			Model:     getEnvOrDefault("LLM_EMBEDDING_MODEL", ""),
			MaxTokens: getEnvIntOrDefault("LLM_EMBEDDING_MAX_TOKENS", 2048),
		},
		Agent: AgentConfig{
			MaxIterations:        getEnvIntOrDefault("AGENT_MAX_ITERATIONS", 100),
			MaxConcurrency:       getEnvIntOrDefault("AGENT_MAX_CONCURRENCY", 3),
			MemoryWindow:         getEnvIntOrDefault("AGENT_MEMORY_WINDOW", 50),
			MemoryProvider:       getEnvOrDefault("MEMORY_PROVIDER", "flat"),
			WorkDir:              getEnvOrDefault("WORK_DIR", "."),
			PromptFile:           getEnvOrDefault("PROMPT_FILE", "prompt.md"),
			SingleUser:           getEnvBoolOrDefault("SINGLE_USER", false),
			MCPInactivityTimeout: getEnvDurationOrDefault("MCP_INACTIVITY_TIMEOUT", 30*time.Minute),
			MCPCleanupInterval:   getEnvDurationOrDefault("MCP_CLEANUP_INTERVAL", 5*time.Minute),
			SessionCacheTimeout:  getEnvDurationOrDefault("SESSION_CACHE_TIMEOUT", 24*time.Hour),
			EnableAutoCompress:   getEnvBoolOrDefault("AGENT_ENABLE_AUTO_COMPRESS", true),
			MaxContextTokens:     getEnvIntOrDefault("AGENT_MAX_CONTEXT_TOKENS", 100000),
			CompressionThreshold: getEnvFloatOrDefault("AGENT_COMPRESSION_THRESHOLD", 0.8),
		},
		OAuth: OAuthConfig{
			Enable:  getEnvBoolOrDefault("OAUTH_ENABLE", false),
			Port:    getEnvIntOrDefault("OAUTH_PORT", 8081),
			BaseURL: getEnvOrDefault("OAUTH_BASE_URL", ""),
		},
		Sandbox: SandboxConfig{
			Mode:                  getEnvOrDefault("SANDBOX_MODE", "docker"),
			DockerImage:           getEnvOrDefault("SANDBOX_DOCKER_IMAGE", "ubuntu:22.04"),
			HostWorkDir:           getEnvOrDefault("HOST_WORK_DIR", ""),
			CommitSquashThreshold: getEnvIntOrDefault("SANDBOX_COMMIT_SQUASH_THRESHOLD", 5),
		},
		StartupNotify: StartupNotifyConfig{
			Channel: getEnvOrDefault("STARTUP_NOTIFY_CHANNEL", ""),
			ChatID:  getEnvOrDefault("STARTUP_NOTIFY_CHAT_ID", ""),
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
	}
	return defaultValue
}

// getEnvBoolOrDefault 获取布尔环境变量，如果不存在则返回默认值
func getEnvBoolOrDefault(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
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
	}
	return defaultValue
}

// getEnvFloatOrDefault 获取浮点数环境变量，如果不存在则返回默认值
func getEnvFloatOrDefault(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
	}
	return defaultValue
}
