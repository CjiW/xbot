package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
	"xbot/oauth"
	"xbot/oauth/providers"
	"xbot/storage"
	"xbot/storage/sqlite"
	"xbot/tools"
	"xbot/tools/feishu_mcp"
	"xbot/version"
)

func main() {
	cfg := config.Load()

	// 配置日志（支持文件输出 + 按日轮转）
	workDir := cfg.Agent.WorkDir
	if err := setupLogger(cfg.Log, workDir); err != nil {
		log.WithError(err).Fatal("Failed to setup logger")
	}
	defer log.Close()

	// 创建 LLM 客户端
	llmClient, err := createLLM(cfg.LLM, llm.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    cfg.Agent.LLMRetryDelay,
		MaxDelay: cfg.Agent.LLMRetryMaxDelay,
		Timeout:  cfg.Agent.LLMRetryTimeout,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to create LLM client")
	}
	log.WithFields(log.Fields{
		"provider": cfg.LLM.Provider,
		"model":    cfg.LLM.Model,
	}).Info("LLM client created")

	// 创建消息总线
	msgBus := bus.NewMessageBus()

	// 创建 Agent
	workDir = cfg.Agent.WorkDir
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	xbotDir := filepath.Join(workDir, ".xbot")
	dbPath := filepath.Join(xbotDir, "xbot.db")

	// 检测并执行数据迁移（如果需要）
	if err := storage.MigrateIfNeeded(context.Background(), workDir, dbPath); err != nil {
		log.WithError(err).Fatal("Failed to migrate data to SQLite")
	}

	// OAuth 管理
	var oauthServer *oauth.Server
	var oauthManager *oauth.Manager
	var feishuProvider *providers.FeishuProvider
	var sharedDB *sqlite.DB
	if cfg.OAuth.Enable {
		// Use the shared database for OAuth token storage
		sharedDB, err = sqlite.Open(dbPath)
		if err != nil {
			log.WithError(err).Fatal("Failed to open shared database for OAuth")
		}
		tokenStorage, err := oauth.NewSQLiteStorage(sharedDB)
		if err != nil {
			log.WithError(err).Fatal("Failed to create OAuth token storage")
		}

		// 创建 OAuth 管理器
		oauthManager = oauth.NewManager(tokenStorage)

		// 注册 Feishu OAuth provider
		feishuProvider = providers.NewFeishuProvider(
			cfg.Feishu.AppID,
			cfg.Feishu.AppSecret,
			cfg.OAuth.BaseURL+"/oauth/callback",
		)
		oauthManager.RegisterProvider(feishuProvider)

		// 创建 OAuth HTTP 服务器（SendFunc 稍后设置，需要在 Dispatcher 创建后）
		oauthServer = oauth.NewServer(oauth.Config{
			Enable:  true,
			Host:    cfg.OAuth.Host,
			Port:    cfg.OAuth.Port,
			BaseURL: cfg.OAuth.BaseURL,
		}, oauthManager)

		log.WithFields(log.Fields{
			"port":    cfg.OAuth.Port,
			"baseURL": cfg.OAuth.BaseURL,
		}).Info("OAuth server started")
	}

	// 嵌入向量配置（Letta 归档记忆使用 chromem-go）
	embBaseURL := cfg.Embedding.BaseURL
	if embBaseURL == "" {
		embBaseURL = cfg.LLM.BaseURL // 回退到 LLM 的 base URL
	}
	embAPIKey := cfg.Embedding.APIKey
	if embAPIKey == "" {
		embAPIKey = cfg.LLM.APIKey
	}

	// 初始化沙箱
	tools.InitSandbox(cfg.Sandbox, workDir)

	agentLoop := agent.New(agent.Config{
		Bus:                  msgBus,
		LLM:                  llmClient,
		Model:                cfg.LLM.Model,
		MaxIterations:        cfg.Agent.MaxIterations,
		MaxConcurrency:       cfg.Agent.MaxConcurrency,
		MemoryWindow:         cfg.Agent.MemoryWindow,
		DBPath:               dbPath,
		SkillsDir:            filepath.Join(xbotDir, "skills"),
		WorkDir:              workDir,
		PromptFile:           cfg.Agent.PromptFile,
		SingleUser:           cfg.Agent.SingleUser,
		SandboxMode:          cfg.Sandbox.Mode,
		Sandbox:              tools.GetSandbox(),
		MemoryProvider:       cfg.Agent.MemoryProvider,
		EmbeddingProvider:    cfg.Embedding.Provider,
		EmbeddingBaseURL:     embBaseURL,
		EmbeddingAPIKey:      embAPIKey,
		EmbeddingModel:       cfg.Embedding.Model,
		EmbeddingMaxTokens:   cfg.Embedding.MaxTokens,
		MCPInactivityTimeout: cfg.Agent.MCPInactivityTimeout,
		MCPCleanupInterval:   cfg.Agent.MCPCleanupInterval,
		SessionCacheTimeout:  cfg.Agent.SessionCacheTimeout,
		EnableAutoCompress:   cfg.Agent.EnableAutoCompress,
		MaxContextTokens:     cfg.Agent.MaxContextTokens,
		CompressionThreshold: cfg.Agent.CompressionThreshold,
		ContextMode:          agent.ContextMode(cfg.Agent.ContextMode),
		MaxSubAgentDepth:     cfg.Agent.MaxSubAgentDepth,
		PersonaIsolation:     cfg.Web.PersonaIsolation,
		PurgeOldMessages:     cfg.Agent.PurgeOldMessages,
		SandboxIdleTimeout:   cfg.Sandbox.IdleTimeout,
	})

	// 注册 OAuth 和 Feishu MCP 工具（如果启用）
	if cfg.OAuth.Enable && oauthManager != nil {
		// 注册 OAuth 工具
		oauthTool := &tools.OAuthTool{
			Manager: oauthManager,
			BaseURL: cfg.OAuth.BaseURL,
		}
		agentLoop.RegisterCoreTool(oauthTool)

		// 注册 Feishu MCP 工具
		feishuMCP := feishu_mcp.NewFeishuMCP(oauthManager, cfg.Feishu.AppID, cfg.Feishu.AppSecret)
		if feishuProvider != nil {
			feishuMCP.SetLarkClient(feishuProvider.GetLarkClient())
		}
		agentLoop.RegisterTool(&feishu_mcp.ListAllBitablesTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.BitableFieldsTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.BitableRecordTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.BitableListTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.BatchCreateAppTableRecordTool{MCP: feishuMCP})

		// Wiki tools
		agentLoop.RegisterTool(&feishu_mcp.WikiListSpacesTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.WikiListNodesTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.WikiGetNodeTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.WikiMoveNodeTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.WikiCreateNodeTool{MCP: feishuMCP})

		// Document tools
		agentLoop.RegisterTool(&feishu_mcp.DocxGetContentTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxListBlocksTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxCreateTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxInsertBlockTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxGetBlockTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxDeleteBlocksTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxFindBlockTool{MCP: feishuMCP})

		// Search tools
		agentLoop.RegisterTool(&feishu_mcp.SearchWikiTool{MCP: feishuMCP})

		// Drive tools
		agentLoop.RegisterTool(&feishu_mcp.UploadFileTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.ListFilesTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.AddPermissionTool{MCP: feishuMCP})

		// Message resource tools
		agentLoop.RegisterTool(&feishu_mcp.DownloadFileTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.SendFileTool{MCP: feishuMCP})

		log.Info("OAuth and Feishu MCP tools registered")
	}

	// 注册 DownloadFile 工具（支持 Web/OSS 和飞书两种来源）
	agentLoop.RegisterCoreTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	agentLoop.RegisterTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	agentLoop.RegisterCoreTool(tools.NewWebSearchTool(os.Getenv("TAVILY_API_KEY")))

	// 注册 Logs 工具（仅管理员可用）
	adminChatID := cfg.Admin.ChatID
	if adminChatID != "" {
		logsTool := tools.NewLogsTool(adminChatID)
		agentLoop.RegisterCoreTool(logsTool)
		log.WithField("admin_chat_id", adminChatID).Info("Logs tool registered (admin only)")
	}

	// 所有工具注册完成，索引全局工具（用于 search_tools 语义搜索）
	agentLoop.IndexGlobalTools()

	// 设置 runner token 持久化（复用同一个 DB 文件）
	tokenDB, err := sqlite.Open(dbPath)
	if err != nil {
		log.WithError(err).Warn("Failed to open token database, runner tokens disabled")
	} else {
		tools.SetRunnerTokenDB(tokenDB.Conn())
	}

	// 创建消息分发器
	disp := channel.NewDispatcher(msgBus)

	// 注册飞书渠道
	var feishuCh *channel.FeishuChannel
	if cfg.Feishu.Enabled {
		feishuCh = channel.NewFeishuChannel(channel.FeishuConfig{
			AppID:             cfg.Feishu.AppID,
			AppSecret:         cfg.Feishu.AppSecret,
			EncryptKey:        cfg.Feishu.EncryptKey,
			VerificationToken: cfg.Feishu.VerificationToken,
			AllowFrom:         cfg.Feishu.AllowFrom,
		}, msgBus)
		disp.Register(feishuCh)

	}

	// 注册 QQ 渠道
	if cfg.QQ.Enabled {
		qqCh := channel.NewQQChannel(channel.QQConfig{
			AppID:        cfg.QQ.AppID,
			ClientSecret: cfg.QQ.ClientSecret,
			AllowFrom:    cfg.QQ.AllowFrom,
		}, msgBus)
		disp.Register(qqCh)
	}

	// 注册 NapCat (OneBot 11) 渠道
	if cfg.NapCat.Enabled {
		napcatCh := channel.NewNapCatChannel(channel.NapCatConfig{
			WSUrl:     cfg.NapCat.WSUrl,
			Token:     cfg.NapCat.Token,
			AllowFrom: cfg.NapCat.AllowFrom,
		}, msgBus)
		disp.Register(napcatCh)
	}

	var webDB *sql.DB

	// 注册 Web 渠道
	if cfg.Web.Enable {
		if tokenDB != nil {
			webDB = tokenDB.Conn()
		}
		if webDB != nil {
			webCh := channel.NewWebChannel(channel.WebChannelConfig{
				Host:             cfg.Web.Host,
				Port:             cfg.Web.Port,
				DB:               webDB,
				MemoryWindow:     cfg.Agent.MemoryWindow,
				FeishuLinkSecret: cfg.Feishu.AppSecret,
				InviteOnly:       cfg.Web.InviteOnly,
				PublicURL:        cfg.Sandbox.PublicURL,
			}, msgBus)
			if cfg.Web.StaticDir != "" {
				webCh.SetStaticDir(cfg.Web.StaticDir)
			}
			// Web file uploads go through cloud OSS only — no local storage
			webCh.SetWorkDir(workDir)
			// Set OSS provider for file storage
			if cfg.OSS.Provider == "qiniu" {
				ossProvider, err := channel.NewOSSProvider(
					cfg.OSS.Provider,
					"",
					channel.QiniuConfig{
						AccessKey: cfg.OSS.QiniuAccessKey,
						SecretKey: cfg.OSS.QiniuSecretKey,
						Bucket:    cfg.OSS.QiniuBucket,
						Domain:    cfg.OSS.QiniuDomain,
						Region:    cfg.OSS.QiniuRegion,
					},
				)
				if err != nil {
					log.WithError(err).Error("Failed to create Qiniu OSS provider")
				} else {
					webCh.SetOSSProvider(ossProvider)
					log.Info("OSS provider configured: qiniu")
				}
			}

			webCh.SetCallbacks(channel.WebCallbacks{
				RunnerTokenGet: func(senderID string) string {
					db := tools.GetRunnerTokenDB()
					if db == nil {
						return ""
					}
					entry := tools.NewRunnerTokenStore(db).Get(senderID)
					if entry == nil {
						return ""
					}
					return buildRunnerConnectCmd(cfg, entry)
				},
				RunnerTokenGenerate: func(senderID, mode, dockerImage, workspace string) (string, error) {
					db := tools.GetRunnerTokenDB()
					if db == nil {
						return "", fmt.Errorf("remote sandbox not configured")
					}
					entry := tools.NewRunnerTokenStore(db).Generate(senderID, tools.RunnerTokenSettings{
						Mode:        mode,
						DockerImage: dockerImage,
						Workspace:   workspace,
					})
					if entry == nil {
						return "", fmt.Errorf("failed to generate token")
					}
					return buildRunnerConnectCmd(cfg, entry), nil
				},
				RunnerTokenRevoke: func(senderID string) error {
					db := tools.GetRunnerTokenDB()
					if db == nil {
						return fmt.Errorf("remote sandbox not configured")
					}
					tools.NewRunnerTokenStore(db).Revoke(senderID)
					return nil
				},
				RunnerList: func(senderID string) ([]tools.RunnerInfo, error) {
					db := tools.GetRunnerTokenDB()
					if db == nil {
						return nil, fmt.Errorf("runner management not configured")
					}
					store := tools.NewRunnerTokenStore(db)
					runners, err := store.ListRunners(senderID)
					if err != nil {
						return nil, err
					}
					// Populate online status from RemoteSandbox
					if sb := tools.GetSandbox(); sb != nil {
						if rs, ok := sb.(*tools.RemoteSandbox); ok {
							for i := range runners {
								runners[i].Online = rs.IsRunnerOnline(senderID, runners[i].Name)
							}
						}
					}
					// Inject built-in docker sandbox if available
					if sb := tools.GetSandbox(); sb != nil {
						if router, ok := sb.(*tools.SandboxRouter); ok && router.HasDocker() {
							dockerEntry := tools.RunnerInfo{
								Name:        tools.BuiltinDockerRunnerName,
								Mode:        "docker",
								DockerImage: router.DockerImage(),
								Online:      true,
							}
							runners = append([]tools.RunnerInfo{dockerEntry}, runners...)
						}
					}
					return runners, nil
				},
				RunnerCreate: func(senderID, name, mode, dockerImage, workspace string) (string, error) {
					db := tools.GetRunnerTokenDB()
					if db == nil {
						return "", fmt.Errorf("runner management not configured")
					}
					store := tools.NewRunnerTokenStore(db)
					token, _, err := store.CreateRunner(senderID, name, mode, dockerImage, workspace)
					if err != nil {
						return "", err
					}
					pubURL := cfg.Sandbox.PublicURL
					if pubURL == "" {
						pubURL = fmt.Sprintf("ws://%s:%d", cfg.Server.Host, cfg.Server.Port)
					}
					cmd := fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, senderID, token)
					if mode == "docker" && dockerImage != "" {
						cmd += fmt.Sprintf(" --mode docker --docker-image %s", dockerImage)
					}
					if workspace != "" {
						cmd += fmt.Sprintf(" --workspace %s", workspace)
					}
					return cmd, nil
				},
				RunnerDelete: func(senderID, name string) error {
					db := tools.GetRunnerTokenDB()
					if db == nil {
						return fmt.Errorf("runner management not configured")
					}
					// Disconnect runner if online
					if sb := tools.GetSandbox(); sb != nil {
						if rs, ok := sb.(*tools.RemoteSandbox); ok {
							rs.DisconnectRunner(senderID, name)
						}
					}
					return tools.NewRunnerTokenStore(db).DeleteRunner(senderID, name)
				},
				RunnerGetActive: func(senderID string) (string, error) {
					db := tools.GetRunnerTokenDB()
					if db == nil {
						return "", fmt.Errorf("runner management not configured")
					}
					return tools.NewRunnerTokenStore(db).GetActiveRunner(senderID)
				},
				RunnerSetActive: func(senderID, name string) error {
					db := tools.GetRunnerTokenDB()
					if db == nil {
						return fmt.Errorf("runner management not configured")
					}
					return tools.NewRunnerTokenStore(db).SetActiveRunner(senderID, name)
				},

				RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
					return agentLoop.RegistryManager().Browse(entryType, limit, offset)
				},
				RegistryInstall: func(entryType string, id int64, senderID string) error {
					return agentLoop.RegistryManager().Install(entryType, id, senderID)
				},
				RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
					return agentLoop.RegistryManager().ListMy(senderID, entryType)
				},
				RegistryPublish: func(entryType, name, senderID string) error {
					return agentLoop.RegistryManager().Publish(entryType, name, senderID)
				},
				RegistryUnpublish: func(entryType, name, senderID string) error {
					return agentLoop.RegistryManager().Unpublish(entryType, name, senderID)
				},

				RegistryUninstall: func(entryType, name, senderID string) error {
					return agentLoop.RegistryManager().Uninstall(entryType, name, senderID)
				},
				LLMList: func(senderID string) ([]string, string) {
					llmClient, currentModel, _, _ := agentLoop.LLMFactory().GetLLM(senderID)
					return llmClient.ListModels(), currentModel
				},
				LLMSet: func(senderID, model string) error {
					return agentLoop.SetUserModel(senderID, model)
				},
				LLMGetConfig: func(senderID string) (string, string, string, bool) {
					return agentLoop.GetUserLLMConfig(senderID)
				},
				LLMSetConfig: func(senderID, provider, baseURL, apiKey, model string) error {
					return agentLoop.SetUserLLM(senderID, provider, baseURL, apiKey, model)
				},
				LLMDelete: func(senderID string) error {
					return agentLoop.DeleteUserLLM(senderID)
				},
				LLMGetMaxContext: func(senderID string) int {
					return agentLoop.GetUserMaxContext(senderID)
				},
				LLMSetMaxContext: func(senderID string, maxContext int) error {
					return agentLoop.SetUserMaxContext(senderID, maxContext)
				},

				NormalizeSenderID: func(senderID string) string {
					return agentLoop.NormalizeSenderID(senderID)
				},
				SandboxWriteFile: func(senderID string, sandboxRelPath string, data []byte, perm os.FileMode) (string, error) {
					sandbox := tools.GetSandbox()
					if sandbox == nil {
						return "", fmt.Errorf("no sandbox available")
					}
					// Resolve per-user sandbox (e.g., remote runner vs docker)
					resolver, ok := sandbox.(tools.SandboxResolver)
					if !ok {
						return "", fmt.Errorf("sandbox does not support per-user resolution")
					}
					userSbx := resolver.SandboxForUser(senderID)
					if userSbx == nil || userSbx.Name() == "none" {
						return "", fmt.Errorf("no sandbox available for user %s", senderID)
					}
					// Build absolute path inside sandbox (e.g., /workspace/uploads/file.txt)
					ws := userSbx.Workspace(senderID)
					absPath := filepath.Join(ws, sandboxRelPath)
					// Ensure parent directory exists
					dir := filepath.Dir(absPath)
					if err := userSbx.MkdirAll(context.Background(), dir, 0755, senderID); err != nil {
						log.WithError(err).Warn("Failed to create directory in sandbox")
					}
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := userSbx.WriteFile(ctx, absPath, data, perm, senderID); err != nil {
						return "", err
					}
					// Return the workspace path prefix so the caller can build the display path
					return ws, nil
				},
			})
			disp.Register(webCh)
		} else {
			log.Warn("Web channel enabled but no database available, skipping")
		}
	}

	// 注入同步发送函数，使 Agent 可直接通过 Dispatcher 发送消息并获取 message_id
	agentLoop.SetDirectSend(disp.SendDirect)
	agentLoop.SetChannelFinder(disp.GetChannel)

	// 设置飞书渠道的 CardBuilder（用于卡片回调处理）
	if feishuCh != nil {
		feishuCh.SetCardBuilder(agentLoop.GetCardBuilder())

		// 传递 admin chatID 和 web DB（用于 admin 命令如 !webadd）
		if adminChatID != "" {
			feishuCh.SetAdminChatID(adminChatID)
		}
		if webDB != nil {
			feishuCh.SetWebDB(webDB)
		}

		// 注入设置卡片回调（让飞书渠道能访问 Agent 的 LLM/Registry/Settings 功能）
		feishuCh.SetSettingsCallbacks(channel.SettingsCallbacks{
			LLMList: func(senderID string) ([]string, string) {
				llmClient, currentModel, _, _ := agentLoop.LLMFactory().GetLLM(senderID)
				return llmClient.ListModels(), currentModel
			},
			LLMSet: func(senderID, model string) error {
				return agentLoop.SetUserModel(senderID, model)
			},
			LLMGetConfig: func(senderID string) (string, string, string, bool) {
				return agentLoop.GetUserLLMConfig(senderID)
			},
			LLMSetConfig: func(senderID, provider, baseURL, apiKey, model string) error {
				return agentLoop.SetUserLLM(senderID, provider, baseURL, apiKey, model)
			},
			LLMDelete: func(senderID string) error {
				return agentLoop.DeleteUserLLM(senderID)
			},
			LLMGetMaxContext: func(senderID string) int {
				return agentLoop.GetUserMaxContext(senderID)
			},
			LLMSetMaxContext: func(senderID string, maxContext int) error {
				return agentLoop.SetUserMaxContext(senderID, maxContext)
			},
			LLMGetThinkingMode: func(senderID string) string {
				return agentLoop.GetUserThinkingMode(senderID)
			},
			LLMSetThinkingMode: func(senderID string, mode string) error {
				return agentLoop.SetUserThinkingMode(senderID, mode)
			},
			ContextModeGet: func() string {
				return agentLoop.GetContextMode()
			},
			ContextModeSet: func(mode string) error {
				return agentLoop.SetContextMode(mode)
			},
			NormalizeSenderID: func(senderID string) string {
				return agentLoop.NormalizeSenderID(senderID)
			},
			RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
				return agentLoop.RegistryManager().Browse(entryType, limit, offset)
			},
			RegistryInstall: func(entryType string, id int64, senderID string) error {
				return agentLoop.RegistryManager().Install(entryType, id, senderID)
			},
			RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
				return agentLoop.RegistryManager().ListMy(senderID, entryType)
			},
			RegistryPublish: func(entryType, name, senderID string) error {
				return agentLoop.RegistryManager().Publish(entryType, name, senderID)
			},
			RegistryUnpublish: func(entryType, name, senderID string) error {
				return agentLoop.RegistryManager().Unpublish(entryType, name, senderID)
			},
			RegistryDelete: func(entryType, name, senderID string) error {
				return agentLoop.RegistryManager().Uninstall(entryType, name, senderID)
			},
			MetricsGet: func() string {
				return agent.GlobalMetrics.Snapshot().FormatMarkdown()
			},
			SandboxCleanupTrigger: func(senderID string) error {
				sb := tools.GetSandbox()
				return sb.ExportAndImport(senderID)
			},
			SandboxIsExporting: func(senderID string) bool {
				sb := tools.GetSandbox()
				return sb.IsExporting(senderID)
			},
			LLMGetPersonalConcurrency: func(senderID string) int {
				return agentLoop.GetLLMConcurrency(senderID)
			},
			LLMSetPersonalConcurrency: func(senderID string, personal int) error {
				return agentLoop.SetLLMConcurrency(senderID, personal)
			},
			RunnerConnectCmdGet: func(senderID string) string {
				token := cfg.Sandbox.AuthToken
				if token == "" {
					return ""
				}
				pubURL := cfg.Sandbox.PublicURL
				if pubURL == "" {
					// Fallback: use the xbot server address directly.
					pubURL = fmt.Sprintf("ws://%s:%d", cfg.Server.Host, cfg.Server.Port)
				}
				return fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, senderID, token)
			},
			RunnerTokenGet: func(senderID string) string {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return ""
				}
				entry := tools.NewRunnerTokenStore(db).Get(senderID)
				if entry == nil {
					return ""
				}
				return buildRunnerConnectCmd(cfg, entry)
			},
			RunnerTokenGenerate: func(senderID, mode, dockerImage, workspace string) (string, error) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return "", fmt.Errorf("remote sandbox not configured")
				}
				entry := tools.NewRunnerTokenStore(db).Generate(senderID, tools.RunnerTokenSettings{
					Mode:        mode,
					DockerImage: dockerImage,
					Workspace:   workspace,
				})
				if entry == nil {
					return "", fmt.Errorf("failed to generate token")
				}
				return buildRunnerConnectCmd(cfg, entry), nil
			},
			RunnerTokenRevoke: func(senderID string) error {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return fmt.Errorf("remote sandbox not configured")
				}
				tools.NewRunnerTokenStore(db).Revoke(senderID)
				return nil
			},
			RunnerList: func(senderID string) ([]tools.RunnerInfo, error) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return nil, fmt.Errorf("runner management not configured")
				}
				store := tools.NewRunnerTokenStore(db)
				runners, err := store.ListRunners(senderID)
				if err != nil {
					return nil, err
				}
				// Populate online status from RemoteSandbox
				if sb := tools.GetSandbox(); sb != nil {
					if rs, ok := sb.(*tools.RemoteSandbox); ok {
						for i := range runners {
							runners[i].Online = rs.IsRunnerOnline(senderID, runners[i].Name)
						}
					}
					// Inject built-in docker sandbox if available
					if sb := tools.GetSandbox(); sb != nil {
						if router, ok := sb.(*tools.SandboxRouter); ok && router.HasDocker() {
							dockerEntry := tools.RunnerInfo{
								Name:        tools.BuiltinDockerRunnerName,
								Mode:        "docker",
								DockerImage: router.DockerImage(),
								Online:      true,
							}
							runners = append([]tools.RunnerInfo{dockerEntry}, runners...)
						}
					}
				}
				return runners, nil
			},
			RunnerCreate: func(senderID, name, mode, dockerImage, workspace string) (string, error) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return "", fmt.Errorf("runner management not configured")
				}
				store := tools.NewRunnerTokenStore(db)
				token, _, err := store.CreateRunner(senderID, name, mode, dockerImage, workspace)
				if err != nil {
					return "", err
				}
				pubURL := cfg.Sandbox.PublicURL
				if pubURL == "" {
					pubURL = fmt.Sprintf("ws://%s:%d", cfg.Server.Host, cfg.Server.Port)
				}
				cmd := fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, senderID, token)
				if mode == "docker" && dockerImage != "" {
					cmd += fmt.Sprintf(" --mode docker --docker-image %s", dockerImage)
				}
				if workspace != "" {
					cmd += fmt.Sprintf(" --workspace %s", workspace)
				}
				return cmd, nil
			},
			RunnerDelete: func(senderID, name string) error {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return fmt.Errorf("runner management not configured")
				}
				if sb := tools.GetSandbox(); sb != nil {
					if rs, ok := sb.(*tools.RemoteSandbox); ok {
						rs.DisconnectRunner(senderID, name)
					}
				}
				return tools.NewRunnerTokenStore(db).DeleteRunner(senderID, name)
			},
			RunnerGetActive: func(senderID string) (string, error) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return "", fmt.Errorf("runner management not configured")
				}
				return tools.NewRunnerTokenStore(db).GetActiveRunner(senderID)
			},
			RunnerSetActive: func(senderID, name string) error {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return fmt.Errorf("runner management not configured")
				}
				return tools.NewRunnerTokenStore(db).SetActiveRunner(senderID, name)
			},
			FeishuWebLink: func(feishuUserID, username, password string) (string, error) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return "", fmt.Errorf("web linking not enabled")
				}
				return channel.FeishuLinkUser(db, feishuUserID, username, password)
			},
			FeishuWebGetLinked: func(feishuUserID string) (string, bool) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return "", false
				}
				return channel.FeishuGetLinkedUser(db, feishuUserID)
			},
			FeishuWebUnlink: func(feishuUserID string) error {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return fmt.Errorf("web linking not enabled")
				}
				return channel.FeishuUnlinkUser(db, feishuUserID)
			},
		})

		// 注入飞书渠道特化 prompt 提供者
		agentLoop.SetChannelPromptProviders(&feishuPromptAdapter{ch: feishuCh})
	}

	// 设置优雅退出（提前声明 ctx，供 OAuth Manager cleanup goroutine 使用）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置 OAuth 服务器的回调函数，使其能在授权完成后发送消息
	if oauthServer != nil {
		// 启动 OAuth flow 定期清理 goroutine
		oauthManager.Start(ctx)

		oauthServer.SetSendFunc(func(channel, chatID, content string) error {
			_, err := disp.SendDirect(bus.OutboundMessage{
				Channel: channel,
				ChatID:  chatID,
				Content: content,
			})
			return err
		})
		// 现在启动 OAuth HTTP 服务器
		if err := oauthServer.Start(); err != nil {
			log.WithError(err).Fatal("Failed to start OAuth server")
		}
		log.WithFields(log.Fields{
			"port":    cfg.OAuth.Port,
			"baseURL": cfg.OAuth.BaseURL,
		}).Info("OAuth server started")
	}

	channels := disp.EnabledChannels()
	if len(channels) == 0 {
		log.Warn("No channels enabled. Set FEISHU_ENABLED=true and configure FEISHU_APP_ID/FEISHU_APP_SECRET.")
		log.Info("Starting in agent-only mode (no IM channels)")
	} else {
		log.WithField("channels", channels).Info("Channels enabled")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 启动出站消息分发
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.WithField("panic", r).Error("Dispatcher panicked\n" + string(debug.Stack()))
			}
		}()
		disp.Run()
	}()

	// 启动所有渠道
	for name, ch := range getChannels(disp) {
		go func(n string, c channel.Channel) {
			defer func() {
				if r := recover(); r != nil {
					log.WithFields(log.Fields{"channel": n, "panic": r}).Error("Channel goroutine panicked\n" + string(debug.Stack()))
				}
			}()
			log.WithField("channel", n).Info("Starting channel...")
			if err := c.Start(); err != nil {
				log.WithError(err).WithField("channel", n).Error("Channel failed")
			}
		}(name, ch)
	}

	// 启动 Agent 循环
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.WithField("panic", r).Error("Agent loop panicked\n" + string(debug.Stack()))
				// 触发优雅退出，避免僵尸进程
				sigCh <- syscall.SIGTERM
			}
		}()
		if err := agentLoop.Run(ctx); err != nil && ctx.Err() == nil {
			log.WithError(err).Error("Agent loop exited with error")
		}
	}()

	log.Info("xbot started successfully")
	fmt.Println("🤖 xbot is running. Press Ctrl+C to stop.")

	// 启动后发送上线通知
	if cfg.StartupNotify.Channel != "" && cfg.StartupNotify.ChatID != "" {
		go sendStartupNotify(disp, cfg)
	}

	// 等待退出信号
	sig := <-sigCh
	log.WithField("signal", sig.String()).Warn("Received shutdown signal")
	fmt.Println("\nShutting down...")

	// 先取消 context，让 agent.Run() 退出（其 defer 会清理 cron 和 cleanup routine）
	cancel()

	// 等待 agent loop 退出后再继续关闭
	if agentLoop != nil {
		agentLoop.Close()
	}

	// 关闭沙箱（清理 Docker 容器等资源）
	// export/import 可能耗时较长（大容器数分钟），不设超时，必须等待完成。
	if sandbox := tools.GetSandbox(); sandbox != nil {
		if err := sandbox.Close(); err != nil {
			log.WithError(err).Warn("Sandbox close error")
		}
	}

	// 停止 OAuth 服务器
	if oauthServer != nil {
		if err := oauthServer.Shutdown(context.Background()); err != nil {
			log.WithError(err).Warn("OAuth server shutdown error")
		}
	}
	// 停止 OAuth Manager 的定期清理 goroutine
	if oauthManager != nil {
		oauthManager.Close()
	}

	// 关闭 OAuth 共享数据库连接
	if sharedDB != nil {
		if err := sharedDB.Close(); err != nil {
			log.WithError(err).Warn("OAuth shared DB close error")
		}
	}

	// 关闭 runner token 数据库连接
	if tokenDB != nil {
		if err := tokenDB.Close(); err != nil {
			log.WithError(err).Warn("Token DB close error")
		}
	}

	disp.Stop()
	log.Info("xbot stopped")
}

// createLLM 根据配置创建 LLM 客户端（带重试、指数退避和随机抖动）
func createLLM(cfg config.LLMConfig, retryCfg llm.RetryConfig) (llm.LLM, error) {
	var inner llm.LLM
	switch cfg.Provider {
	case "openai":
		inner = llm.NewOpenAILLM(llm.OpenAIConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
		})
	case "anthropic":
		inner = llm.NewAnthropicLLM(llm.AnthropicConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
		})
	default:
		return nil, fmt.Errorf("unknown LLM provider: %s", cfg.Provider)
	}

	return llm.NewRetryLLM(inner, retryCfg), nil
}

// setupLogger 配置日志
func setupLogger(cfg config.LogConfig, workDir string) error {
	return log.Setup(log.SetupConfig{
		Level:   cfg.Level,
		Format:  cfg.Format,
		WorkDir: workDir,
		MaxAge:  7, // 保留 7 天日志
	})
}

// getChannels 获取分发器中的所有渠道（辅助函数）
func getChannels(disp *channel.Dispatcher) map[string]channel.Channel {
	result := make(map[string]channel.Channel)
	for _, name := range disp.EnabledChannels() {
		if ch, ok := disp.GetChannel(name); ok {
			result[name] = ch
		}
	}
	return result
}

// sendStartupNotify 发送启动上线通知
func sendStartupNotify(disp *channel.Dispatcher, cfg *config.Config) {
	// 等待渠道 WebSocket 连接建立（轮询，最多 10 秒）
	const maxWait = 10 * time.Second
	const pollInterval = 500 * time.Millisecond
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		channels := disp.EnabledChannels()
		if len(channels) > 0 {
			// Give channels a moment to fully initialize
			time.Sleep(1 * time.Second)
			break
		}
		time.Sleep(pollInterval)
	}

	content := fmt.Sprintf("🟢 **xbot 已上线**\n- 版本：%s\n- 时间：%s\n- 模型：%s\n- 沙箱：%s\n- 记忆：%s",
		version.Info(),
		time.Now().Format("2006-01-02 15:04:05 MST"),
		cfg.LLM.Model,
		cfg.Sandbox.Mode,
		cfg.Agent.MemoryProvider,
	)

	for i := 0; i < 3; i++ {
		_, err := disp.SendDirect(bus.OutboundMessage{
			Channel: cfg.StartupNotify.Channel,
			ChatID:  cfg.StartupNotify.ChatID,
			Content: content,
		})
		if err == nil {
			log.WithFields(log.Fields{
				"channel": cfg.StartupNotify.Channel,
				"chat_id": cfg.StartupNotify.ChatID,
			}).Info("Startup notification sent")
			return
		}
		log.WithError(err).Warn("Failed to send startup notification, retrying...")
		time.Sleep(2 * time.Second)
	}
	log.Error("Failed to send startup notification after 3 attempts")
}

// feishuPromptAdapter 将 FeishuChannel 桥接为 agent.ChannelPromptProvider 接口。
// 避免在 agent 包中直接依赖 channel 包。
type feishuPromptAdapter struct {
	ch *channel.FeishuChannel
}

func (a *feishuPromptAdapter) ChannelPromptName() string {
	return a.ch.Name()
}

func (a *feishuPromptAdapter) ChannelSystemParts(ctx context.Context, chatID, senderID string) map[string]string {
	return a.ch.ChannelSystemParts(ctx, chatID, senderID)
}

// buildRunnerConnectCmd constructs the xbot-runner CLI command from a token entry.
func buildRunnerConnectCmd(cfg *config.Config, entry *tools.RunnerTokenEntry) string {
	pubURL := cfg.Sandbox.PublicURL
	if pubURL == "" {
		pubURL = fmt.Sprintf("ws://%s:%d", cfg.Server.Host, cfg.Server.Port)
	}
	cmd := fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, entry.UserID, entry.Token)
	if entry.Settings.Mode == "docker" {
		cmd += " --mode docker"
		if entry.Settings.DockerImage != "" {
			cmd += fmt.Sprintf(" --docker-image %s", entry.Settings.DockerImage)
		}
	}
	if entry.Settings.Workspace != "" && entry.Settings.Workspace != "/workspace" {
		cmd += fmt.Sprintf(" --workspace %s", entry.Settings.Workspace)
	}
	return cmd
}
