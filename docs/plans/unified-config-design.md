# 配置系统统一方案：全面迁移至 config.json

> 状态：approved | 创建：2026-04-10 | 审查：2026-04-11

## 背景

当前配置体系存在 4 个来源，职责分裂：

```
.env 文件 (godotenv)  →  环境变量 (80个)  →  config.json  →  硬编码默认值
```

**核心问题：**

1. 同一字段两套命名（JSON 字段 vs 环境变量，如 `LLM_RETRY_*` → `Agent.*`）
2. `.env` 使用相对路径 `.env`，Docker/systemd 部署时行为不一致
3. 部分变量绕过 Config 结构体（`XBOT_ENCRYPTION_KEY`、`WEB_USER_SERVER_RUNNER`）
4. 不能通过空环境变量清零 config.json 中的值
5. CLI 保存 config.json 后，环境变量会覆盖回去，形成冲突
6. `applyEnvOverrides()` 占 300 行维护成本高

## 目标

**config.json 为唯一持久化配置源**，环境变量仅保留元配置级别的 3 个变量。

```
config.json (primary) → Config struct → Hardcoded defaults (zero-value fallback)

保留环境变量（元配置）：
  XBOT_HOME          — 决定 config.json 存放位置
  XBOT_ENCRYPTION_KEY — AES-256 加密密钥，安全考虑不放文件
  XBOT_TEST_DOCKER   — 测试开关
```

## 方案详情

### 1. 删除 .env 文件支持

- 移除 `config/config.go` 中 `init()` 的 `godotenv.Load(".env")`
- 移除 `github.com/joho/godotenv` 依赖
- `.env.example` 替换为 `config.example.json`（带注释的 JSON 模板）

### 2. 删除 applyEnvOverrides()

- 移除 `config/config.go:310-605` 整个函数（~300 行）
- `Load()` 简化为：

```go
func Load() *Config {
    cfg := LoadFromFile(ConfigFilePath())
    if cfg == nil {
        cfg = &Config{}
    }
    applyDefaults(cfg)
    return cfg
}
```

### 3. WEB_USER_SERVER_RUNNER 纳入 Config 结构体

当前 `tools/sandbox_router.go:167` 直接读环境变量，应移入 Config：

```go
// config.go — SandboxConfig 中新增
type SandboxConfig struct {
    // ... existing fields ...
    AllowWebUserServerRunner bool `json:"allow_web_user_server_runner"`
}
```

对应修改 `tools/sandbox_router.go` 从 Config 读取。

### 4. LLM_RETRY_* 命名修正

当前 `LLM_RETRY_*` 环境变量映射到 `Agent.*` 字段，命名不一致。统一后不再需要环境变量映射，Config 内部结构保持不变，仅更新文档。

### 5. 清理废弃内容

- `.env.example` 中 `SINGLE_USER` 注释已废弃，删除
- `config.go:374` 中 `SINGLE_USER` 相关注释删除

### 6. 提供 --config 命令行参数

主服务和 CLI 支持通过命令行参数指定配置文件路径：

```go
--config string  (default: $XBOT_HOME/config.json)
```

### 7. 迁移工具

CLI 首次运行时检测 `.env` 文件存在，提供自动迁移：

```go
func migrateFromDotenv(dotenvPath, configPath string) error {
    // 1. 读取 .env 文件
    // 2. 加载已有 config.json（或创建空 Config）
    // 3. 将 .env 中的有效变量映射到 Config 字段
    // 4. SaveToFile
    // 5. 将 .env 重命名为 .env.migrated
    // 6. 日志提示迁移完成
}
```

映射表：`applyEnvOverrides()` 中已有的 80 个环境变量 → Config 字段路径，这个逻辑可以直接复用。

## 部署方式变更

| 场景 | 现在 | 改后 |
|------|------|------|
| Docker | 挂载 `.env` 或注入大量 env | 挂载 `config.json` |
| K8s | env 注入 | ConfigMap → 挂载卷 |
| systemd | `EnvironmentFile=.env` | `--config /path/to/config.json` |
| 本地开发 | 复制 `.env.example` → `.env` | 复制 `config.example.json` → `~/.xbot/config.json` |

## 兼容性

| 组件 | 影响 | 处理 |
|------|------|------|
| 主服务 main.go | 仅 `config.Load()` 调用，无直接影响 | 无需改动 |
| CLI cmd/xbot-cli | 已用 `SaveToFile`，无直接影响 | 新增迁移逻辑 |
| Runner cmd/runner | 独立配置体系，不导入 config 包 | 无需改动 |
| crypto/crypto.go | 独立读 `XBOT_ENCRYPTION_KEY` | 保持不变 |
| 现有 Docker 用户 | 习惯用 env 注入 | 迁移工具 + 过渡期 |
| 现有 .env 用户 | 习惯用 .env 文件 | 自动迁移到 config.json |

## 风险与缓解

| 风险 | 级别 | 缓解 |
|------|------|------|
| 敏感信息泄露（config.json 明文 API Key） | 低 | 现状相同（权限 0o600）；可选引入 XBOT_ENCRYPTION_KEY 加密存储 |
| Docker/K8s 用户不挂载文件 | 中 | 过渡期保留 `LLM_API_KEY` 等关键变量作为覆盖层 |
| .env 用户未迁移 | 中 | CLI 启动时自动检测并迁移 |
| Config 结构体变更导致 JSON 反序列化失败 | 低 | Go JSON 反序列化对未知字段宽容 |

## 实施路径

### Phase 1 — 清理（低风险，可立即执行）

- [ ] 提取 `applyDefaults(cfg)` 函数（将 `Load()` 中 625-748 行的默认值逻辑独立）
- [ ] `WEB_USER_SERVER_RUNNER` 纳入 Config 结构体
- [ ] `.env.example` 改为 `config.example.json`
- [ ] 清理废弃注释（`SINGLE_USER` 等）
- [ ] 添加 `--config` 命令行参数（`Load()` → `Load(configPath string)`）
- [ ] `getAdminChatID()` 环境变量读取清理

### Phase 2 — 过渡期（中风险）

- [ ] `applyEnvOverrides()` 标记 deprecated，日志 warn 提示迁移
- [ ] CLI 首次运行时检测 `.env` 并自动合并到 config.json
- [ ] 关键环境变量（`LLM_API_KEY`、`LLM_BASE_URL`、`LLM_PROVIDER`）保留覆盖能力，标记 deprecated
- [ ] 更新部署文档

### Phase 3 — 统一（破坏性变更）

- [ ] 删除 godotenv 依赖
- [ ] 删除 `applyEnvOverrides()`
- [ ] 删除 `getAdminChatID()` 中的环境变量读取
- [ ] `Load()` 简化为 `file → defaults`
- [ ] 更新 Dockerfile / CI 配置
- [ ] CHANGELOG 记录 breaking change

## 审查记录（2026-04-11）

代码验证结果：
- ✅ `applyEnvOverrides()` 实际 310-605 行（296 行），覆盖 80 个环境变量
- ✅ `WEB_USER_SERVER_RUNNER` 在 `tools/sandbox_router.go:167` 直接 `os.Getenv`，确认绕过 Config
- ✅ `cmd/runner` 不导入 config 包，无影响
- ✅ `crypto/crypto.go` 独立读 `XBOT_ENCRYPTION_KEY`，保留
- ✅ `main.go` 和 `cmd/xbot-cli/main.go` 均只调用 `config.Load()`，无直接环境变量读取
- ⚠️ 遗漏：`getAdminChatID()`（config.go:754-758）仍读 `ADMIN_CHAT_ID`/`STARTUP_NOTIFY_CHAT_ID` 环境变量，Phase 3 需清理
- ⚠️ 遗漏：`Load()` 内默认值逻辑（625-748 行）散落在函数体内，应先提取 `applyDefaults()`

