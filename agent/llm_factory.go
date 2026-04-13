package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

// LLMFactory 管理用户自定义 LLM 客户端的创建和缓存
type LLMFactory struct {
	configSvc           *sqlite.UserLLMConfigService
	subscriptionSvc     *sqlite.LLMSubscriptionService // 多订阅管理
	settingsSvc         *SettingsService               // 用于读写用户并发配置
	defaultLLM          llm.LLM
	defaultModel        string
	defaultThinkingMode string
	tierModels          config.LLMConfig

	// LLMSemaphoreManager 管理 per-tenant LLM 并发信号量
	llmSemManager *llm.LLMSemaphoreManager

	// 缓存用户的 LLM 客户端
	mu              sync.RWMutex
	clients         map[string]llm.LLM // senderID -> LLM client
	models          map[string]string  // senderID -> model name
	maxContexts     map[string]int     // senderID -> max_context tokens
	maxOutputTokens map[string]int     // senderID -> max_output_tokens
	thinkingModes   map[string]string  // senderID -> thinking_mode

	// hasCustomLLMCache 缓存用户是否有自定义 LLM 配置（避免频繁查数据库）
	// 使用 sync.Map 保证并发安全
	hasCustomLLMCache sync.Map
}

// NewLLMFactory 创建 LLM 工厂
func NewLLMFactory(configSvc *sqlite.UserLLMConfigService, defaultLLM llm.LLM, defaultModel string) *LLMFactory {
	return &LLMFactory{
		configSvc:       configSvc,
		defaultLLM:      defaultLLM,
		defaultModel:    defaultModel,
		clients:         make(map[string]llm.LLM),
		models:          make(map[string]string),
		maxContexts:     make(map[string]int),
		maxOutputTokens: make(map[string]int),
		thinkingModes:   make(map[string]string),
		// hasCustomLLMCache 使用零值 sync.Map，无需初始化
	}
}

// SetModelTiers updates the configured tier-to-model mappings used by SubAgent model resolution.
func (f *LLMFactory) SetModelTiers(cfg config.LLMConfig) {
	f.mu.Lock()
	f.tierModels = cfg
	f.mu.Unlock()
}

// GetLLM 获取用户的 LLM 客户端，如果没有自定义配置则返回默认客户端
// 返回: (LLM客户端, 模型名, maxContext, thinkingMode)
//
// 查找优先级:
//  1. 缓存 (configSvc 或 subscriptionSvc 建立的)
//  2. configSvc (user_llm_configs 表，旧的单配置系统)
//  3. subscriptionSvc (user_llm_subscriptions 表，新的多订阅系统，取 default)
//  4. 全局默认 LLM
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

	// 从 configSvc 加载 (旧的单配置系统)
	if f.configSvc != nil {
		cfg, err := f.configSvc.GetConfig(senderID)
		if err == nil && cfg != nil && cfg.BaseURL != "" && cfg.APIKey != "" {
			client, model := f.createClient(cfg)
			if client != nil {
				f.mu.Lock()
				f.clients[senderID] = client
				f.models[senderID] = model
				f.maxContexts[senderID] = cfg.MaxContext
				f.maxOutputTokens[senderID] = cfg.MaxOutputTokens
				f.thinkingModes[senderID] = cfg.ThinkingMode
				f.mu.Unlock()
				return client, model, cfg.MaxContext, cfg.ThinkingMode
			}
		}
	}

	// Fallback 到 subscriptionSvc (新的多订阅系统)
	if f.subscriptionSvc != nil {
		sub, err := f.subscriptionSvc.GetDefault(senderID)
		if err == nil && sub != nil && sub.BaseURL != "" && sub.APIKey != "" {
			client := f.createClientFromSub(sub, sub.Model)
			if client != nil {
				model := sub.Model
				if model == "" {
					model = f.defaultModel
				}
				f.mu.Lock()
				f.clients[senderID] = client
				f.models[senderID] = model
				f.maxContexts[senderID] = sub.MaxContext
				f.maxOutputTokens[senderID] = sub.MaxOutputTokens
				f.thinkingModes[senderID] = sub.ThinkingMode
				f.mu.Unlock()
				return client, model, sub.MaxContext, sub.ThinkingMode
			}
		}
	}

	// 无配置或出错，使用默认客户端
	return f.defaultLLM, f.defaultModel, 0, f.defaultThinkingMode
}

// HasCustomLLM 检查用户是否有自定义 LLM 配置
func (f *LLMFactory) HasCustomLLM(senderID string) bool {
	// 先检查缓存
	if val, ok := f.hasCustomLLMCache.Load(senderID); ok {
		if b, ok := val.(bool); ok {
			return b
		}
		return false
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

// SetSubscriptionSvc sets the subscription service (optional, for multi-subscription support).
func (f *LLMFactory) SetSubscriptionSvc(svc *sqlite.LLMSubscriptionService) {
	f.subscriptionSvc = svc
}

// GetSubscriptionSvc returns the subscription service.
func (f *LLMFactory) GetSubscriptionSvc() *sqlite.LLMSubscriptionService {
	return f.subscriptionSvc
}

// GetDefaultModel returns the default model name.
func (f *LLMFactory) GetDefaultModel() string {
	return f.defaultModel
}

// SwitchSubscription switches a user's active LLM to the specified subscription.
// It creates a new LLM client from the subscription config and caches it.
func (f *LLMFactory) SwitchSubscription(senderID string, sub *sqlite.LLMSubscription) error {
	cfg := &sqlite.UserLLMConfig{
		Provider:     sub.Provider,
		BaseURL:      sub.BaseURL,
		APIKey:       sub.APIKey,
		Model:        sub.Model,
		MaxContext:   0,
		ThinkingMode: "",
	}
	client, model := f.createClient(cfg)
	if client == nil {
		return fmt.Errorf("failed to create LLM client for subscription %s", sub.ID)
	}

	f.mu.Lock()
	f.clients[senderID] = client
	f.models[senderID] = model
	f.maxContexts[senderID] = 0
	f.thinkingModes[senderID] = ""
	f.mu.Unlock()

	f.hasCustomLLMCache.Store(senderID, true)
	return nil
}

// SwitchModel switches a user's active model without changing the subscription/LLM client.
// Works by invalidating the cache so next GetLLM call picks up the new model from DB.
func (f *LLMFactory) SwitchModel(senderID, model string) {
	f.mu.Lock()
	if _, ok := f.clients[senderID]; ok {
		// User has a custom client — update model in cache
		f.models[senderID] = model
	} else {
		// User uses default client — update default model
		f.defaultModel = model
	}
	f.mu.Unlock()
}

// SetDefaults 更新默认 LLM 客户端和模型名。
// 用于 setup/settings 面板修改全局 LLM 配置后立即生效。
func (f *LLMFactory) SetDefaults(newLLM llm.LLM, newModel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaultLLM = newLLM
	f.defaultModel = newModel
	// 清除所有用户缓存，让后续 GetLLM 重新创建客户端
	f.clients = make(map[string]llm.LLM)
	f.models = make(map[string]string)
	f.maxContexts = make(map[string]int)
	f.maxOutputTokens = make(map[string]int)
	f.thinkingModes = make(map[string]string)
}

// SetDefaultThinkingMode sets the default thinking mode for users without custom config.
// Used by CLI mode where there's no DB-backed configSvc.
func (f *LLMFactory) SetDefaultThinkingMode(mode string) {
	f.mu.Lock()
	f.defaultThinkingMode = mode
	// Clear cached thinkingModes so GetLLM picks up the new default
	f.thinkingModes = make(map[string]string)
	f.mu.Unlock()
}

// SetProxyLLM sets a ProxyLLM for a user (used when their active runner has local LLM).
// This overrides any per-user LLM config for this sender.
func (f *LLMFactory) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clients[senderID] = proxy
	f.models[senderID] = model
	f.maxContexts[senderID] = 0
	f.maxOutputTokens[senderID] = 0
	f.thinkingModes[senderID] = ""
}

// ClearProxyLLM removes a ProxyLLM for a user (runner disconnected or local LLM disabled).
func (f *LLMFactory) ClearProxyLLM(senderID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.clients, senderID)
	delete(f.models, senderID)
	delete(f.maxContexts, senderID)
	delete(f.thinkingModes, senderID)
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
			MaxTokens:    cfg.MaxOutputTokens,
		})
		return client, model

	default:
		// 其他所有 provider（openai, deepseek, siliconflow 等）都使用 OpenAI 兼容 API
		maxTokens := cfg.MaxOutputTokens
		client := llm.NewOpenAILLM(llm.OpenAIConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: model,
			MaxTokens:    maxTokens,
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
	delete(f.maxOutputTokens, senderID)
	delete(f.thinkingModes, senderID)
	f.mu.Unlock()
}

// InvalidateAll 使所有缓存失效
func (f *LLMFactory) InvalidateAll() {
	f.mu.Lock()
	f.clients = make(map[string]llm.LLM)
	f.models = make(map[string]string)
	f.maxContexts = make(map[string]int)
	f.maxOutputTokens = make(map[string]int)
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

// ListAllModelsForUser returns model names from the default LLM plus all subscription
// Model fields for a given user. Used for global tier settings where the user should
// see models across all their subscriptions.
func (f *LLMFactory) ListAllModelsForUser(senderID string) []string {
	seen := make(map[string]bool)
	var result []string

	// Default LLM models
	for _, m := range f.defaultLLM.ListModels() {
		if !seen[m] {
			seen[m] = true
			result = append(result, m)
		}
	}

	// All subscription model fields (no API calls, just DB records)
	if f.subscriptionSvc != nil && senderID != "" {
		if subs, err := f.subscriptionSvc.List(senderID); err == nil {
			for _, sub := range subs {
				if sub.Model != "" && !seen[sub.Model] {
					seen[sub.Model] = true
					result = append(result, sub.Model)
				}
			}
		}
	}

	return result
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

// GetMaxOutputTokens returns the user's configured max_output_tokens (0 = default).
// Uses the per-user cache populated by GetLLM(); no DB hit.
func (f *LLMFactory) GetMaxOutputTokens(senderID string) int {
	f.mu.RLock()
	if v, ok := f.maxOutputTokens[senderID]; ok {
		f.mu.RUnlock()
		return v
	}
	f.mu.RUnlock()
	// User has no cached config (using default client) — return 0 (use default)
	return 0
}

// GetLLMForModel 获取指定模型的 LLM 客户端，用于 SubAgent 使用不同于主 Agent 的模型。
//
// 查找优先级：
//  1. 在用户所有订阅中查找 Model 字段精确匹配 targetModel 的订阅
//  2. 使用当前活跃订阅的凭证 + targetModel
//  3. 使用任意订阅的凭证 + targetModel（优先 Provider 匹配）
//  4. Fallback 到主 Agent 的当前 LLM（忽略 targetModel）
//
// 返回: (LLM客户端, 实际模型名, maxContext, thinkingMode, 是否使用了非默认模型)
func (f *LLMFactory) GetLLMForModel(senderID, targetModel string) (llm.LLM, string, int, string, bool) {
	resolvedModel, _ := f.resolveTierModel(targetModel)
	if resolvedModel == "" {
		// 无指定模型 → 使用默认
		client, model, maxCtx, tm := f.GetLLM(senderID)
		return client, model, maxCtx, tm, false
	}

	// Tier resolved or explicit model name specified — try subscription matching first
	if f.subscriptionSvc != nil {
		subs, err := f.subscriptionSvc.List(senderID)
		if err == nil && len(subs) > 0 {
			// 1. 精确匹配：订阅的 Model 字段 == resolvedModel
			for _, sub := range subs {
				if sub.Model == resolvedModel {
					client := f.createClientFromSub(sub, resolvedModel)
					if client != nil {
						return client, resolvedModel, sub.MaxContext, sub.ThinkingMode, true
					}
				}
			}

			// 2. 使用活跃订阅的凭证 + resolvedModel
			activeSub, err := f.subscriptionSvc.GetDefault(senderID)
			if err == nil && activeSub != nil {
				client := f.createClientFromSub(activeSub, resolvedModel)
				if client != nil {
					return client, resolvedModel, activeSub.MaxContext, activeSub.ThinkingMode, true
				}
			}

			// 3. Provider 匹配：找 provider 能服务该模型的订阅
			provider := guessProvider(resolvedModel)
			for _, sub := range subs {
				if provider != "" && sub.Provider == provider {
					client := f.createClientFromSub(sub, resolvedModel)
					if client != nil {
						return client, resolvedModel, sub.MaxContext, sub.ThinkingMode, true
					}
				}
			}

			// 4. 任意可用订阅
			for _, sub := range subs {
				client := f.createClientFromSub(sub, resolvedModel)
				if client != nil {
					return client, resolvedModel, sub.MaxContext, sub.ThinkingMode, true
				}
			}
		}
	}

	// Fallback: use default client with resolved model name (works for CLI mode without subscriptions)
	client, _, maxCtx, tm := f.GetLLM(senderID)
	return client, resolvedModel, maxCtx, tm, true
}

// createClientFromSub 从订阅创建 LLM 客户端，使用指定的模型名（而非订阅的默认模型）
func (f *LLMFactory) createClientFromSub(sub *sqlite.LLMSubscription, model string) llm.LLM {
	if sub.BaseURL == "" || sub.APIKey == "" {
		return nil
	}
	cfg := &sqlite.UserLLMConfig{
		Provider:        sub.Provider,
		BaseURL:         sub.BaseURL,
		APIKey:          sub.APIKey,
		Model:           model,
		MaxOutputTokens: sub.MaxOutputTokens,
	}
	client, _ := f.createClient(cfg)
	return client
}

func normalizeModelTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "vanguard", "strong":
		return "vanguard"
	case "balance", "medium":
		return "balance"
	case "swift", "weak":
		return "swift"
	default:
		return ""
	}
}

func (f *LLMFactory) resolveTierModel(value string) (string, bool) {
	tier := normalizeModelTier(value)
	if tier == "" {
		return value, false
	}

	f.mu.RLock()
	tiers := f.tierModels
	f.mu.RUnlock()

	switch tier {
	case "vanguard":
		if strings.TrimSpace(tiers.VanguardModel) != "" {
			return strings.TrimSpace(tiers.VanguardModel), true
		}
	case "balance":
		if strings.TrimSpace(tiers.BalanceModel) != "" {
			return strings.TrimSpace(tiers.BalanceModel), true
		}
	case "swift":
		if strings.TrimSpace(tiers.SwiftModel) != "" {
			return strings.TrimSpace(tiers.SwiftModel), true
		}
	}
	return "", true
}

// guessProvider 根据模型名猜测 provider。
// 返回空字符串表示无法猜测。
func guessProvider(model string) string {
	switch {
	case strings.Contains(model, "claude"):
		return "anthropic"
	case strings.Contains(model, "gpt") || strings.Contains(model, "o1") || strings.Contains(model, "o3") || strings.Contains(model, "chatgpt"):
		return "openai"
	case strings.Contains(model, "deepseek"):
		return "deepseek"
	case strings.Contains(model, "gemini"):
		return "google"
	case strings.Contains(model, "qwen"):
		return "qwen"
	default:
		return ""
	}
}
