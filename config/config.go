package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	Token  string `json:"token"`
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
// CLIConfig CLI 客户端配置（存储在 config.json，供 xbot-cli 读取）。
type CLIConfig struct {
	// ServerURL 指定远端 agent server 的 WebSocket 地址（如 ws://localhost:8080）。
	// 若非空，xbot-cli 默认以 RemoteBackend 连接该 server，而非本地运行 agent。
	// 可通过 --server flag 在命令行覆盖此值。
	ServerURL string `json:"server_url,omitempty"`
	// Token 连接 server 时使用的认证 token（对应 server 端的 admin.token）。
	Token string `json:"token,omitempty"`
}

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
	CLI           CLIConfig            `json:"cli,omitempty"`
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
	DynamicMaxTokens     *bool   `json:"dynamic_max_tokens,omitempty"` // DEPRECATED: no longer used, kept for config.json compat

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
	VanguardModel   string `json:"vanguard_model,omitempty"`
	BalanceModel    string `json:"balance_model,omitempty"`
	SwiftModel      string `json:"swift_model,omitempty"`
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
// 它会先读取磁盘上已有的文件，将 Go struct 序列化后的顶层 key 覆盖到原始 JSON 上，
// 同时保留磁盘文件中存在但 Go struct 未定义的字段（未知 key）。
// 这样用户手动添加的自定义字段或未来新增的 struct 字段不会被静默丢弃。
func SaveToFile(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// 序列化 Go struct
	structData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// 尝试读取磁盘上已有的文件，做 JSON 级合并以保留未知字段
	finalData := structData
	if existing, readErr := os.ReadFile(path); readErr == nil && len(existing) > 0 {
		if merged, mergeErr := mergeJSONPreserveUnknown(existing, structData); mergeErr == nil {
			finalData = merged
		}
		// 合并失败时回退到纯 struct 序列化（安全降级）
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, finalData, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

// mergeJSONPreserveUnknown 将 structData 的顶层 key 深度合并到 existing 上。
// 对于两边都是 JSON object 的嵌套值，递归合并以保留 unknown 字段。
// structData 中的 key 始终覆盖 existing 中的同名 key（非 object 时直接替换）。
func mergeJSONPreserveUnknown(existing, structData []byte) ([]byte, error) {
	var existingMap map[string]json.RawMessage
	if err := json.Unmarshal(existing, &existingMap); err != nil {
		return nil, err
	}
	var structMap map[string]json.RawMessage
	if err := json.Unmarshal(structData, &structMap); err != nil {
		return nil, err
	}
	// 递归合并：两边都是 object 时深度合并，否则 struct 覆盖
	for k, structVal := range structMap {
		if existingVal, ok := existingMap[k]; ok {
			merged, err := deepMergeJSON(existingVal, structVal)
			if err != nil {
				// 降级为直接覆盖
				existingMap[k] = structVal
				continue
			}
			existingMap[k] = merged
		} else {
			existingMap[k] = structVal
		}
	}
	return json.MarshalIndent(existingMap, "", "  ")
}

// deepMergeJSON 对两个 JSON 值做深度合并。
// 如果两者都是 JSON object，递归合并（structVal 的 key 覆盖 existingVal）。
// 否则返回 structVal（直接替换）。
func deepMergeJSON(existing, structVal json.RawMessage) (json.RawMessage, error) {
	var existingObj, structObj map[string]json.RawMessage
	existingIsObj := json.Unmarshal(existing, &existingObj) == nil
	structIsObj := json.Unmarshal(structVal, &structObj) == nil

	if existingIsObj && structIsObj {
		for k, v := range structObj {
			if ev, ok := existingObj[k]; ok {
				merged, err := deepMergeJSON(ev, v)
				if err != nil {
					existingObj[k] = v
					continue
				}
				existingObj[k] = merged
			} else {
				existingObj[k] = v
			}
		}
		return json.Marshal(existingObj)
	}
	return structVal, nil
}

func splitCommaTrim(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func setDurationEnv(key string, dst *time.Duration) {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			*dst = d
		}
	}
}

func setSecondsEnv(key string, dst *time.Duration) {
	if v := os.Getenv(key); v != "" {
		if sec, err := strconv.Atoi(v); err == nil {
			*dst = time.Duration(sec) * time.Second
		}
	}
}

// applyEnvOverrides 用环境变量覆盖配置（与 README / .env.example 中的变量名一致，优先级高于 config.json）。
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("SERVER_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("SERVER_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = i
		}
	}
	setSecondsEnv("SERVER_READ_TIMEOUT", &cfg.Server.ReadTimeout)
	setSecondsEnv("SERVER_WRITE_TIMEOUT", &cfg.Server.WriteTimeout)

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
	if v := os.Getenv("LLM_RETRY_ATTEMPTS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Agent.LLMRetryAttempts = i
		}
	}
	setDurationEnv("LLM_RETRY_DELAY", &cfg.Agent.LLMRetryDelay)
	setDurationEnv("LLM_RETRY_MAX_DELAY", &cfg.Agent.LLMRetryMaxDelay)
	setDurationEnv("LLM_RETRY_TIMEOUT", &cfg.Agent.LLMRetryTimeout)

	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
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
	if v := os.Getenv("LLM_EMBEDDING_MAX_TOKENS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Embedding.MaxTokens = i
		}
	}

	if v := os.Getenv("WORK_DIR"); v != "" {
		cfg.Agent.WorkDir = v
	}
	if v := os.Getenv("PROMPT_FILE"); v != "" {
		cfg.Agent.PromptFile = v
	}
	// SINGLE_USER env var removed — singleUser normalization is no longer used
	if v := os.Getenv("MEMORY_PROVIDER"); v != "" {
		cfg.Agent.MemoryProvider = v
	}
	if v := os.Getenv("AGENT_MAX_ITERATIONS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && cfg.Agent.MaxIterations == 0 {
			cfg.Agent.MaxIterations = i
		}
	}
	if v := os.Getenv("AGENT_MAX_CONCURRENCY"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && cfg.Agent.MaxConcurrency == 0 {
			cfg.Agent.MaxConcurrency = i
		}
	}
	setDurationEnv("MCP_INACTIVITY_TIMEOUT", &cfg.Agent.MCPInactivityTimeout)
	setDurationEnv("MCP_CLEANUP_INTERVAL", &cfg.Agent.MCPCleanupInterval)
	setDurationEnv("SESSION_CACHE_TIMEOUT", &cfg.Agent.SessionCacheTimeout)

	if v := os.Getenv("AGENT_CONTEXT_MODE"); v != "" {
		cfg.Agent.ContextMode = v
	}
	if v := os.Getenv("AGENT_ENABLE_AUTO_COMPRESS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Agent.EnableAutoCompress = &b
		}
	}
	if v := os.Getenv("AGENT_MAX_CONTEXT_TOKENS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && cfg.Agent.MaxContextTokens == 0 {
			cfg.Agent.MaxContextTokens = i
		}
	}
	if v := os.Getenv("AGENT_COMPRESSION_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Agent.CompressionThreshold = f
		}
	}
	if v := os.Getenv("AGENT_PURGE_OLD_MESSAGES"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Agent.PurgeOldMessages = b
		}
	}
	if v := os.Getenv("MAX_SUBAGENT_DEPTH"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && cfg.Agent.MaxSubAgentDepth == 0 {
			cfg.Agent.MaxSubAgentDepth = i
		}
	}

	if v := os.Getenv("SANDBOX_MODE"); v != "" {
		cfg.Sandbox.Mode = v
	}
	if v := os.Getenv("SANDBOX_REMOTE_MODE"); v != "" {
		cfg.Sandbox.RemoteMode = v
	}
	if v := os.Getenv("SANDBOX_DOCKER_IMAGE"); v != "" {
		cfg.Sandbox.DockerImage = v
	}
	if v := os.Getenv("HOST_WORK_DIR"); v != "" {
		cfg.Sandbox.HostWorkDir = v
	}
	if v := os.Getenv("SANDBOX_IDLE_TIMEOUT_MINUTES"); v != "" {
		if min, err := strconv.Atoi(v); err == nil && cfg.Sandbox.IdleTimeout == 0 {
			cfg.Sandbox.IdleTimeout = time.Duration(min) * time.Minute
		}
	}
	if v := os.Getenv("SANDBOX_WS_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && cfg.Sandbox.WSPort == 0 {
			cfg.Sandbox.WSPort = i
		}
	}
	if v := os.Getenv("SANDBOX_AUTH_TOKEN"); v != "" {
		cfg.Sandbox.AuthToken = v
	}
	if v := os.Getenv("SANDBOX_PUBLIC_URL"); v != "" {
		cfg.Sandbox.PublicURL = v
	}

	if v := os.Getenv("FEISHU_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Feishu.Enabled = b
		}
	}
	if v := os.Getenv("FEISHU_APP_ID"); v != "" {
		cfg.Feishu.AppID = v
	}
	if v := os.Getenv("FEISHU_APP_SECRET"); v != "" {
		cfg.Feishu.AppSecret = v
	}
	if v := os.Getenv("FEISHU_ENCRYPT_KEY"); v != "" {
		cfg.Feishu.EncryptKey = v
	}
	if v := os.Getenv("FEISHU_VERIFICATION_TOKEN"); v != "" {
		cfg.Feishu.VerificationToken = v
	}
	if v, ok := os.LookupEnv("FEISHU_ALLOW_FROM"); ok {
		cfg.Feishu.AllowFrom = splitCommaTrim(v)
	}
	if v := os.Getenv("FEISHU_DOMAIN"); v != "" {
		cfg.Feishu.Domain = v
	}

	if v := os.Getenv("QQ_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.QQ.Enabled = b
		}
	}
	if v := os.Getenv("QQ_APP_ID"); v != "" {
		cfg.QQ.AppID = v
	}
	if v := os.Getenv("QQ_CLIENT_SECRET"); v != "" {
		cfg.QQ.ClientSecret = v
	}
	if v, ok := os.LookupEnv("QQ_ALLOW_FROM"); ok {
		cfg.QQ.AllowFrom = splitCommaTrim(v)
	}

	if v := os.Getenv("NAPCAT_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.NapCat.Enabled = b
		}
	}
	if v := os.Getenv("NAPCAT_WS_URL"); v != "" {
		cfg.NapCat.WSUrl = v
	}
	if v := os.Getenv("NAPCAT_TOKEN"); v != "" {
		cfg.NapCat.Token = v
	}
	if v, ok := os.LookupEnv("NAPCAT_ALLOW_FROM"); ok {
		cfg.NapCat.AllowFrom = splitCommaTrim(v)
	}

	if v := os.Getenv("WEB_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Web.Enable = b
		}
	}
	if v := os.Getenv("WEB_HOST"); v != "" {
		cfg.Web.Host = v
	}
	if v := os.Getenv("WEB_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Web.Port = i
		}
	}
	if v := os.Getenv("WEB_STATIC_DIR"); v != "" {
		cfg.Web.StaticDir = v
	}
	if v := os.Getenv("WEB_UPLOAD_DIR"); v != "" {
		cfg.Web.UploadDir = v
	}
	if v := os.Getenv("WEB_PERSONA_ISOLATION"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Web.PersonaIsolation = b
		}
	}
	if v := os.Getenv("WEB_INVITE_ONLY"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Web.InviteOnly = b
		}
	}

	if v := os.Getenv("EVENT_WEBHOOK_ENABLE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.EventWebhook.Enable = b
		}
	}
	if v := os.Getenv("EVENT_WEBHOOK_HOST"); v != "" {
		cfg.EventWebhook.Host = v
	}
	if v := os.Getenv("EVENT_WEBHOOK_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.EventWebhook.Port = i
		}
	}
	if v := os.Getenv("EVENT_WEBHOOK_BASE_URL"); v != "" {
		cfg.EventWebhook.BaseURL = v
	}
	if v := os.Getenv("EVENT_WEBHOOK_MAX_BODY_SIZE"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.EventWebhook.MaxBodySize = int64(i)
		}
	}
	if v := os.Getenv("EVENT_WEBHOOK_RATE_LIMIT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.EventWebhook.RateLimit = i
		}
	}

	if v := os.Getenv("OAUTH_ENABLE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.OAuth.Enable = b
		}
	}
	if v := os.Getenv("OAUTH_HOST"); v != "" {
		cfg.OAuth.Host = v
	}
	if v := os.Getenv("OAUTH_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.OAuth.Port = i
		}
	}
	if v := os.Getenv("OAUTH_BASE_URL"); v != "" {
		cfg.OAuth.BaseURL = v
	}

	if v := os.Getenv("PPROF_ENABLE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.PProf.Enable = b
		}
	}
	if v := os.Getenv("PPROF_HOST"); v != "" {
		cfg.PProf.Host = v
	}
	if v := os.Getenv("PPROF_PORT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.PProf.Port = i
		}
	}

	if v := os.Getenv("STARTUP_NOTIFY_CHANNEL"); v != "" {
		cfg.StartupNotify.Channel = v
	}
	if v := os.Getenv("STARTUP_NOTIFY_CHAT_ID"); v != "" {
		cfg.StartupNotify.ChatID = v
	}
	if v := os.Getenv("ADMIN_CHAT_ID"); v != "" {
		cfg.Admin.ChatID = v
	}
	if v := os.Getenv("ADMIN_TOKEN"); v != "" {
		cfg.Admin.Token = v
	}

	if v := os.Getenv("TAVILY_API_KEY"); v != "" {
		cfg.TavilyAPIKey = v
	}
}

// EffectiveEnableAutoCompress 返回是否启用自动压缩；config.json 省略该字段时与文档默认一致，为 true。
func (a AgentConfig) EffectiveEnableAutoCompress() bool {
	if a.EnableAutoCompress == nil {
		return true
	}
	return *a.EnableAutoCompress
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
		cfg.Sandbox.Mode = "none"
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
		cfg.Agent.CompressionThreshold = 0.9
	}
	if cfg.Agent.MaxSubAgentDepth == 0 {
		cfg.Agent.MaxSubAgentDepth = 6
	}
	// Server.Host/Port defaults follow Web.Host/Port since all traffic
	// (HTTP API, WebSocket, runner WS) goes through the same port.
	// Keeping them in sync avoids confusion.
	if cfg.Server.Host == "" {
		cfg.Server.Host = cfg.Web.Host // "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = cfg.Web.Port // 8082
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

// PublicWSAddr returns the WebSocket address runners should connect to.
// Uses Sandbox.PublicURL if set, otherwise falls back to the unified
// web server address (Server.Host:Server.Port, which defaults to Web.Host:Web.Port).
func (c *Config) PublicWSAddr() string {
	if c.Sandbox.PublicURL != "" {
		return c.Sandbox.PublicURL
	}
	return fmt.Sprintf("ws://%s:%d", c.Server.Host, c.Server.Port)
}

// getAdminChatID 获取管理员会话 ID，实现回退逻辑
// 优先读取 ADMIN_CHAT_ID，如果为空则回退到 STARTUP_NOTIFY_CHAT_ID
func getAdminChatID() string {
	if adminChatID := os.Getenv("ADMIN_CHAT_ID"); adminChatID != "" {
		return adminChatID
	}
	return os.Getenv("STARTUP_NOTIFY_CHAT_ID")
}
