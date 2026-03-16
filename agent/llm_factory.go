package agent

import (
	"sync"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// LLMFactory 管理用户自定义 LLM 客户端的创建和缓存
type LLMFactory struct {
	configSvc    *sqlite.UserLLMConfigService
	defaultLLM   llm.LLM
	defaultModel string

	// 缓存用户的 LLM 客户端
	mu          sync.RWMutex
	clients     map[string]llm.LLM // senderID -> LLM client
	models      map[string]string  // senderID -> model name
	maxContexts map[string]int     // senderID -> max_context tokens

	// hasCustomLLMCache 缓存用户是否有自定义 LLM 配置（避免频繁查数据库）
	// 使用 sync.Map 保证并发安全
	hasCustomLLMCache sync.Map
}

// NewLLMFactory 创建 LLM 工厂
func NewLLMFactory(configSvc *sqlite.UserLLMConfigService, defaultLLM llm.LLM, defaultModel string) *LLMFactory {
	return &LLMFactory{
		configSvc:    configSvc,
		defaultLLM:   defaultLLM,
		defaultModel: defaultModel,
		clients:      make(map[string]llm.LLM),
		models:       make(map[string]string),
		maxContexts:  make(map[string]int),
		// hasCustomLLMCache 使用零值 sync.Map，无需初始化
	}
}

// GetLLM 获取用户的 LLM 客户端，如果没有自定义配置则返回默认客户端
// 返回: (LLM客户端, 模型名, maxContext)
func (f *LLMFactory) GetLLM(senderID string) (llm.LLM, string, int) {
	// 先检查缓存
	f.mu.RLock()
	if client, ok := f.clients[senderID]; ok {
		model := f.models[senderID]
		maxCtx := f.maxContexts[senderID]
		f.mu.RUnlock()
		return client, model, maxCtx
	}
	f.mu.RUnlock()

	// 从数据库加载配置
	cfg, err := f.configSvc.GetConfig(senderID)
	if err != nil || cfg == nil {
		// 无配置或出错，使用默认客户端
		return f.defaultLLM, f.defaultModel, 0
	}

	// 创建用户自定义 LLM 客户端
	client, model := f.createClient(cfg)
	if client == nil {
		return f.defaultLLM, f.defaultModel, 0
	}

	// 缓存客户端
	f.mu.Lock()
	f.clients[senderID] = client
	f.models[senderID] = model
	f.maxContexts[senderID] = cfg.MaxContext
	f.mu.Unlock()

	return client, model, cfg.MaxContext
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
	case "codebuddy":
		// CodeBuddy 使用专有 API
		client := llm.NewCodeBuddyLLM(llm.CodeBuddyConfig{
			BaseURL:      cfg.BaseURL,
			Token:        cfg.APIKey,
			UserID:       cfg.UserID,
			EnterpriseID: cfg.EnterpriseID,
			Domain:       cfg.Domain,
			DefaultModel: model,
		})
		return client, model

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
	f.mu.Unlock()
}

// InvalidateAll 使所有缓存失效
func (f *LLMFactory) InvalidateAll() {
	f.mu.Lock()
	f.clients = make(map[string]llm.LLM)
	f.models = make(map[string]string)
	f.maxContexts = make(map[string]int)
	f.mu.Unlock()
}
