package channel

// SettingScope defines where a setting's value is stored and persisted.
type SettingScope int

const (
	ScopeGlobal       SettingScope = iota // Shared across all users, persisted in config.json
	ScopeUser                             // Per-user preference, persisted in user_settings DB
	ScopeSubscription                     // Per-subscription LLM field, persisted in user_llm_subscriptions DB
	ScopeAction                           // UI action trigger, not persisted
)

// SettingDef defines a single setting key — its scope and whether it needs runtime application.
// This is the SINGLE source of truth for all setting keys in the system.
//
// To add a new setting:
//  1. Add a SettingDef entry to AllSettingDefs below
//  2. Add a handler to serverapp/setting_handlers.go (if Runtime=true)
//  3. Add a handler to cmd/xbot-cli/setting_handlers.go (if Runtime=true)
//  4. That's it — all scope maps and key lists are auto-derived.
type SettingDef struct {
	Key     string       // Unique string key used in UI, DB, and RPC
	Scope   SettingScope // Where this setting's value lives
	Runtime bool         // If true, requires runtime apply handler (config + backend side-effect)
}

// AllSettingDefs is the single registry of all known setting keys.
// Every other scope map, runtime key list, and known-key check is derived from this.
var AllSettingDefs = []SettingDef{
	// ── LLM Subscription-scoped fields (persisted in user_llm_subscriptions) ──
	{Key: "llm_provider", Scope: ScopeSubscription},
	{Key: "llm_api_key", Scope: ScopeSubscription},
	{Key: "llm_base_url", Scope: ScopeSubscription},
	{Key: "llm_model", Scope: ScopeSubscription},
	{Key: "max_output_tokens", Scope: ScopeSubscription},
	{Key: "thinking_mode", Scope: ScopeSubscription},

	// ── Global-scoped settings (shared, persisted in config.json) ──
	{Key: "sandbox_mode", Scope: ScopeGlobal, Runtime: true},
	{Key: "compression_threshold", Scope: ScopeGlobal, Runtime: true},
	{Key: "memory_provider", Scope: ScopeGlobal, Runtime: true},
	{Key: "tavily_api_key", Scope: ScopeGlobal, Runtime: true},
	{Key: "enable_stream", Scope: ScopeGlobal},
	{Key: "enable_masking", Scope: ScopeGlobal},
	{Key: "default_user", Scope: ScopeGlobal},
	{Key: "privileged_user", Scope: ScopeGlobal},

	// ── User-scoped settings (per-user, persisted in user_settings DB) ──
	{Key: "theme", Scope: ScopeUser},
	{Key: "language", Scope: ScopeUser},
	{Key: "context_mode", Scope: ScopeUser, Runtime: true},
	{Key: "max_iterations", Scope: ScopeUser, Runtime: true},
	{Key: "max_concurrency", Scope: ScopeUser, Runtime: true},
	{Key: "max_context_tokens", Scope: ScopeUser, Runtime: true},
	{Key: "enable_auto_compress", Scope: ScopeUser, Runtime: true}, // legacy alias for context_mode
	{Key: "runner_server", Scope: ScopeUser},
	{Key: "runner_token", Scope: ScopeUser},
	{Key: "runner_workspace", Scope: ScopeUser},
	{Key: "vanguard_model", Scope: ScopeUser, Runtime: true},
	{Key: "balance_model", Scope: ScopeUser, Runtime: true},
	{Key: "swift_model", Scope: ScopeUser, Runtime: true},

	// ── Action keys (UI triggers, not persisted) ──
	{Key: "subscription_manage", Scope: ScopeAction},
	{Key: "runner_panel", Scope: ScopeAction},
	{Key: "danger_zone", Scope: ScopeAction},
}

// init-time derived indexes — built once, used everywhere.
var (
	allSettingDefsMap map[string]SettingDef
	scopeIndex        map[SettingScope]map[string]struct{}
)

func init() {
	allSettingDefsMap = make(map[string]SettingDef, len(AllSettingDefs))
	scopeIndex = make(map[SettingScope]map[string]struct{})
	for _, s := range AllSettingDefs {
		allSettingDefsMap[s.Key] = s
		if scopeIndex[s.Scope] == nil {
			scopeIndex[s.Scope] = make(map[string]struct{})
		}
		scopeIndex[s.Scope][s.Key] = struct{}{}
	}
}

// GetSettingDef returns the SettingDef for a key, or (SettingDef{}, false) if unknown.
func GetSettingDef(key string) (SettingDef, bool) {
	d, ok := allSettingDefsMap[key]
	return d, ok
}

// SettingScopeOf returns the scope of a setting key. Returns ("unknown") for unrecognized keys.
func SettingScopeOf(key string) string {
	if d, ok := allSettingDefsMap[key]; ok {
		switch d.Scope {
		case ScopeUser:
			return "user"
		case ScopeGlobal:
			return "global"
		case ScopeSubscription:
			return "subscription"
		case ScopeAction:
			return "action"
		}
	}
	return "unknown"
}

// CLIRuntimeSettingKeys lists all setting keys that require runtime application
// beyond DB persistence. Both serverapp and cmd/xbot-cli use this list to verify
// every runtime-affecting key has a handler registered.
//
// Derived from AllSettingDefs — do not edit manually.
var CLIRuntimeSettingKeys []string

func init() {
	for _, d := range AllSettingDefs {
		if d.Runtime {
			CLIRuntimeSettingKeys = append(CLIRuntimeSettingKeys, d.Key)
		}
	}
}

// IsUserScopedSettingKey returns true if the key has ScopeUser.
func IsUserScopedSettingKey(key string) bool {
	_, ok := scopeIndex[ScopeUser][key]
	return ok
}

// IsGlobalScopedSettingKey returns true if the key has ScopeGlobal.
func IsGlobalScopedSettingKey(key string) bool {
	_, ok := scopeIndex[ScopeGlobal][key]
	return ok
}

// IsSubscriptionScopedSettingKey returns true if the key has ScopeSubscription.
func IsSubscriptionScopedSettingKey(key string) bool {
	_, ok := scopeIndex[ScopeSubscription][key]
	return ok
}

// IsActionSettingKey returns true if the key has ScopeAction.
func IsActionSettingKey(key string) bool {
	_, ok := scopeIndex[ScopeAction][key]
	return ok
}
