package agent

import (
	"context"
	"fmt"
	"sync"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// LLMFactory 管理用户自定义 LLM 客户端的创建和缓存
type LLMFactory struct {
	configSvc    *sqlite.UserLLMConfigService
	settingsSvc  *SettingsService // 用于读写用户并发配置
	defaultLLM   llm.LLM
	defaultModel string

	// LLMSemaphoreManager 管理 per-tenant LLM 并发信号量
	llmSemManager *llm.LLMSemaphoreManager

	// 缓存用户的 LLM 客户端
	mu            sync.RWMutex
	clients       map[string]llm.LLM // senderID -> LLM client
	models        map[string]string  // senderID -> model name
	maxContexts   map[string]int     // senderID -> max_context tokens
	thinkingModes map[string]string  // senderID -> thinking_mode

	// hasCustomLLMCache 缓存用户是否有自定义 LLM 配置（避免频繁查数据库）
	// 使用 sync.Map 保证并发安全
	hasCustomLLMCache sync.Map
}

// NewLLMFactory 创建 LLM 工厂
func NewLLMFactory(configSvc *sqlite.UserLLMConfigService, defaultLLM llm.LLM, defaultModel string) *LLMFactory {
	return &LLMFactory{
		configSvc:     configSvc,
		defaultLLM:    defaultLLM,
		defaultModel:  defaultModel,
		clients:       make(map[string]llm.LLM),
		models:        make(map[string]string),
		maxContexts:   make(map[string]int),
		thinkingModes: make(map[string]string),
		// hasCustomLLMCache 使用零值 sync.Map，无需初始化
	}
}

// GetLLM 获取用户的 LLM 客户端，如果没有自定义配置则返回默认客户端
// 返回: (LLM客户端, 模型名, maxContext, thinkingMode)
func (f *LLMFactory) GetLLM(senderID string) (llm.LLM, string, int, string) {
	// 先检查缓存
	f.mu.RLock()
	if client, ok := f.clients[senderID]; ok {
		model := f.models[senderID]
		maxCtx := f.maxContexts[senderID]
		thinkingMode := f.thinkingModes[senderID]
		f.mu.RUnlock()
		return client, model, maxCtx, thinkingMode
	}
	f.mu.RUnlock()

	// 从数据库加载配置
	cfg, err := f.configSvc.GetConfig(senderID)
	if err != nil || cfg == nil {
		// 无配置或出错，使用默认客户端
		return f.defaultLLM, f.defaultModel, 0, ""
	}

	// 创建用户自定义 LLM 客户端
	client, model := f.createClient(cfg)
	if client == nil {
		return f.defaultLLM, f.defaultModel, 0, ""
	}

	// 缓存客户端
	f.mu.Lock()
	f.clients[senderID] = client
	f.models[senderID] = model
	f.maxContexts[senderID] = cfg.MaxContext
	f.thinkingModes[senderID] = cfg.ThinkingMode
	f.mu.Unlock()

	return client, model, cfg.MaxContext, cfg.ThinkingMode
}

// HasCustomLLM 检查用户是否有自定义 LLM 配置
func (f *LLMFactory) HasCustomLLM(senderID string) bool {
	// 先检查缓存
	if val, ok := f.hasCustomLLMCache.Load(senderID); ok {
		return val.(bool)
	}

	// 再检查客户端缓存
	f.mu.RLock()
	if _, ok := f.clients[senderID]; ok {
		f.mu.RUnlock()
		f.hasCustomLLMCache.Store(senderID, true)
		return true
	}
	f.mu.RUnlock()

	// 从数据库检查
	cfg, err := f.configSvc.GetConfig(senderID)
	if err != nil || cfg == nil {
		f.hasCustomLLMCache.Store(senderID, false)
		return false
	}
	hasCustom := cfg.BaseURL != "" && cfg.APIKey != ""
	f.hasCustomLLMCache.Store(senderID, hasCustom)
	return hasCustom
}

// InvalidateCustomLLMCache 使指定用户的自定义 LLM 缓存失效
func (f *LLMFactory) InvalidateCustomLLMCache(senderID string) {
	f.hasCustomLLMCache.Delete(senderID)
}

// createClient 根据配置创建 LLM 客户端，配置无效时返回 nil
func (f *LLMFactory) createClient(cfg *sqlite.UserLLMConfig) (llm.LLM, string) {
	// 检查必要字段
	if cfg.BaseURL == "" || cfg.APIKey == "" {
		return nil, ""
	}

	model := cfg.Model
	if model == "" {
		model = f.defaultModel
	}

	switch cfg.Provider {
	case "anthropic":
		client := llm.NewAnthropicLLM(llm.AnthropicConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: model,
		})
		return client, model

	default:
		// 其他所有 provider（openai, deepseek, siliconflow 等）都使用 OpenAI 兼容 API
		client := llm.NewOpenAILLM(llm.OpenAIConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: model,
		})
		return client, model
	}
}

// Invalidate 使用户的 LLM 客户端缓存失效（配置更新后调用）
func (f *LLMFactory) Invalidate(senderID string) {
	f.mu.Lock()
	delete(f.clients, senderID)
	delete(f.models, senderID)
	delete(f.maxContexts, senderID)
	delete(f.thinkingModes, senderID)
	f.mu.Unlock()
}

// InvalidateAll 使所有缓存失效
func (f *LLMFactory) InvalidateAll() {
	f.mu.Lock()
	f.clients = make(map[string]llm.LLM)
	f.models = make(map[string]string)
	f.maxContexts = make(map[string]int)
	f.thinkingModes = make(map[string]string)
	f.mu.Unlock()
}

// SetSettingsService 注入 SettingsService（用于读写用户并发配置）。
// 必须在 Agent 初始化后调用，因为 SettingsService 创建依赖于 Agent。
func (f *LLMFactory) SetSettingsService(svc *SettingsService) {
	f.settingsSvc = svc
}

// SetLLMSemaphoreManager 注入 LLMSemaphoreManager。
func (f *LLMFactory) SetLLMSemaphoreManager(mgr *llm.LLMSemaphoreManager) {
	f.llmSemManager = mgr
}

// LLMSemaphoreManager 返回 LLMSemaphoreManager 实例。
func (f *LLMFactory) LLMSemaphoreManager() *llm.LLMSemaphoreManager {
	return f.llmSemManager
}

// ListModels returns available model names from the default LLM client.
func (f *LLMFactory) ListModels() []string {
	return f.defaultLLM.ListModels()
}

// GetLLMConcurrency 读取用户配置的个人 LLM 并发上限。
// 未配置时使用默认值 DefaultLLMConcurrencyPersonal。
func (f *LLMFactory) GetLLMConcurrency(senderID string) int {
	if f.settingsSvc == nil {
		return llm.DefaultLLMConcurrencyPersonal
	}
	settings, err := f.settingsSvc.GetSettings("feishu", senderID)
	if err != nil || settings == nil {
		return llm.DefaultLLMConcurrencyPersonal
	}
	return parseOrDefault(settings["llm_max_concurrent_personal"], llm.DefaultLLMConcurrencyPersonal)
}

// SetLLMConcurrency 设置用户的个人 LLM 并发上限配置。
func (f *LLMFactory) SetLLMConcurrency(senderID string, personal int) error {
	if f.settingsSvc == nil {
		return fmt.Errorf("settings service not available")
	}
	return f.settingsSvc.SetSetting("feishu", senderID, "llm_max_concurrent_personal", fmt.Sprintf("%d", personal))
}

// parseOrDefault 解析字符串为 int，失败时返回默认值。
func parseOrDefault(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	var v int
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil || v <= 0 {
		return defaultVal
	}
	return v
}

// LLMSemAcquireForUser returns an LLMSemAcquire callback for the given user.
// It determines whether the user uses a personal or global LLM and reads
// the corresponding concurrency setting.
// Returns nil if no semaphore manager is configured.
func (f *LLMFactory) LLMSemAcquireForUser(senderID string) func() func() {
	if f.llmSemManager == nil {
		return nil
	}
	llmKey := "global"
	if f.HasCustomLLM(senderID) {
		llmKey = "personal"
	}
	return func() func() {
		personalCap := f.GetLLMConcurrency(senderID)
		cap := llm.DefaultLLMConcurrency
		if llmKey == "personal" {
			cap = personalCap
		}
		return f.llmSemManager.Acquire(context.Background(), senderID, llmKey, func() int { return cap })
	}
}

// SubAgentSemAcquireForUser returns a SubAgentSem callback for the given user.
// SubAgent concurrency is bounded by a separate semaphore (llmKey="subagent").
// Returns nil if no semaphore manager is configured.
func (f *LLMFactory) SubAgentSemAcquireForUser(senderID string) func() func() {
	if f.llmSemManager == nil {
		return nil
	}
	return func() func() {
		// Default max concurrent SubAgents: 3
		cap := parseOrDefault(f.getSetting(senderID, "subagent_max_concurrent"), 3)
		return f.llmSemManager.Acquire(context.Background(), senderID, "subagent", func() int { return cap })
	}
}

// getSetting reads a single user setting. Returns "" on any error.
func (f *LLMFactory) getSetting(senderID, key string) string {
	if f.settingsSvc == nil {
		return ""
	}
	settings, err := f.settingsSvc.GetSettings("feishu", senderID)
	if err != nil || settings == nil {
		return ""
	}
	return settings[key]
}
