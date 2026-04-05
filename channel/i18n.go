package channel

// UILocale holds all UI strings for a given language.
type UILocale struct {
	// --- A. System messages ---
	CancelSent     string // "已发送取消请求"
	SettingsSaved  string // "✅ 设置已保存"
	NoSettings     string // "当前渠道没有可配置的设置项。"
	CheckingUpdate string // "正在检查更新..."
	ModelUsage     string // "用法: /model <模型名>\n使用 /models 查看可用模型"
	AskCancelled   string // "已取消提问"
	SetupComplete  string // "✅ 初始配置完成，可以开始使用了。随时用 /settings 修改配置，/setup 重新引导。"
	SetupLettaNote string // "[!] letta memory mode requires embedding service:\n  1. ..."
	UpdateFound    string // "发现新版本: %s → %s\n升级命令: ..."
	UpdateCurrent  string // "当前版本 %s 已是最新"
	UpdateFailed   string // "更新检查失败（网络超时或无法连接 GitHub API）"

	// --- B. Panel text ---
	PanelSettingsTitle   string // "⚙ Settings"
	PanelNotSet          string // "(未设置)"
	PanelEditHint        string // "Enter confirm | Esc cancel"
	PanelComboHint       string // "Up/Down select | Enter confirm | Type custom | Esc cancel"
	PanelNavHint         string // "↑↓ 导航 · Enter 编辑/切换 · Ctrl+S 保存 · Esc 关闭"
	PanelEditPlaceholder string // "输入新值..."
	PanelToggleOn        string // "● ON"
	PanelToggleOff       string // "○ OFF"

	PanelOther      string // "Other: "
	PanelSubmit     string // "Submit →"
	PanelAskNav     string // "←→/Tab 切换问题"
	PanelAskToggle  string // "Space/Enter toggle"
	PanelAskOther   string // "v Other input"
	PanelAskSubmit  string // "Enter submit"
	PanelAskNewline string // "Ctrl+J newline"
	PanelAskCancel  string // "Esc cancel"

	BgTasksTitle       string // "Background Tasks"
	BgTasksHelp        string // "↑↓ navigate  Enter view log  Del kill  Esc close"
	BgTasksEmpty       string // "No background tasks running"
	BgTasksUnsupported string // "Background tasks not supported."
	BgTaskLogTitle     string // "Log: %s — %s"
	BgTaskLogHelp      string // "↑↓ scroll  Esc back"
	BgTaskLogMore      string // "... %d more lines (↑↓ scroll)"

	PanelOmitted          string // "... %d lines omitted (narrow terminal) ..."
	PanelOtherPlaceholder string // askuser panel Other input placeholder
	EmergencyQuitHint     string // Ctrl+Z emergency quit hint

	// --- C. Status bar ---
	TitleHint             string // "Enter send · Ctrl+J newline · /help"
	ProcessingPlaceholder string // "[Processing...] (Ctrl+C to cancel)"
	CheckingUpdates       string // "⟳ checking for updates..."
	StatusReady           string // "● ready"
	StatusCompressing     string // "compressing"
	StatusRetrying        string // "retrying"
	StatusDone            string // "done"
	NewContentHint        string // "v new content"
	BgTaskRunning         string // "[bg: %d task%s running -- ^ to manage]"
	TabNoMatch            string // "[Tab] no matching files"

	// --- D. Temp status ---
	WaitingOperation   string // "... waiting for previous operation to complete..."
	NoMessagesToDelete string // "[!] no messages to delete"
	KillFailed         string // "Kill failed: %s"

	// --- E. Help ---
	HelpTitle          string // "xbot Help"
	HelpCommandsTitle  string // " Commands "
	HelpShortcutsTitle string // " Shortcuts "
	HelpCmds           []HelpCmdEntry
	HelpKeys           []HelpKeyEntry

	// --- E2. Fold messages (§19) ---
	MsgTooShortToFold string
	MsgExpanded       string
	MsgCollapsed      string

	// --- E3. Search (§21) ---
	SearchPlaceholder string
	SearchResults     string
	SearchNoResults   string
	SearchNavFormat   string

	// --- F. Confirm dialog ---
	ConfirmDelete string // "[!] Ctrl+K: delete last %d messages? (y/N, number to adjust)"

	// --- G. Splash ---
	SplashDesc    string // "AI-powered terminal agent"
	SplashLoading string // "  %s  initializing..."

	// --- H. Footer keys ---
	FooterScroll   string // "scroll"
	FooterBack     string // "back"
	FooterNavigate string // "navigate"
	FooterLog      string // "log"
	FooterKill     string // "kill"
	FooterClose    string // "close"
	FooterCancel   string // "cancel"
	FooterDelete   string // "delete"
	FooterCommands string // "commands"
	FooterComplete string // "complete"
	FooterBgTasks  string // "bg tasks"
	FooterNewline  string // "newline"
	FooterSelect   string // "select"
	FooterManage   string // "manage"
	FooterHistory  string // "history"
	FooterSearch   string // "search"
	FooterFold     string // "fold"
	FooterUnfold   string // "unfold"

	// --- K. Runner panel ---
	RunnerPanelTitle           string
	RunnerStatusConnected      string
	RunnerConnecting           string
	RunnerDisconnect           string
	RunnerDisconnectAction     string
	RunnerConnectSuccess       string
	RunnerConnectFailed        string
	RunnerServerLabel          string
	RunnerTokenLabel           string
	RunnerWorkspaceLabel       string
	RunnerServerPlaceholder    string
	RunnerTokenPlaceholder     string
	RunnerWorkspacePlaceholder string
	RunnerServerRequired       string
	RunnerWorkspaceRequired    string
	RunnerPleaseWait           string
	RunnerNavHint              string
	RunnerNotAvailable         string
	RunnerLogLabel             string // "📋 日志"
	RunnerBack                 string // "back"

	// --- I. Dynamic arrays ---
	ThinkingVerbs    []string // spinner verbs: Thinking, Reasoning, ...
	IdlePlaceholders []string // rotating hints for empty input

	// --- J. Settings schema ---
	SetupSchema    []SettingDefinition
	SettingsSchema []SettingDefinition
}

// HelpCmdEntry describes a single help command entry.
type HelpCmdEntry struct {
	Cmd  string
	Desc string
}

// HelpKeyEntry describes a single help key entry.
type HelpKeyEntry struct {
	Key  string
	Desc string
}

// ---------------------------------------------------------------------------
// Locales
// ---------------------------------------------------------------------------

var locales map[string]*UILocale

func init() {
	zh := &UILocale{
		// --- A. System messages ---
		CancelSent:     "已发送取消请求",
		SettingsSaved:  "✅ 设置已保存",
		NoSettings:     "当前渠道没有可配置的设置项。",
		CheckingUpdate: "正在检查更新...",
		ModelUsage:     "用法: /model <模型名>\n使用 /models 查看可用模型",
		AskCancelled:   "已取消提问",
		SetupComplete:  "✅ 初始配置完成，可以开始使用了。随时用 /settings 修改配置，/setup 重新引导。",
		SetupLettaNote: "\n\n[!] letta 记忆模式需要嵌入服务:\n  1. 安装 Ollama: https://ollama.ai\n  2. 拉取嵌入模型: `ollama pull nomic-embed-text`\n  3. 在配置或环境变量中设置嵌入端点",
		UpdateFound:    "发现新版本: %s → %s\n升级命令: curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash\n%s",
		UpdateCurrent:  "当前版本 %s 已是最新",
		UpdateFailed:   "更新检查失败（网络超时或无法连接 GitHub API）",

		// --- B. Panel text ---
		PanelSettingsTitle:   "⚙ 设置",
		PanelNotSet:          "(未设置)",
		PanelEditHint:        "Enter 确认 | Esc 取消",
		PanelComboHint:       "↑↓ 选择 | Enter 确认 | 输入自定义值 | Esc 取消",
		PanelNavHint:         "↑↓ 导航 · Enter 编辑/切换 · Ctrl+S 保存 · Esc 关闭",
		PanelEditPlaceholder: "> 输入新值...",
		PanelToggleOn:        "● 开启",
		PanelToggleOff:       "○ 关闭",

		PanelOther:      "其他: ",
		PanelSubmit:     "提交 →",
		PanelAskNav:     "←→/Tab 切换问题",
		PanelAskToggle:  "Space/Enter 切换",
		PanelAskOther:   "v 自定义输入",
		PanelAskSubmit:  "Enter 提交",
		PanelAskNewline: "Ctrl+J 换行",
		PanelAskCancel:  "Esc 取消",

		BgTasksTitle:       "后台任务",
		BgTasksHelp:        "↑↓ 导航  Enter 查看日志  Del 终止  Esc 关闭",
		BgTasksEmpty:       "没有正在运行的后台任务",
		BgTasksUnsupported: "不支持后台任务。",
		BgTaskLogTitle:     "日志: %s — %s",
		BgTaskLogHelp:      "↑↓ 滚动  Esc 返回",
		BgTaskLogMore:      "... 还有 %d 行（↑↓ 滚动）",

		PanelOmitted:          "  ... %d 行已省略（终端过窄） ...",
		PanelOtherPlaceholder: "在此输入...",
		EmergencyQuitHint:     "🚪 紧急退出 (Ctrl+Z)",

		// --- C. Status bar ---
		TitleHint:             "Enter 发送 · Ctrl+J 换行 · /help",
		ProcessingPlaceholder: "[处理中...] (Ctrl+C 取消)",
		CheckingUpdates:       "⟳ 正在检查更新...",
		StatusReady:           "● 就绪",
		StatusCompressing:     "压缩中",
		StatusRetrying:        "重试中",
		StatusDone:            "完成",
		NewContentHint:        "↓ 新内容",
		BgTaskRunning:         "[后台: %d 个任务运行中 -- ^ 管理]",
		TabNoMatch:            "[Tab] 无匹配文件",

		// --- D. Temp status ---
		WaitingOperation:   "... 等待上一个操作完成...",
		NoMessagesToDelete: "[!] 没有可删除的消息",
		KillFailed:         "终止失败: %s",

		// --- E. Help commands ---
		HelpTitle:          "xbot 帮助",
		HelpCommandsTitle:  " 命令 ",
		HelpShortcutsTitle: " 快捷键 ",
		HelpCmds: []HelpCmdEntry{
			{Cmd: "/cancel", Desc: "取消当前操作"},
			{Cmd: "/clear", Desc: "清空聊天记录"},
			{Cmd: "/compact", Desc: "压缩上下文"},
			{Cmd: "/model", Desc: "切换模型"},
			{Cmd: "/models", Desc: "列出可用模型"},
			{Cmd: "/context", Desc: "查看上下文信息"},
			{Cmd: "/new", Desc: "开始新会话"},
			{Cmd: "/quit", Desc: "退出程序"},
			{Cmd: "/settings", Desc: "打开设置面板"},
			{Cmd: "/setup", Desc: "重新运行配置引导"},
			{Cmd: "/tasks", Desc: "查看后台任务"},
			{Cmd: "/update", Desc: "检查更新"},
			{Cmd: "/help", Desc: "显示此帮助"},
		},
		HelpKeys: []HelpKeyEntry{
			{Key: "Enter", Desc: "发送消息"},
			{Key: "Ctrl+C/Esc", Desc: "中止/清空"},
			{Key: "Ctrl+J", Desc: "输入框换行"},
			{Key: "Ctrl+K", Desc: "上下文删除"},
			{Key: "Ctrl+O", Desc: "展开/折叠工具"},
			{Key: "Tab", Desc: "命令/路径补全"},
			{Key: "Home/End", Desc: "跳到顶/底部"},
			{Key: "↑", Desc: "后台任务面板"},
		},

		// --- E2. Fold messages (§19) ---
		MsgTooShortToFold: "消息太短，无法折叠（需超过 %d 行）",
		MsgExpanded:       "已展开",
		MsgCollapsed:      "已折叠",

		// --- E3. Search (§21) ---
		SearchPlaceholder: "搜索消息...",
		SearchResults:     "找到 %d 条匹配消息 (n/N 导航, Esc 退出)",
		SearchNoResults:   "未找到匹配消息",
		SearchNavFormat:   "/ %s  [%d/%d]  n next · N prev · Esc",

		// --- F. Confirm dialog ---
		ConfirmDelete: "[!] Ctrl+K: 删除最后 %d 轮对话？(y/N, 数字调整)",

		// --- G. Splash ---
		SplashDesc:    "AI 驱动的终端助手",
		SplashLoading: "  %s  初始化中...",

		// --- H. Footer keys ---
		FooterScroll:   "滚动",
		FooterBack:     "返回",
		FooterNavigate: "导航",
		FooterLog:      "日志",
		FooterKill:     "终止",
		FooterClose:    "关闭",
		FooterCancel:   "取消",
		FooterDelete:   "删除",
		FooterCommands: "命令",
		FooterComplete: "补全",
		FooterBgTasks:  "后台任务",
		FooterNewline:  "换行",
		FooterSelect:   "选择",
		FooterManage:   "管理",
		FooterHistory:  "历史",
		FooterSearch:   "搜索",
		FooterFold:     "折叠",
		FooterUnfold:   "展开",

		// --- K. Runner panel ---
		RunnerPanelTitle:           "Runner 管理",
		RunnerStatusConnected:      "已连接",
		RunnerConnecting:           "正在连接...",
		RunnerDisconnect:           "断开连接",
		RunnerDisconnectAction:     "断开",
		RunnerConnectSuccess:       "✅ Runner 已连接",
		RunnerConnectFailed:        "❌ Runner 连接失败: %s",
		RunnerServerLabel:          "Server URL",
		RunnerTokenLabel:           "Token",
		RunnerWorkspaceLabel:       "工作目录",
		RunnerServerPlaceholder:    "ws://host:port/ws/userID",
		RunnerTokenPlaceholder:     "认证 Token",
		RunnerWorkspacePlaceholder: "共享的工作目录路径",
		RunnerServerRequired:       "Server URL 不能为空",
		RunnerWorkspaceRequired:    "工作目录不能为空",
		RunnerPleaseWait:           "请稍候...",
		RunnerNavHint:              "↑↓/Tab 切换字段  Enter 连接  Esc 返回",
		RunnerNotAvailable:         "Runner 功能不可用",
		RunnerLogLabel:             "📋 日志",
		RunnerBack:                 "返回",

		// --- I. Dynamic arrays ---
		ThinkingVerbs: []string{"思考中", "推理中", "分析中", "考虑中", "评估中", "反思中", "处理中", "沉思中"},
		IdlePlaceholders: []string{
			"Enter 发送 · Ctrl+J 换行 · /help",
			"/model 切换模型",
			"Ctrl+K 删除 · Ctrl+O 工具",
			"@filepath 附加文件",
			"^ 打开后台任务面板",
			"/compact 压缩上下文",
			"/settings 打开设置",
			"/new 开始新会话",
		},

		// --- J. Settings schema ---
		SetupSchema: []SettingDefinition{
			{
				Key: "llm_provider", Label: "LLM 提供商", Description: "选择 LLM 服务提供商",
				Type: SettingTypeSelect, Category: "LLM", DefaultValue: "openai",
				Options: []SettingOption{
					{Label: "OpenAI (及兼容 API)", Value: "openai"},
					{Label: "Anthropic (Claude)", Value: "anthropic"},
				},
			},
			{
				Key: "llm_api_key", Label: "API Key", Description: "LLM 服务的 API Key（必填）",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "llm_base_url", Label: "Base URL", Description: "LLM API 地址",
				Type: SettingTypeText, Category: "LLM", DefaultValue: "https://api.openai.com/v1",
			},
			{
				Key: "llm_model", Label: "模型名称", Description: "选择或输入 LLM 模型名称",
				Type: SettingTypeText, Category: "LLM", DefaultValue: "gpt-4o",
			},
			{
				Key: "tavily_api_key", Label: "Tavily API Key", Description: "网络搜索服务密钥（可选，留空则无法使用 WebSearch）",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "sandbox_mode", Label: "沙箱模式", Description: "命令执行隔离方式",
				Type: SettingTypeSelect, Category: "环境", DefaultValue: "none",
				Options: []SettingOption{
					{Label: "none — 直接执行（推荐）", Value: "none"},
					{Label: "docker — 容器隔离", Value: "docker"},
				},
			},
			{
				Key: "memory_provider", Label: "记忆模式", Description: "记忆系统实现方式",
				Type: SettingTypeSelect, Category: "环境", DefaultValue: "flat",
				Options: []SettingOption{
					{Label: "flat — 全量注入（推荐）", Value: "flat"},
					{Label: "letta — 分层记忆", Value: "letta"},
				},
			},
			{
				Key: "theme", Label: "配色方案", Description: "CLI 界面配色",
				Type: SettingTypeSelect, Category: "外观", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight（默认）", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:极光", Value: "nord"},
					{Label: "dracula:暗夜", Value: "dracula"},
					{Label: "catppuccin:摩卡", Value: "catppuccin"},
				},
			},
		},
		SettingsSchema: []SettingDefinition{
			{
				Key: "llm_provider", Label: "LLM 提供商", Description: "选择 LLM 服务提供商",
				Type: SettingTypeSelect, Category: "LLM",
				Options: []SettingOption{
					{Label: "OpenAI (及兼容 API)", Value: "openai"},
					{Label: "Anthropic (Claude)", Value: "anthropic"},
				},
			},
			{
				Key: "llm_api_key", Label: "API Key", Description: "LLM 服务的 API Key",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "llm_model", Label: "LLM 模型", Description: "选择或输入 LLM 模型名称",
				Type: SettingTypeCombo, Category: "LLM",
			},
			{
				Key: "llm_base_url", Label: "LLM Base URL", Description: "LLM API 地址（兼容 OpenAI 格式的第三方服务可修改此项）",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "context_mode", Label: "上下文模式", Description: "控制上下文管理策略",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "auto",
				Options: []SettingOption{
					{Label: "自动（默认）", Value: "auto"},
					{Label: "手动压缩", Value: "manual"},
					{Label: "不压缩", Value: "none"},
				},
			},
			{
				Key: "max_iterations", Label: "最大迭代次数", Description: "单次对话最大工具调用迭代次数（默认 2000）",
				Type: SettingTypeNumber, Category: "Agent", DefaultValue: "2000",
			},
			{
				Key: "max_concurrency", Label: "最大并发数", Description: "同时处理的最大请求数（默认 3）",
				Type: SettingTypeNumber, Category: "Agent", DefaultValue: "3",
			},
			{
				Key: "max_context_tokens", Label: "最大上下文 Token", Description: "上下文最大 token 数（默认 200000）",
				Type: SettingTypeNumber, Category: "Agent", DefaultValue: "200000",
			},
			{
				Key: "enable_auto_compress", Label: "自动压缩", Description: "上下文过长时自动压缩（默认开启）",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "开启", Value: "true"},
					{Label: "关闭", Value: "false"},
				},
			},
			{
				Key: "enable_masking", Label: "工具结果遮蔽", Description: "上下文较大时自动遮蔽旧工具结果以释放空间（默认开启）",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "开启", Value: "true"},
					{Label: "关闭", Value: "false"},
				},
			},
			{
				Key: "language", Label: "语言", Description: "Agent 回复使用的语言",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "",
				Options: []SettingOption{
					{Label: "跟随 Prompt（默认）", Value: ""},
					{Label: "English", Value: "en"},
					{Label: "中文", Value: "zh"},
					{Label: "日本語", Value: "ja"},
				},
			},
			{
				Key: "theme", Label: "配色", Description: "CLI 界面配色方案",
				Type: SettingTypeSelect, Category: "外观", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight（默认）", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:极光", Value: "nord"},
					{Label: "dracula:暗夜", Value: "dracula"},
					{Label: "catppuccin:摩卡", Value: "catppuccin"},
				},
			},
			// Runner panel entry (display-only, triggers panel switch)
			{Key: "runner_panel", Label: "🔧 Runner 管理", Type: SettingTypeText, Category: "Runner"},
			// Danger zone entry (display-only, triggers panel switch)
			{Key: "danger_zone", Label: "⚠️ 危险区 — 清空记忆", Type: SettingTypeText, Category: "危险"},
		},
	}

	en := &UILocale{
		// --- A. System messages ---
		CancelSent:     "Cancel request sent",
		SettingsSaved:  "✅ Settings saved",
		NoSettings:     "No configurable settings for this channel.",
		CheckingUpdate: "Checking for updates...",
		ModelUsage:     "Usage: /model <model name>\nUse /models to list available models",
		AskCancelled:   "Question cancelled",
		SetupComplete:  "✅ Initial setup complete. Use /settings to configure, /setup to re-run.",
		SetupLettaNote: "\n\n[!] letta memory mode requires embedding service:\n  1. Install Ollama: https://ollama.ai\n  2. Pull embedding model: `ollama pull nomic-embed-text`\n  3. Set embedding endpoint in config or env",
		UpdateFound:    "New version available: %s → %s\nUpdate command: curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash\n%s",
		UpdateCurrent:  "Current version %s is up to date",
		UpdateFailed:   "Update check failed (network timeout or unable to connect to GitHub API)",

		// --- B. Panel text ---
		PanelSettingsTitle:   "⚙ Settings",
		PanelNotSet:          "(not set)",
		PanelEditHint:        "Enter confirm | Esc cancel",
		PanelComboHint:       "Up/Down select | Enter confirm | Type custom | Esc cancel",
		PanelNavHint:         "↑↓ navigate · Enter edit/toggle · Ctrl+S save · Esc close",
		PanelEditPlaceholder: "Enter new value...",
		PanelToggleOn:        "● ON",
		PanelToggleOff:       "○ OFF",

		PanelOther:      "Other: ",
		PanelSubmit:     "Submit →",
		PanelAskNav:     "←→/Tab switch question",
		PanelAskToggle:  "Space/Enter toggle",
		PanelAskOther:   "v Other input",
		PanelAskSubmit:  "Enter submit",
		PanelAskNewline: "Ctrl+J newline",
		PanelAskCancel:  "Esc cancel",

		BgTasksTitle:       "Background Tasks",
		BgTasksHelp:        "↑↓ navigate  Enter view log  Del kill  Esc close",
		BgTasksEmpty:       "No background tasks running",
		BgTasksUnsupported: "Background tasks not supported.",
		BgTaskLogTitle:     "Log: %s — %s",
		BgTaskLogHelp:      "↑↓ scroll  Esc back",
		BgTaskLogMore:      "... %d more lines (↑↓ scroll)",

		PanelOmitted:          "  ... %d lines omitted (narrow terminal) ...",
		PanelOtherPlaceholder: "Type here...",
		EmergencyQuitHint:     "🚪 Emergency Quit (Ctrl+Z)",

		// --- C. Status bar ---
		TitleHint:             "Enter send · Ctrl+J newline · /help",
		ProcessingPlaceholder: "[Processing...] (Ctrl+C to cancel)",
		CheckingUpdates:       "⟳ checking for updates...",
		StatusReady:           "● ready",
		StatusCompressing:     "compressing",
		StatusRetrying:        "retrying",
		StatusDone:            "done",
		NewContentHint:        "v new content",
		BgTaskRunning:         "[bg: %d task(s) running -- ^ to manage]",
		TabNoMatch:            "[Tab] no matching files",

		// --- D. Temp status ---
		WaitingOperation:   "... waiting for previous operation to complete...",
		NoMessagesToDelete: "[!] no messages to delete",
		KillFailed:         "Kill failed: %s",

		// --- E. Help commands ---
		HelpTitle:          "xbot Help",
		HelpCommandsTitle:  " Commands ",
		HelpShortcutsTitle: " Shortcuts ",
		HelpCmds: []HelpCmdEntry{
			{Cmd: "/cancel", Desc: "Cancel current operation"},
			{Cmd: "/clear", Desc: "Clear chat history"},
			{Cmd: "/compact", Desc: "Compress context"},
			{Cmd: "/model", Desc: "Switch model"},
			{Cmd: "/models", Desc: "List available models"},
			{Cmd: "/context", Desc: "View context info"},
			{Cmd: "/new", Desc: "Start new session"},
			{Cmd: "/quit", Desc: "Quit"},
			{Cmd: "/settings", Desc: "Open settings panel"},
			{Cmd: "/setup", Desc: "Re-run setup wizard"},
			{Cmd: "/tasks", Desc: "View background tasks"},
			{Cmd: "/update", Desc: "Check for updates"},
			{Cmd: "/help", Desc: "Show this help"},
		},
		HelpKeys: []HelpKeyEntry{
			{Key: "Enter", Desc: "Send message"},
			{Key: "Ctrl+C/Esc", Desc: "Abort/clear"},
			{Key: "Ctrl+J", Desc: "Newline in input"},
			{Key: "Ctrl+K", Desc: "Context delete"},
			{Key: "Ctrl+O", Desc: "Expand/collapse tools"},
			{Key: "Tab", Desc: "Command/path completion"},
			{Key: "Home/End", Desc: "Jump to top/bottom"},
			{Key: "↑", Desc: "Background tasks panel"},
		},

		// --- E2. Fold messages (§19) ---
		MsgTooShortToFold: "Message too short to fold (needs > %d lines)",
		MsgExpanded:       "Expanded",
		MsgCollapsed:      "Collapsed",

		// --- E3. Search (§21) ---
		SearchPlaceholder: "Search messages...",
		SearchResults:     "Found %d matching messages (n/N navigate, Esc to exit)",
		SearchNoResults:   "No matching messages found",
		SearchNavFormat:   "/ %s  [%d/%d]  n next · N prev · Esc",

		// --- F. Confirm dialog ---
		ConfirmDelete: "[!] Ctrl+K: delete last %d turns? (y/N, number to adjust)",

		// --- G. Splash ---
		SplashDesc:    "AI-powered terminal agent",
		SplashLoading: "  %s  initializing...",

		// --- H. Footer keys ---
		FooterScroll:   "scroll",
		FooterBack:     "back",
		FooterNavigate: "navigate",
		FooterLog:      "log",
		FooterKill:     "kill",
		FooterClose:    "close",
		FooterCancel:   "cancel",
		FooterDelete:   "delete",
		FooterCommands: "commands",
		FooterComplete: "complete",
		FooterBgTasks:  "bg tasks",
		FooterNewline:  "newline",
		FooterSelect:   "select",
		FooterManage:   "manage",
		FooterHistory:  "history",
		FooterSearch:   "search",
		FooterFold:     "fold",
		FooterUnfold:   "unfold",

		// --- K. Runner panel ---
		RunnerPanelTitle:           "Runner Manager",
		RunnerStatusConnected:      "Connected",
		RunnerConnecting:           "Connecting...",
		RunnerDisconnect:           "Disconnect",
		RunnerDisconnectAction:     "disconnect",
		RunnerConnectSuccess:       "✅ Runner connected",
		RunnerConnectFailed:        "❌ Runner connection failed: %s",
		RunnerServerLabel:          "Server URL",
		RunnerTokenLabel:           "Token",
		RunnerWorkspaceLabel:       "Workspace",
		RunnerServerPlaceholder:    "ws://host:port/ws/userID",
		RunnerTokenPlaceholder:     "Auth token",
		RunnerWorkspacePlaceholder: "Shared workspace directory",
		RunnerServerRequired:       "Server URL is required",
		RunnerWorkspaceRequired:    "Workspace is required",
		RunnerPleaseWait:           "Please wait...",
		RunnerNavHint:              "↑↓/Tab switch fields  Enter connect  Esc back",
		RunnerNotAvailable:         "Runner not available",
		RunnerLogLabel:             "📋 Log",
		RunnerBack:                 "back",

		// --- I. Dynamic arrays ---
		ThinkingVerbs: []string{"Thinking", "Reasoning", "Analyzing", "Considering", "Evaluating", "Reflecting", "Processing", "Contemplating"},
		IdlePlaceholders: []string{
			"Enter send · Ctrl+J newline · /help",
			"Type /model to switch model",
			"Ctrl+K delete | Ctrl+O tools",
			"@filepath to attach files",
			"^ open background tasks panel",
			"Type /compact to compress context",
			"Type /settings to configure",
			"Type /new to start fresh session",
		},

		// --- J. Settings schema ---
		SetupSchema: []SettingDefinition{
			{
				Key: "llm_provider", Label: "LLM Provider", Description: "Select LLM service provider",
				Type: SettingTypeSelect, Category: "LLM", DefaultValue: "openai",
				Options: []SettingOption{
					{Label: "OpenAI (and compatible API)", Value: "openai"},
					{Label: "Anthropic (Claude)", Value: "anthropic"},
				},
			},
			{
				Key: "llm_api_key", Label: "API Key", Description: "API key for LLM service (required)",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "llm_base_url", Label: "Base URL", Description: "LLM API endpoint",
				Type: SettingTypeText, Category: "LLM", DefaultValue: "https://api.openai.com/v1",
			},
			{
				Key: "llm_model", Label: "Model Name", Description: "Select or enter LLM model name",
				Type: SettingTypeText, Category: "LLM", DefaultValue: "gpt-4o",
			},
			{
				Key: "tavily_api_key", Label: "Tavily API Key", Description: "Web search service key (optional, leave empty to disable WebSearch)",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "sandbox_mode", Label: "Sandbox Mode", Description: "Command execution isolation method",
				Type: SettingTypeSelect, Category: "Environment", DefaultValue: "none",
				Options: []SettingOption{
					{Label: "none — direct execution (recommended)", Value: "none"},
					{Label: "docker — container isolation", Value: "docker"},
				},
			},
			{
				Key: "memory_provider", Label: "Memory Mode", Description: "Memory system implementation",
				Type: SettingTypeSelect, Category: "Environment", DefaultValue: "flat",
				Options: []SettingOption{
					{Label: "flat — full injection (recommended)", Value: "flat"},
					{Label: "letta — layered memory", Value: "letta"},
				},
			},
			{
				Key: "theme", Label: "Color Theme", Description: "CLI color scheme",
				Type: SettingTypeSelect, Category: "Appearance", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight (default)", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:Aurora", Value: "nord"},
					{Label: "dracula:Dark Night", Value: "dracula"},
					{Label: "catppuccin:Mocha", Value: "catppuccin"},
				},
			},
		},
		SettingsSchema: []SettingDefinition{
			{
				Key: "llm_provider", Label: "LLM Provider", Description: "Select LLM service provider",
				Type: SettingTypeSelect, Category: "LLM",
				Options: []SettingOption{
					{Label: "OpenAI (and compatible API)", Value: "openai"},
					{Label: "Anthropic (Claude)", Value: "anthropic"},
				},
			},
			{
				Key: "llm_api_key", Label: "API Key", Description: "LLM service API Key",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "llm_model", Label: "LLM Model", Description: "Select or enter LLM model name",
				Type: SettingTypeCombo, Category: "LLM",
			},
			{
				Key: "llm_base_url", Label: "LLM Base URL", Description: "LLM API endpoint (modify for OpenAI-compatible third-party services)",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "context_mode", Label: "Context Mode", Description: "Context management strategy",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "auto",
				Options: []SettingOption{
					{Label: "Auto (default)", Value: "auto"},
					{Label: "Manual compress", Value: "manual"},
					{Label: "No compress", Value: "none"},
				},
			},
			{
				Key: "max_iterations", Label: "Max Iterations", Description: "Max tool call iterations per conversation (default 2000)",
				Type: SettingTypeNumber, Category: "Agent", DefaultValue: "2000",
			},
			{
				Key: "max_concurrency", Label: "Max Concurrency", Description: "Max concurrent requests (default 3)",
				Type: SettingTypeNumber, Category: "Agent", DefaultValue: "3",
			},
			{
				Key: "max_context_tokens", Label: "Max Context Tokens", Description: "Max context token count (default 200000)",
				Type: SettingTypeNumber, Category: "Agent", DefaultValue: "200000",
			},
			{
				Key: "enable_auto_compress", Label: "Auto Compress", Description: "Automatically compress when context is too long (on by default)",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "On", Value: "true"},
					{Label: "Off", Value: "false"},
				},
			},
			{
				Key: "enable_masking", Label: "Tool Result Masking", Description: "Automatically mask old tool results to free context space (on by default)",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "On", Value: "true"},
					{Label: "Off", Value: "false"},
				},
			},
			{
				Key: "language", Label: "Language", Description: "Language for agent replies",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "",
				Options: []SettingOption{
					{Label: "Follow prompt (default)", Value: ""},
					{Label: "English", Value: "en"},
					{Label: "中文", Value: "zh"},
					{Label: "日本語", Value: "ja"},
				},
			},
			{
				Key: "theme", Label: "Theme", Description: "CLI color scheme",
				Type: SettingTypeSelect, Category: "Appearance", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight (default)", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:Aurora", Value: "nord"},
					{Label: "dracula:Dark Night", Value: "dracula"},
					{Label: "catppuccin:Mocha", Value: "catppuccin"},
				},
			},
			// Runner panel entry (display-only, triggers panel switch)
			{Key: "runner_panel", Label: "🔧 Runner Manager", Type: SettingTypeText, Category: "Runner"},
			// Danger zone entry (display-only, triggers panel switch)
			{Key: "danger_zone", Label: "⚠️ Danger Zone — Clear Memory", Type: SettingTypeText, Category: "Danger"},
		},
	}

	ja := &UILocale{
		// --- A. System messages ---
		CancelSent:     "キャンセルリクエストを送信しました",
		SettingsSaved:  "✅ 設定を保存しました",
		NoSettings:     "このチャンネルには設定項目がありません。",
		CheckingUpdate: "アップデートを確認中...",
		ModelUsage:     "使い方: /model <モデル名>\n/models で利用可能モデルを表示",
		AskCancelled:   "質問をキャンセルしました",
		SetupComplete:  "✅ 初期設定が完了しました。/settings で設定変更、/setup で再設定。",
		SetupLettaNote: "\n\n[!] letta メモリモードには埋め込みサービスが必要です:\n  1. Ollama をインストール: https://ollama.ai\n  2. 埋め込みモデルを取得: `ollama pull nomic-embed-text`\n  3. 設定または環境変数で埋め込みエンドポイントを設定",
		UpdateFound:    "新しいバージョン: %s → %s\nアップデート: curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash\n%s",
		UpdateCurrent:  "現在のバージョン %s は最新です",
		UpdateFailed:   "アップデート確認に失敗（ネットワークタイムアウトまたは GitHub API に接続できません）",

		// --- B. Panel text ---
		PanelSettingsTitle:   "⚙ 設定",
		PanelNotSet:          "(未設定)",
		PanelEditHint:        "Enter 確認 | Esc キャンセル",
		PanelComboHint:       "↑↓ 選択 | Enter 確認 | カスタム入力 | Esc キャンセル",
		PanelNavHint:         "↑↓ 移動 · Enter 編集/切替 · Ctrl+S 保存 · Esc 閉じる",
		PanelEditPlaceholder: "> 新しい値を入力...",
		PanelToggleOn:        "● オン",
		PanelToggleOff:       "○ オフ",

		PanelOther:      "その他: ",
		PanelSubmit:     "送信 →",
		PanelAskNav:     "←→/Tab 質問切替",
		PanelAskToggle:  "Space/Enter 切替",
		PanelAskOther:   "v その他入力",
		PanelAskSubmit:  "Enter 送信",
		PanelAskNewline: "Ctrl+J 改行",
		PanelAskCancel:  "Esc キャンセル",

		BgTasksTitle:       "バックグラウンドタスク",
		BgTasksHelp:        "↑↓ 移動  Enter ログ表示  Del 終了  Esc 閉じる",
		BgTasksEmpty:       "実行中のバックグラウンドタスクはありません",
		BgTasksUnsupported: "バックグラウンドタスクは未対応です。",
		BgTaskLogTitle:     "ログ: %s — %s",
		BgTaskLogHelp:      "↑↓ スクロール  Esc 戻る",
		BgTaskLogMore:      "... あと %d 行（↑↓ スクロール）",

		PanelOmitted:          "  ... %d 行省略（端末が狭すぎます） ...",
		PanelOtherPlaceholder: "ここに入力...",
		EmergencyQuitHint:     "🚪 緊急終了 (Ctrl+Z)",

		// --- C. Status bar ---
		TitleHint:             "Enter 送信 · Ctrl+J 改行 · /help",
		ProcessingPlaceholder: "[処理中...] (Ctrl+C キャンセル)",
		CheckingUpdates:       "⟳ アップデート確認中...",
		StatusReady:           "● 準備完了",
		StatusCompressing:     "圧縮中",
		StatusRetrying:        "リトライ中",
		StatusDone:            "完了",
		NewContentHint:        "↓ 新着",
		BgTaskRunning:         "[bg: %d タスク実行中 -- ^ 管理]",
		TabNoMatch:            "[Tab] 一致するファイルなし",

		// --- D. Temp status ---
		WaitingOperation:   "... 前の操作の完了を待機中...",
		NoMessagesToDelete: "[!] 削除するメッセージがありません",
		KillFailed:         "終了に失敗: %s",

		// --- E. Help commands ---
		HelpTitle:          "xbot ヘルプ",
		HelpCommandsTitle:  " コマンド ",
		HelpShortcutsTitle: " ショートカット ",
		HelpCmds: []HelpCmdEntry{
			{Cmd: "/cancel", Desc: "現在の操作をキャンセル"},
			{Cmd: "/clear", Desc: "チャット履歴をクリア"},
			{Cmd: "/compact", Desc: "コンテキストを圧縮"},
			{Cmd: "/model", Desc: "モデル切替"},
			{Cmd: "/models", Desc: "利用可能モデル一覧"},
			{Cmd: "/context", Desc: "コンテキスト情報表示"},
			{Cmd: "/new", Desc: "新規セッション開始"},
			{Cmd: "/quit", Desc: "終了"},
			{Cmd: "/settings", Desc: "設定パネルを開く"},
			{Cmd: "/setup", Desc: "セットアップウィザード再実行"},
			{Cmd: "/tasks", Desc: "バックグラウンドタスク表示"},
			{Cmd: "/update", Desc: "アップデート確認"},
			{Cmd: "/help", Desc: "ヘルプ表示"},
		},
		HelpKeys: []HelpKeyEntry{
			{Key: "Enter", Desc: "メッセージ送信"},
			{Key: "Ctrl+C/Esc", Desc: "中止/クリア"},
			{Key: "Ctrl+J", Desc: "入力欄で改行"},
			{Key: "Ctrl+K", Desc: "コンテキスト削除"},
			{Key: "Ctrl+O", Desc: "ツール展開/折りたたみ"},
			{Key: "Tab", Desc: "コマンド/パス補完"},
			{Key: "Home/End", Desc: "先頭/末尾へ移動"},
			{Key: "↑", Desc: "バックグラウンドタスクパネル"},
		},

		// --- E2. Fold messages (§19) ---
		MsgTooShortToFold: "メッセージが短すぎます（%d 行を超える必要があります）",
		MsgExpanded:       "展開しました",
		MsgCollapsed:      "折りたたみました",

		// --- E3. Search (§21) ---
		SearchPlaceholder: "メッセージを検索...",
		SearchResults:     "%d 件の一致メッセージが見つかりました (n/N で移動, Esc で終了)",
		SearchNoResults:   "一致するメッセージが見つかりません",
		SearchNavFormat:   "/ %s  [%d/%d]  n 次 · N 前 · Esc",

		// --- F. Confirm dialog ---
		ConfirmDelete: "[!] Ctrl+K: 最後の %d ターンを削除しますか？(y/N, 数字で調整)",

		// --- G. Splash ---
		SplashDesc:    "AI駆動のターミナルエージェント",
		SplashLoading: "  %s  初期化中...",

		// --- H. Footer keys ---
		FooterScroll:   "スクロール",
		FooterBack:     "戻る",
		FooterNavigate: "移動",
		FooterLog:      "ログ",
		FooterKill:     "終了",
		FooterClose:    "閉じる",
		FooterCancel:   "キャンセル",
		FooterDelete:   "削除",
		FooterCommands: "コマンド",
		FooterComplete: "補完",
		FooterBgTasks:  "バックグラウンド",
		FooterNewline:  "改行",
		FooterSelect:   "選択",
		FooterManage:   "管理",
		FooterHistory:  "履歴",
		FooterSearch:   "検索",
		FooterFold:     "折りたたみ",
		FooterUnfold:   "展開",

		// --- K. Runner panel ---
		RunnerPanelTitle:           "Runner 管理",
		RunnerStatusConnected:      "接続済み",
		RunnerConnecting:           "接続中...",
		RunnerDisconnect:           "接続解除",
		RunnerDisconnectAction:     "切断",
		RunnerConnectSuccess:       "✅ Runner 接続完了",
		RunnerConnectFailed:        "❌ Runner 接続失敗: %s",
		RunnerServerLabel:          "Server URL",
		RunnerTokenLabel:           "Token",
		RunnerWorkspaceLabel:       "ワークスペース",
		RunnerServerPlaceholder:    "ws://host:port/ws/userID",
		RunnerTokenPlaceholder:     "認証 Token",
		RunnerWorkspacePlaceholder: "共有ワークスペースパス",
		RunnerServerRequired:       "Server URL は必須です",
		RunnerWorkspaceRequired:    "ワークスペースは必須です",
		RunnerPleaseWait:           "お待ちください...",
		RunnerNavHint:              "↑↓/Tab フィールド切替  Enter 接続  Esc 戻る",
		RunnerNotAvailable:         "Runner 機能は利用できません",
		RunnerLogLabel:             "📋 ログ",
		RunnerBack:                 "戻る",

		// --- I. Dynamic arrays ---
		ThinkingVerbs: []string{"思考中", "推論中", "分析中", "検討中", "評価中", "振り返り", "処理中", "熟考中"},
		IdlePlaceholders: []string{
			"Enter 送信 · Ctrl+J 改行 · /help",
			"/model でモデル切替",
			"Ctrl+K 削除 · Ctrl+O ツール",
			"@filepath でファイル添付",
			"^ バックグラウンドタスクパネル",
			"/compact でコンテキスト圧縮",
			"/settings で設定",
			"/new で新規セッション",
		},

		// --- J. Settings schema ---
		SetupSchema: []SettingDefinition{
			{
				Key: "llm_provider", Label: "LLM プロバイダー", Description: "LLM サービスプロバイダーを選択",
				Type: SettingTypeSelect, Category: "LLM", DefaultValue: "openai",
				Options: []SettingOption{
					{Label: "OpenAI (および互換API)", Value: "openai"},
					{Label: "Anthropic (Claude)", Value: "anthropic"},
				},
			},
			{
				Key: "llm_api_key", Label: "API Key", Description: "LLM サービスの API Key（必須）",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "llm_base_url", Label: "Base URL", Description: "LLM API エンドポイント",
				Type: SettingTypeText, Category: "LLM", DefaultValue: "https://api.openai.com/v1",
			},
			{
				Key: "llm_model", Label: "モデル名", Description: "LLM モデル名を選択または入力",
				Type: SettingTypeText, Category: "LLM", DefaultValue: "gpt-4o",
			},
			{
				Key: "tavily_api_key", Label: "Tavily API Key", Description: "Web検索サービスキー（オプション、空の場合 WebSearch は無効）",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "sandbox_mode", Label: "サンドボックスモード", Description: "コマンド実行の分離方法",
				Type: SettingTypeSelect, Category: "環境", DefaultValue: "none",
				Options: []SettingOption{
					{Label: "none — 直接実行（推奨）", Value: "none"},
					{Label: "docker — コンテナ分離", Value: "docker"},
				},
			},
			{
				Key: "memory_provider", Label: "メモリモード", Description: "メモリシステムの実装方式",
				Type: SettingTypeSelect, Category: "環境", DefaultValue: "flat",
				Options: []SettingOption{
					{Label: "flat — 全量注入（推奨）", Value: "flat"},
					{Label: "letta — 階層メモリ", Value: "letta"},
				},
			},
			{
				Key: "theme", Label: "カラーテーマ", Description: "CLI カラースキーム",
				Type: SettingTypeSelect, Category: "外観", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight（デフォルト）", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:オーロラ", Value: "nord"},
					{Label: "dracula:ダークナイト", Value: "dracula"},
					{Label: "catppuccin:モカ", Value: "catppuccin"},
				},
			},
		},
		SettingsSchema: []SettingDefinition{
			{
				Key: "llm_provider", Label: "LLM プロバイダー", Description: "LLM サービスプロバイダーを選択",
				Type: SettingTypeSelect, Category: "LLM",
				Options: []SettingOption{
					{Label: "OpenAI (および互換 API)", Value: "openai"},
					{Label: "Anthropic (Claude)", Value: "anthropic"},
				},
			},
			{
				Key: "llm_api_key", Label: "API Key", Description: "LLM サービスの API Key",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "llm_model", Label: "LLM モデル", Description: "LLM モデル名を選択または入力",
				Type: SettingTypeCombo, Category: "LLM",
			},
			{
				Key: "llm_base_url", Label: "LLM Base URL", Description: "LLM API エンドポイント（OpenAI 互換のサードパーティサービス用に変更可）",
				Type: SettingTypeText, Category: "LLM",
			},
			{
				Key: "context_mode", Label: "コンテキストモード", Description: "コンテキスト管理戦略",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "auto",
				Options: []SettingOption{
					{Label: "自動（デフォルト）", Value: "auto"},
					{Label: "手動圧縮", Value: "manual"},
					{Label: "圧縮なし", Value: "none"},
				},
			},
			{
				Key: "max_iterations", Label: "最大反復数", Description: "1回の会話の最大ツール呼び出し反復数（デフォルト 2000）",
				Type: SettingTypeNumber, Category: "Agent", DefaultValue: "2000",
			},
			{
				Key: "max_concurrency", Label: "最大同時実行数", Description: "同時に処理する最大リクエスト数（デフォルト 3）",
				Type: SettingTypeNumber, Category: "Agent", DefaultValue: "3",
			},
			{
				Key: "max_context_tokens", Label: "最大コンテキストトークン", Description: "コンテキストの最大トークン数（デフォルト 200000）",
				Type: SettingTypeNumber, Category: "Agent", DefaultValue: "200000",
			},
			{
				Key: "enable_auto_compress", Label: "自動圧縮", Description: "コンテキストが長すぎる場合に自動圧縮（デフォルト: オン）",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "オン", Value: "true"},
					{Label: "オフ", Value: "false"},
				},
			},
			{
				Key: "enable_masking", Label: "ツール結果マスキング", Description: "コンテキストが大きい場合に古いツール結果を自動マスキング（デフォルト: オン）",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "true",
				Options: []SettingOption{
					{Label: "オン", Value: "true"},
					{Label: "オフ", Value: "false"},
				},
			},
			{
				Key: "language", Label: "言語", Description: "エージェントの返信言語",
				Type: SettingTypeSelect, Category: "Agent", DefaultValue: "",
				Options: []SettingOption{
					{Label: "プロンプトに従う（デフォルト）", Value: ""},
					{Label: "English", Value: "en"},
					{Label: "中文", Value: "zh"},
					{Label: "日本語", Value: "ja"},
				},
			},
			{
				Key: "theme", Label: "テーマ", Description: "CLI カラースキーム",
				Type: SettingTypeSelect, Category: "外観", DefaultValue: "midnight",
				Options: []SettingOption{
					{Label: "Midnight（デフォルト）", Value: "midnight"},
					{Label: "Ocean", Value: "ocean"},
					{Label: "Forest", Value: "forest"},
					{Label: "Sunset", Value: "sunset"},
					{Label: "Rose", Value: "rose"},
					{Label: "Mono", Value: "mono"},
					{Label: "nord:オーロラ", Value: "nord"},
					{Label: "dracula:ダークナイト", Value: "dracula"},
					{Label: "catppuccin:モカ", Value: "catppuccin"},
				},
			},
			// Runner panel entry (display-only, triggers panel switch)
			{Key: "runner_panel", Label: "🔧 Runner 管理", Type: SettingTypeText, Category: "Runner"},
			// Danger zone entry (display-only, triggers panel switch)
			{Key: "danger_zone", Label: "⚠️ 危険エリア — 記憶クリア", Type: SettingTypeText, Category: "危険"},
		},
	}

	locales = map[string]*UILocale{
		"":   zh, // default
		"zh": zh,
		"en": en,
		"ja": ja,
	}
}

// localeChangeCh is used to notify the running CLI model of locale changes.
// Follows the same pattern as themeChangeCh (buffered channel, non-blocking send).
var localeChangeCh = make(chan struct{}, 1)

// currentLocaleLang stores the current locale language code.
var currentLocaleLang string

// setLocale updates currentLocaleLang without sending on localeChangeCh.
// Use when the caller handles the locale change synchronously (e.g. applyLanguageChange),
// to avoid a redundant fullRebuild in the next Update cycle.
func setLocale(lang string) {
	currentLocaleLang = lang
}

// SetLocale switches the current locale and notifies the running model.
func SetLocale(lang string) {
	setLocale(lang)
	select {
	case localeChangeCh <- struct{}{}:
	default:
	}
}

// GetLocale returns the UILocale for the given language code.
// Falls back to Chinese (zh) for unknown languages.
func GetLocale(lang string) *UILocale {
	if loc, ok := locales[lang]; ok {
		return loc
	}
	return locales[""] // default zh
}
