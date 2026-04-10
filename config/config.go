package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// OAuthConfig OAuth 配置
type OAuthConfig struct {
	Enable  bool   `json:"enable"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	BaseURL string `json:"base_url"`
}

// SandboxConfig 沙箱配置
type SandboxConfig struct {
	Mode        string        `json:"mode"`
	RemoteMode  string        `json:"remote_mode"`
	DockerImage string        `json:"docker_image"`
	HostWorkDir string        `json:"host_work_dir"`
	IdleTimeout time.Duration `json:"idle_timeout"`
	WSPort      int           `json:"ws_port"`
	AuthToken   string        `json:"auth_token"`
	PublicURL   string        `json:"public_url"`
	AllowWebUserServerRunner bool `json:"allow_web_user_server_runner"`
}

// QQConfig QQ 机器人渠道配置
type QQConfig struct {
	Enabled      bool     `json:"enabled"`
	AppID        string   `json:"app_id"`
	ClientSecret string   `json:"client_secret"`
	AllowFrom    []string `json:"allow_from"`
}

// NapCatConfig NapCat (OneBot 11) 渠道配置
type NapCatConfig struct {
	Enabled   bool     `json:"enabled"`
	WSUrl     string   `json:"ws_url"`
	Token     string   `json:"token"`
	AllowFrom []string `json:"allow_from"`
}

// EmbeddingConfig Embedding 配置
type EmbeddingConfig struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

// StartupNotifyConfig 启动通知配置
type StartupNotifyConfig struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

// AdminConfig 管理员配置
type AdminConfig struct {
	ChatID string `json:"chat_id"`
}

// OSSConfig 对象存储配置
type OSSConfig struct {
	Provider       string `json:"provider"`
	QiniuAccessKey string `json:"qiniu_access_key"`
	QiniuSecretKey string `json:"qiniu_secret_key"`
	QiniuBucket    string `json:"qiniu_bucket"`
	QiniuDomain    string `json:"qiniu_domain"`
	QiniuRegion    string `json:"qiniu_region"`
}

// EventWebhookConfig 事件 Webhook 配置
type EventWebhookConfig struct {
	Enable      bool   `json:"enable"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	BaseURL     string `json:"base_url"`
	MaxBodySize int64  `json:"max_body_size"`
	RateLimit   int    `json:"rate_limit"` // max requests per minute per trigger
}

// WebConfig Web 渠道配置
type WebConfig struct {
	Enable           bool   `json:"enable"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	StaticDir        string `json:"static_dir"`
	UploadDir        string `json:"upload_dir"`
	PersonaIsolation bool   `json:"persona_isolation"`
	InviteOnly       bool   `json:"invite_only"`
}

// Config 应用配置
type Config struct {
	Server        ServerConfig         `json:"server"`
	LLM           LLMConfig            `json:"llm"`
	Embedding     EmbeddingConfig      `json:"embedding"`
	Log           LogConfig            `json:"log"`
	PProf         PProfConfig          `json:"pprof"`
	Feishu        FeishuConfig         `json:"feishu"`
	QQ            QQConfig             `json:"qq"`
	NapCat        NapCatConfig         `json:"napcat"`
	Agent         AgentConfig          `json:"agent"`
	OAuth         OAuthConfig          `json:"oauth"`
	Sandbox       SandboxConfig        `json:"sandbox"`
	StartupNotify StartupNotifyConfig  `json:"startup_notify"`
	Admin         AdminConfig          `json:"admin"`
	Web           WebConfig            `json:"web"`
	EventWebhook  EventWebhookConfig   `json:"event_webhook"`
	OSS           OSSConfig            `json:"oss"`
	TavilyAPIKey  string               `json:"tavily_api_key"`
	Subscriptions []SubscriptionConfig `json:"subscriptions,omitempty"`
}

// FeishuConfig 飞书渠道配置
type FeishuConfig struct {
	Enabled           bool     `json:"enabled"`
	AppID             string   `json:"app_id"`
	AppSecret         string   `json:"app_secret"`
	EncryptKey        string   `json:"encrypt_key"`
	VerificationToken string   `json:"verification_token"`
	AllowFrom         []string `json:"allow_from"`
	Domain            string   `json:"domain"`
}

// AgentConfig Agent 配置
type AgentConfig struct {
	MaxIterations  int    `json:"max_iterations"`
	MaxConcurrency int    `json:"max_concurrency"`
	MemoryProvider string `json:"memory_provider"`
	WorkDir        string `json:"work_dir"`
	PromptFile     string `json:"prompt_file"`
	SingleUser     bool   `json:"single_user"` // Deprecated: no longer used, kept for config file compatibility

	MCPInactivityTimeout time.Duration `json:"mcp_inactivity_timeout"`
	MCPCleanupInterval   time.Duration `json:"mcp_cleanup_interval"`
	SessionCacheTimeout  time.Duration `json:"session_cache_timeout"`

	ContextMode string `json:"context_mode"`
	// EnableAutoCompress 为 nil 表示 JSON 未写该字段，Load 后与未设置 AGENT_ENABLE_AUTO_COMPRESS 一致，默认启用压缩。
	EnableAutoCompress   *bool   `json:"enable_auto_compress,omitempty"`
	MaxContextTokens     int     `json:"max_context_tokens"`
	CompressionThreshold float64 `json:"compression_threshold"`

	PurgeOldMessages bool `json:"purge_old_messages"`

	MaxSubAgentDepth int `json:"max_sub_agent_depth"`

	LLMRetryAttempts int           `json:"llm_retry_attempts"`
	LLMRetryDelay    time.Duration `json:"llm_retry_delay"`
	LLMRetryMaxDelay time.Duration `json:"llm_retry_max_delay"`
	LLMRetryTimeout  time.Duration `json:"llm_retry_timeout"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Host         string        `json:"host"`
	Port         int           `json:"port"`
	ReadTimeout  time.Duration `json:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout"`
}

// LLMConfig LLM 配置
type LLMConfig struct {
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"` // 0 = use default (8192)
	ThinkingMode    string `json:"thinking_mode,omitempty"`
}

// SubscriptionConfig CLI 订阅配置（存储在 config.json，不存数据库）。
type SubscriptionConfig struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"` // 0 = use default (8192)
	ThinkingMode    string `json:"thinking_mode,omitempty"`     // "" = auto, "enabled", "disabled"
	Active          bool   `json:"active"`
}

// LogConfig 日志配置
type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

// PProfConfig pprof 配置
type PProfConfig struct {
	Enable bool   `json:"enable"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
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

// SaveToFile 将配置保存到 JSON 文件（原子写入：先写临时文件再 rename）。
func SaveToFile(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}


func (a AgentConfig) EffectiveEnableAutoCompress() bool {
	if a.EnableAutoCompress == nil {
		return true
	}
	return *a.EnableAutoCompress
}

// Load 加载配置：从全局 config.json 读取基础值，最后填充默认值。
// 便捷函数，等价于 LoadFromPath(ConfigFilePath())。
func Load() *Config {
	return LoadFromPath(ConfigFilePath())
}

// LoadFromPath 从指定路径加载配置，并填充默认值。
func LoadFromPath(path string) *Config {
	cfg := LoadFromFile(path)
	if cfg == nil {
		if path != "" {
			slog.Warn("config file not found, using defaults", "path", path)
		}
		cfg = &Config{}
	}
	applyDefaults(cfg)
	return cfg
}

// applyDefaults 填充零值默认配置（仅在对应字段为零值时生效）。
func applyDefaults(cfg *Config) {
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
		cfg.Agent.MaxIterations = 2000
	}
	if cfg.Agent.MaxConcurrency == 0 {
		cfg.Agent.MaxConcurrency = 3
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
	if cfg.Sandbox.DockerImage == "" {
		cfg.Sandbox.DockerImage = "ubuntu:22.04"
	}
	if cfg.Sandbox.WSPort == 0 {
		cfg.Sandbox.WSPort = 8080
	}
	if cfg.Agent.MemoryProvider == "" {
		cfg.Agent.MemoryProvider = "flat"
	}
	if cfg.OAuth.Host == "" {
		cfg.OAuth.Host = "127.0.0.1"
	}
	if cfg.OAuth.Port == 0 {
		cfg.OAuth.Port = 8081
	}
	if cfg.Web.Host == "" {
		cfg.Web.Host = "0.0.0.0"
	}
	if cfg.Web.Port == 0 {
		cfg.Web.Port = 8082
	}
	if cfg.EventWebhook.Host == "" {
		cfg.EventWebhook.Host = "0.0.0.0"
	}
	if cfg.EventWebhook.Port == 0 {
		cfg.EventWebhook.Port = 8090
	}
	if cfg.EventWebhook.MaxBodySize == 0 {
		cfg.EventWebhook.MaxBodySize = 1 << 20 // 1 MB
	}
	if cfg.EventWebhook.RateLimit == 0 {
		cfg.EventWebhook.RateLimit = 60
	}
	if cfg.NapCat.WSUrl == "" {
		cfg.NapCat.WSUrl = "ws://localhost:3001"
	}
	if cfg.PProf.Host == "" {
		cfg.PProf.Host = "localhost"
	}
	if cfg.PProf.Port == 0 {
		cfg.PProf.Port = 6060
	}
	if cfg.Embedding.MaxTokens == 0 {
		cfg.Embedding.MaxTokens = 2048
	}
	if cfg.Agent.MaxContextTokens == 0 {
		cfg.Agent.MaxContextTokens = 200000
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
		cfg.Admin.ChatID = getAdminChatID(cfg)
	}
}

// getAdminChatID 获取管理员会话 ID，实现回退逻辑。
// 优先使用 Admin.ChatID，为空则回退到 StartupNotify.ChatID。
func getAdminChatID(cfg *Config) string {
	if cfg.Admin.ChatID != "" {
		return cfg.Admin.ChatID
	}
	return cfg.StartupNotify.ChatID
}
