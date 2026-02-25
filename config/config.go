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

// Config 应用配置
type Config struct {
	Server ServerConfig
	LLM    LLMConfig
	Log    LogConfig
	PProf  PProfConfig
	Feishu FeishuConfig
	Agent  AgentConfig
}

// FeishuConfig 飞书渠道配置
type FeishuConfig struct {
	Enabled           bool
	AppID             string
	AppSecret         string
	EncryptKey        string
	VerificationToken string
	AllowFrom         []string // 允许的 open_id 列表（空则允许所有）
}

// AgentConfig Agent 配置
type AgentConfig struct {
	MaxIterations int    // 单次对话最大工具迭代次数
	MemoryWindow  int    // 上下文窗口（保留最近多少条消息）
	WorkDir       string // 工作目录（所有文件相对此目录存放）
	PromptFile    string // 系统提示词模板文件路径（空则使用内置默认值）
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
	Provider string // LLM 提供商: "openai" 或 "codebuddy"
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
		Feishu: FeishuConfig{
			Enabled:           getEnvBoolOrDefault("FEISHU_ENABLED", false),
			AppID:             getEnvOrDefault("FEISHU_APP_ID", ""),
			AppSecret:         getEnvOrDefault("FEISHU_APP_SECRET", ""),
			EncryptKey:        getEnvOrDefault("FEISHU_ENCRYPT_KEY", ""),
			VerificationToken: getEnvOrDefault("FEISHU_VERIFICATION_TOKEN", ""),
			AllowFrom:         splitEnv("FEISHU_ALLOW_FROM"),
		},
		Agent: AgentConfig{
			MaxIterations: getEnvIntOrDefault("AGENT_MAX_ITERATIONS", 20),
			MemoryWindow:  getEnvIntOrDefault("AGENT_MEMORY_WINDOW", 50),
			WorkDir:       getEnvOrDefault("WORK_DIR", "."),
			PromptFile:    getEnvOrDefault("PROMPT_FILE", ""),
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
