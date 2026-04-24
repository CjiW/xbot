package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/event"
	llm "xbot/llm"
	log "xbot/logger"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// LocalBackend runs the agent in-process. It wraps an agent.Agent directly,
// delegating all methods to the underlying Agent.
//
// Outbound messages flow through the MessageBus as usual — the caller
// should set up a Dispatcher to route them to channels.
type LocalBackend struct {
	agent *Agent
	bus   *bus.MessageBus
}

// NewLocalBackend creates a LocalBackend with the given agent config.
// It calls agent.New() internally, so all initialization (tools, sessions, etc.)
// is complete by the time this returns.
func NewLocalBackend(cfg Config) (*LocalBackend, error) {
	a, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &LocalBackend{
		agent: a,
		bus:   cfg.Bus,
	}, nil
}

// Agent returns the underlying *Agent for direct access when needed
// (e.g., for main.go to inject dependencies before Start).
func (b *LocalBackend) Agent() *Agent {
	return b.agent
}

func (b *LocalBackend) Start(ctx context.Context) error {
	go b.agent.Run(ctx)
	return nil
}

func (b *LocalBackend) Stop() {
	if err := b.agent.Close(); err != nil {
		_ = err // best effort
	}
}

func (b *LocalBackend) SendInbound(msg bus.InboundMessage) error {
	select {
	case b.bus.Inbound <- msg:
		return nil
	default:
		return fmt.Errorf("inbound channel full, message dropped")
	}
}

// OnOutbound is a no-op for LocalBackend: outbound messages are handled
// by the Dispatcher + Channel wiring set up by the caller.
// For RemoteBackend, this registers the callback for WS-delivered replies.
func (b *LocalBackend) OnOutbound(_ func(bus.OutboundMessage)) {
	// no-op: LocalBackend uses Dispatcher for outbound routing
}

func (b *LocalBackend) Bus() *bus.MessageBus { return b.bus }

func (b *LocalBackend) IsRemote() bool { return false }

// IsProcessing returns true if there is an active agent turn for the given chat.
func (b *LocalBackend) IsProcessing(ch, chatID string) bool {
	prefix := ch + ":" + chatID + ":"
	found := false
	b.agent.chatCancelCh.Range(func(key, _ interface{}) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, prefix) {
			found = true
			return false
		}
		return true
	})
	return found
}

// GetActiveProgress returns the latest progress snapshot for an active turn,
// including completed iteration history for mid-session reconnect.
func (b *LocalBackend) GetActiveProgress(ch, chatID string) *channel.CLIProgressPayload {
	key := ch + ":" + chatID
	v, ok := b.agent.lastProgressSnapshot.Load(key)
	if !ok {
		log.WithField("key", key).Info("GetActiveProgress: no snapshot found")
		return nil
	}
	snapshot := v.(*channel.CLIProgressPayload)
	log.WithFields(log.Fields{
		"key":       key,
		"phase":     snapshot.Phase,
		"iteration": snapshot.Iteration,
		"active":    len(snapshot.ActiveTools),
		"completed": len(snapshot.CompletedTools),
	}).Info("GetActiveProgress: snapshot found")
	// Attach iteration history if available
	if histPtr, ok := b.agent.iterationHistories.Load(key); ok {
		hist := *histPtr.(*[]channel.CLIProgressPayload)
		if len(hist) > 0 {
			// Clone to avoid mutating shared state
			result := *snapshot
			result.IterationHistory = make([]channel.CLIProgressPayload, len(hist))
			copy(result.IterationHistory, hist)
			return &result
		}
	}
	return snapshot
}

// OnProgress is a no-op for LocalBackend: progress flows through the
// Dispatcher → CLIChannel.SendProgress path directly.
func (b *LocalBackend) OnProgress(_ func(*channel.CLIProgressPayload)) {}

func (b *LocalBackend) LLMFactory() *LLMFactory {
	return b.agent.LLMFactory()
}

func (b *LocalBackend) SettingsService() *SettingsService {
	return b.agent.SettingsService()
}

func (b *LocalBackend) MultiSession() *session.MultiTenantSession {
	return b.agent.MultiSession()
}

func (b *LocalBackend) BgTaskManager() *tools.BackgroundTaskManager {
	return b.agent.BgTaskManager()
}

func (b *LocalBackend) ToolHookChain() *tools.HookChain {
	return b.agent.ToolHookChain()
}

func (b *LocalBackend) SetDirectSend(fn func(bus.OutboundMessage) (string, error)) {
	b.agent.SetDirectSend(fn)
}

func (b *LocalBackend) SetChannelFinder(fn func(name string) (channel.Channel, bool)) {
	b.agent.SetChannelFinder(fn)
}

func (b *LocalBackend) SetChannelPromptProviders(providers ...ChannelPromptProvider) {
	b.agent.SetChannelPromptProviders(providers...)
}

func (b *LocalBackend) RegisterCoreTool(tool tools.Tool) {
	b.agent.RegisterCoreTool(tool)
}

func (b *LocalBackend) IndexGlobalTools() {
	b.agent.IndexGlobalTools()
}

func (b *LocalBackend) CountInteractiveSessions(channelName, chatID string) int {
	return b.agent.CountInteractiveSessions(channelName, chatID)
}

func (b *LocalBackend) ListInteractiveSessions(channelName, chatID string) []InteractiveSessionInfo {
	return b.agent.ListInteractiveSessions(channelName, chatID)
}

func (b *LocalBackend) InspectInteractiveSession(ctx context.Context, roleName, channelName, chatID, instance string, tailCount int) (string, error) {
	return b.agent.InspectInteractiveSession(ctx, roleName, channelName, chatID, instance, tailCount)
}

func (b *LocalBackend) GetSessionMessages(channelName, chatID, roleName, instance string) ([]SessionMessage, bool) {
	return b.agent.GetSessionMessages(channelName, chatID, roleName, instance)
}

func (b *LocalBackend) GetAgentSessionDump(channelName, chatID, roleName, instance string) (*AgentSessionDump, bool) {
	return b.agent.GetAgentSessionDump(channelName, chatID, roleName, instance)
}

func (b *LocalBackend) GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool) {
	return b.agent.GetAgentSessionDumpByFullKey(fullKey)
}

func (b *LocalBackend) SetContextMode(mode string) error {
	return b.agent.SetContextMode(mode)
}

func (b *LocalBackend) SetCWD(ch, chatID, dir string) error {
	// CWD sync only makes sense when sandbox is "none" — CLI and server share
	// the same filesystem. In docker/remote mode, host paths don't map to the
	// sandbox environment.
	if b.agent.sandboxMode != "none" {
		return fmt.Errorf("CWD sync not supported in %s sandbox mode", b.agent.sandboxMode)
	}
	if b.agent.MultiSession() == nil {
		return fmt.Errorf("no session manager")
	}
	sess, err := b.agent.MultiSession().GetOrCreateSession(ch, chatID)
	if err != nil {
		return err
	}
	sess.SetCurrentDir(dir)
	return nil
}

func (b *LocalBackend) SetMaxIterations(n int) {
	b.agent.SetMaxIterations(n)
}

func (b *LocalBackend) SetMaxConcurrency(n int) {
	b.agent.SetMaxConcurrency(n)
}

func (b *LocalBackend) SetMaxContextTokens(n int) {
	b.agent.SetMaxContextTokens(n)
}

func (b *LocalBackend) SetSandbox(sb tools.Sandbox, mode string) {
	b.agent.SetSandbox(sb, mode)
}

func (b *LocalBackend) GetCardBuilder() *tools.CardBuilder {
	return b.agent.GetCardBuilder()
}

func (b *LocalBackend) SetEventRouter(router *event.Router) {
	b.agent.SetEventRouter(router)
}

// --- Extended methods (delegated to b.agent) ---

func (b *LocalBackend) RegisterTool(tool tools.Tool) {
	b.agent.RegisterTool(tool)
}

func (b *LocalBackend) RegistryManager() *RegistryManager {
	return b.agent.RegistryManager()
}

func (b *LocalBackend) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	b.agent.SetProxyLLM(senderID, proxy, model)
}

func (b *LocalBackend) ClearProxyLLM(senderID string) {
	b.agent.ClearProxyLLM(senderID)
}

func (b *LocalBackend) GetDefaultModel() string {
	return b.agent.GetDefaultModel()
}

func (b *LocalBackend) SetUserModel(senderID, model string) error {
	return b.agent.SetUserModel(senderID, model)
}

func (b *LocalBackend) SwitchModel(senderID, model string) error {
	b.agent.llmFactory.SwitchModel(senderID, model)
	return nil
}

func (b *LocalBackend) GetUserMaxContext(senderID string) int {
	return b.agent.GetUserMaxContext(senderID)
}

func (b *LocalBackend) SetUserMaxContext(senderID string, maxContext int) error {
	return b.agent.SetUserMaxContext(senderID, maxContext)
}

func (b *LocalBackend) GetUserMaxOutputTokens(senderID string) int {
	return b.agent.GetUserMaxOutputTokens(senderID)
}

func (b *LocalBackend) SetUserMaxOutputTokens(senderID string, maxTokens int) error {
	if err := b.agent.SetUserMaxOutputTokens(senderID, maxTokens); err != nil {
		// LLMConfig may not exist (e.g. remote CLI user using server default) —
		// fallback to updating the factory cache directly.
		// Still apply basic validation.
		if maxTokens < 0 {
			return fmt.Errorf("max_output_tokens must be >= 0, got %d", maxTokens)
		}
		b.agent.llmFactory.SetUserMaxOutputTokens(senderID, maxTokens)
	}
	return nil
}

func (b *LocalBackend) GetUserThinkingMode(senderID string) string {
	return b.agent.GetUserThinkingMode(senderID)
}

func (b *LocalBackend) SetUserThinkingMode(senderID string, mode string) error {
	validModes := map[string]bool{"": true, "enabled": true, "disabled": true, "auto": true}
	if !validModes[mode] {
		return fmt.Errorf("invalid thinking_mode: %q", mode)
	}
	if err := b.agent.SetUserThinkingMode(senderID, mode); err != nil {
		b.agent.llmFactory.SetUserThinkingMode(senderID, mode)
	}
	return nil
}

func (b *LocalBackend) GetLLMConcurrency(senderID string) int {
	return b.agent.GetLLMConcurrency(senderID)
}

func (b *LocalBackend) SetLLMConcurrency(senderID string, personal int) error {
	return b.agent.SetLLMConcurrency(senderID, personal)
}

func (b *LocalBackend) GetContextMode() string {
	return b.agent.GetContextMode()
}

// --- Extended RPC methods (delegate to local services) ---

func (b *LocalBackend) GetSettings(namespace, senderID string) (map[string]string, error) {
	if b.agent.settingsSvc == nil {
		return nil, fmt.Errorf("settings service not available")
	}
	return b.agent.settingsSvc.GetSettings(namespace, senderID)
}

func (b *LocalBackend) SetSetting(namespace, senderID, key, value string) error {
	if b.agent.settingsSvc == nil {
		return fmt.Errorf("settings service not available")
	}
	return b.agent.settingsSvc.SetSetting(namespace, senderID, key, value)
}

func (b *LocalBackend) ListModels() []string {
	return b.agent.llmFactory.ListModels()
}

func (b *LocalBackend) ListAllModels() []string {
	return b.agent.llmFactory.ListAllModelsForUser("")
}

func (b *LocalBackend) SetModelTiers(cfg config.LLMConfig) error {
	b.agent.llmFactory.SetModelTiers(cfg)
	return nil
}

func (b *LocalBackend) SetDefaultThinkingMode(mode string) error {
	b.agent.llmFactory.SetDefaultThinkingMode(mode)
	return nil
}

func (b *LocalBackend) ClearMemory(ctx context.Context, ch, chatID, targetType, senderID string) error {
	if b.agent.multiSession == nil {
		return nil
	}
	return b.agent.multiSession.ClearMemory(ctx, ch, chatID, targetType, senderID)
}

func (b *LocalBackend) GetMemoryStats(ctx context.Context, ch, chatID, senderID string) map[string]string {
	if b.agent.multiSession == nil {
		return nil
	}
	return b.agent.multiSession.GetMemoryStats(ctx, ch, chatID, senderID)
}

func (b *LocalBackend) GetUserTokenUsage(senderID string) (map[string]any, error) {
	if b.agent.multiSession == nil {
		return nil, nil
	}
	usage, err := b.agent.multiSession.GetUserTokenUsage(senderID)
	if err != nil || usage == nil {
		return nil, err
	}
	return map[string]any{
		"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
		"total_tokens": usage.TotalTokens, "cached_tokens": usage.CachedTokens,
		"conversation_count": usage.ConversationCount, "llm_call_count": usage.LLMCallCount,
	}, nil
}

func (b *LocalBackend) GetDailyTokenUsage(senderID string, days int) ([]map[string]any, error) {
	if b.agent.multiSession == nil {
		return nil, nil
	}
	daily, err := b.agent.multiSession.GetDailyTokenUsage(senderID, days)
	if err != nil || daily == nil {
		return nil, err
	}
	result := make([]map[string]any, len(daily))
	for i, d := range daily {
		result[i] = map[string]any{
			"date": d.Date, "model": d.Model,
			"input_tokens": d.InputTokens, "output_tokens": d.OutputTokens,
			"cached_tokens":      d.CachedTokens,
			"conversation_count": d.ConversationCount, "llm_call_count": d.LLMCallCount,
		}
	}
	return result, nil
}

func (b *LocalBackend) GetBgTaskCount(sessionKey string) int {
	if b.agent.bgTaskMgr == nil {
		return 0
	}
	return len(b.agent.bgTaskMgr.ListRunning(sessionKey))
}

func (b *LocalBackend) ListBgTasks(sessionKey string) ([]BgTaskJSON, error) {
	if b.agent.bgTaskMgr == nil {
		return nil, nil
	}
	// Return all tasks (running + done + error) for the task panel.
	tasks := b.agent.bgTaskMgr.ListAllForSession(sessionKey)
	result := make([]BgTaskJSON, len(tasks))
	for i, t := range tasks {
		result[i] = BgTaskJSON{
			ID:        t.ID,
			Command:   t.Command,
			Status:    string(t.Status),
			StartedAt: t.StartedAt.Format(time.RFC3339),
			ExitCode:  t.ExitCode,
			Output:    t.Output,
			Error:     t.Error,
		}
		if t.FinishedAt != nil {
			result[i].FinishedAt = t.FinishedAt.Format(time.RFC3339)
		}
	}
	return result, nil
}

func (b *LocalBackend) KillBgTask(taskID string) error {
	if b.agent.bgTaskMgr == nil {
		return fmt.Errorf("background tasks not available")
	}
	return b.agent.bgTaskMgr.Kill(taskID)
}

func (b *LocalBackend) CleanupCompletedBgTasks(sessionKey string) {
	if b.agent.bgTaskMgr != nil {
		b.agent.bgTaskMgr.RemoveCompletedTasks(sessionKey)
	}
}

func (b *LocalBackend) ListTenants() ([]TenantInfo, error) {
	if b.agent.multiSession == nil {
		return nil, nil
	}
	db := b.agent.multiSession.DB()
	if db == nil {
		return nil, nil
	}
	tenantSvc := sqlite.NewTenantService(db)
	tenants, err := tenantSvc.ListTenants()
	if err != nil {
		return nil, err
	}
	result := make([]TenantInfo, len(tenants))
	for i, t := range tenants {
		result[i] = TenantInfo{
			ID:           t.ID,
			Channel:      t.Channel,
			ChatID:       t.ChatID,
			CreatedAt:    t.CreatedAt.Format(time.RFC3339),
			LastActiveAt: t.LastActiveAt.Format(time.RFC3339),
		}
	}
	return result, nil
}

func (b *LocalBackend) ListSubscriptions(senderID string) ([]channel.Subscription, error) {
	svc := b.agent.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return nil, nil
	}
	subs, err := svc.List(senderID)
	if err != nil || subs == nil {
		return nil, err
	}
	result := make([]channel.Subscription, len(subs))
	for i, s := range subs {
		result[i] = channel.Subscription{
			ID: s.ID, Name: s.Name, Provider: s.Provider,
			BaseURL: s.BaseURL, APIKey: s.APIKey,
			Model: s.Model, Active: s.IsDefault,
			MaxOutputTokens: s.MaxOutputTokens, ThinkingMode: s.ThinkingMode,
		}
	}
	return result, nil
}

func (b *LocalBackend) GetDefaultSubscription(senderID string) (*channel.Subscription, error) {
	svc := b.agent.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return nil, nil
	}
	sub, err := svc.GetDefault(senderID)
	if err != nil || sub == nil {
		return nil, err
	}
	return &channel.Subscription{
		ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
		BaseURL: sub.BaseURL, APIKey: sub.APIKey,
		Model: sub.Model, Active: sub.IsDefault,
		MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
	}, nil
}

func (b *LocalBackend) AddSubscription(senderID string, sub channel.Subscription) error {
	svc := b.agent.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return fmt.Errorf("subscription service not available")
	}
	if err := svc.Add(&sqlite.LLMSubscription{
		ID: sub.ID, SenderID: senderID, Name: sub.Name,
		Provider: sub.Provider, BaseURL: sub.BaseURL, APIKey: sub.APIKey,
		Model: sub.Model, IsDefault: sub.Active,
	}); err != nil {
		return err
	}
	b.agent.llmFactory.Invalidate(senderID)
	return nil
}

func (b *LocalBackend) RemoveSubscription(id string) error {
	svc := b.agent.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return fmt.Errorf("subscription service not available")
	}
	sub, err := svc.Get(id)
	if err != nil {
		return err
	}
	if err := svc.Remove(id); err != nil {
		return err
	}
	if sub != nil {
		b.agent.llmFactory.Invalidate(sub.SenderID)
	}
	return nil
}

func (b *LocalBackend) SetDefaultSubscription(id string, chatID string) error {
	svc := b.agent.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return fmt.Errorf("subscription service not available")
	}
	if err := svc.SetDefault(id); err != nil {
		return err
	}
	sub, err := svc.Get(id)
	if err == nil && sub != nil {
		b.agent.llmFactory.Invalidate(sub.SenderID)
		if err := b.agent.llmFactory.SwitchSubscription(sub.SenderID, sub, chatID); err != nil {
			return err
		}
	}
	return nil
}

func (b *LocalBackend) RenameSubscription(id, name string) error {
	svc := b.agent.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return fmt.Errorf("subscription service not available")
	}
	return svc.Rename(id, name)
}

func (b *LocalBackend) UpdateSubscription(id string, sub channel.Subscription) error {
	svc := b.agent.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return fmt.Errorf("subscription service not available")
	}
	existing, err := svc.Get(id)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("subscription %s not found", id)
	}
	dbSub := &sqlite.LLMSubscription{
		ID:              id,
		SenderID:        existing.SenderID,
		Name:            sub.Name,
		Provider:        sub.Provider,
		BaseURL:         sub.BaseURL,
		APIKey:          sub.APIKey,
		Model:           sub.Model,
		MaxContext:      existing.MaxContext,
		MaxOutputTokens: existing.MaxOutputTokens,
		ThinkingMode:    existing.ThinkingMode,
		IsDefault:       sub.Active,
	}
	if err := svc.Update(dbSub); err != nil {
		return err
	}
	b.agent.llmFactory.Invalidate(existing.SenderID)
	return nil
}

func (b *LocalBackend) SetSubscriptionModel(id, model string) error {
	svc := b.agent.llmFactory.GetSubscriptionSvc()
	if svc == nil {
		return fmt.Errorf("subscription service not available")
	}
	sub, err := svc.Get(id)
	if err != nil {
		return err
	}
	if err := svc.SetModel(id, model); err != nil {
		return err
	}
	if sub != nil {
		b.agent.llmFactory.Invalidate(sub.SenderID)
	}
	return nil
}

func (b *LocalBackend) GetHistory(ch, chatID string) ([]channel.HistoryMessage, error) {
	ms := b.agent.MultiSession()
	if ms == nil {
		return nil, fmt.Errorf("multi-session not available")
	}
	sess, err := ms.GetOrCreateSession(ch, chatID)
	if err != nil {
		return nil, err
	}
	msgs, err := sess.GetMessages()
	if err != nil {
		return nil, err
	}
	return channel.ConvertMessagesToHistory(msgs), nil
}

func (b *LocalBackend) TrimHistory(ch, chatID string, cutoff time.Time) error {
	ms := b.agent.MultiSession()
	if ms == nil {
		return fmt.Errorf("multi-session not available")
	}
	return ms.TrimHistory(ch, chatID, cutoff)
}

func (b *LocalBackend) Close() error {
	return b.agent.Close()
}

func (b *LocalBackend) ResetTokenState() {
	// No-op: token state is per-tenant in DB (tenant_state table), not a
	// global variable. Clearing it would require knowing the current tenant.
	// /rewind already clears token state via TrimHistory → SetTokenState(0,0).
}

func (b *LocalBackend) GetChannelConfigs() (map[string]map[string]string, error) {
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil {
		return nil, fmt.Errorf("config not found")
	}
	result := make(map[string]map[string]string)
	result["web"] = map[string]string{
		"enable": strconv.FormatBool(cfg.Web.Enable),
		"host":   cfg.Web.Host,
		"port":   strconv.Itoa(cfg.Web.Port),
	}
	result["feishu"] = map[string]string{
		"enabled":            strconv.FormatBool(cfg.Feishu.Enabled),
		"app_id":             cfg.Feishu.AppID,
		"app_secret":         cfg.Feishu.AppSecret,
		"encrypt_key":        cfg.Feishu.EncryptKey,
		"verification_token": cfg.Feishu.VerificationToken,
		"domain":             cfg.Feishu.Domain,
	}
	result["qq"] = map[string]string{
		"enabled":       strconv.FormatBool(cfg.QQ.Enabled),
		"app_id":        cfg.QQ.AppID,
		"client_secret": cfg.QQ.ClientSecret,
	}
	result["napcat"] = map[string]string{
		"enabled": strconv.FormatBool(cfg.NapCat.Enabled),
		"ws_url":  cfg.NapCat.WSUrl,
		"token":   cfg.NapCat.Token,
	}
	return result, nil
}

func (b *LocalBackend) SetChannelConfig(channel string, values map[string]string) error {
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil {
		cfg = &config.Config{}
	}
	switch channel {
	case "web":
		if v, ok := values["enable"]; ok {
			cfg.Web.Enable, _ = strconv.ParseBool(v)
		}
		if v, ok := values["host"]; ok {
			cfg.Web.Host = v
		}
		if v, ok := values["port"]; ok {
			cfg.Web.Port, _ = strconv.Atoi(v)
		}
	case "feishu":
		if v, ok := values["enabled"]; ok {
			cfg.Feishu.Enabled, _ = strconv.ParseBool(v)
		}
		if v, ok := values["app_id"]; ok {
			cfg.Feishu.AppID = v
		}
		if v, ok := values["app_secret"]; ok {
			cfg.Feishu.AppSecret = v
		}
		if v, ok := values["encrypt_key"]; ok {
			cfg.Feishu.EncryptKey = v
		}
		if v, ok := values["verification_token"]; ok {
			cfg.Feishu.VerificationToken = v
		}
		if v, ok := values["domain"]; ok {
			cfg.Feishu.Domain = v
		}
	case "qq":
		if v, ok := values["enabled"]; ok {
			cfg.QQ.Enabled, _ = strconv.ParseBool(v)
		}
		if v, ok := values["app_id"]; ok {
			cfg.QQ.AppID = v
		}
		if v, ok := values["client_secret"]; ok {
			cfg.QQ.ClientSecret = v
		}
	case "napcat":
		if v, ok := values["enabled"]; ok {
			cfg.NapCat.Enabled, _ = strconv.ParseBool(v)
		}
		if v, ok := values["ws_url"]; ok {
			cfg.NapCat.WSUrl = v
		}
		if v, ok := values["token"]; ok {
			cfg.NapCat.Token = v
		}
	default:
		return fmt.Errorf("unknown channel: %s", channel)
	}
	return config.SaveToFile(config.ConfigFilePath(), cfg)
}

func (b *LocalBackend) Run(ctx context.Context) error {
	return b.agent.Run(ctx)
}
