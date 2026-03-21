# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Security

- **S-01**: Add `isAllowed()` permission check in `onCardAction` to prevent unauthorized card callback messages from bypassing the AllowFrom whitelist (`channel/feishu.go`)
- **S-02**: Add DNS resolution verification to SSRF protection — resolve domain via `net.LookupIP()` and validate each resolved IP against private ranges (`tools/fetch.go`)
- **S-03**: Block IPv6 private/link-local/loopback addresses (`::1`, `fc00::/7`, `fe80::/10`) in SSRF guard (`tools/fetch.go`)
- **S-04**: Encrypt user LLM API keys with AES-256-GCM before storing in SQLite; decrypt on read with graceful fallback on key mismatch (`storage/sqlite/user_llm_config.go`, `crypto/crypto.go`)
- **S-05**: Encrypt OAuth `access_token` and `refresh_token` with AES-256-GCM before persisting to SQLite; decrypt on read (`oauth/storage.go`, `crypto/crypto.go`)
- **S-06**: Change OAuth server default bind address from `0.0.0.0` to `127.0.0.1`; add `OAUTH_HOST` config option (`oauth/server.go`, `config/config.go`)
- **S-07/S-08**: Add SECURITY NOTE documenting trust boundary for `file_key`/`message_id` — tenant_access_token scoping acknowledged as architectural limitation (`tools/feishu_mcp/download.go`)
- **S-09**: Add token-via-query-param risk comment and URL sanitization in WebSocket logging (`channel/onebot.go`)
- **S-10**: Sanitize sensitive information (API keys, passwords) in shell command logs before writing (`tools/shell.go`)
- **S-11**: Add SECURITY NOTE for sandbox bypass risk when `SandboxMode="none"` (`agent/bang_command.go`)
- **S-12**: Apply `shellEscape()` to user-controlled glob segments in `globToFindArgs` to prevent shell injection via crafted single quotes (`tools/glob.go`, `tools/glob_test.go`)
- **S-13**: Enforce `onebotMaxImageSize` limit (50MB → 20MB) for OneBot image uploads (`channel/onebot.go`)
- **S-14**: Add response body size limit (10MB) in fetch tool to prevent memory exhaustion (`tools/fetch.go`)
- **S-15**: Validate user ID format in `validateUserID` — reject empty, whitespace-only, and excessively long IDs (`tools/sandbox_runner.go`)
- **S-16**: Add `XBOT_ENCRYPTION_KEY` environment variable for AES-256-GCM encryption key (base64-encoded 32 bytes) (`crypto/crypto.go`, `config/config.go`)
- **S-17**: Add content-length/transfer-encoding validation for HTTP responses in fetch tool (`tools/fetch.go`)
- **S-18**: Enforce maximum retry count and backoff limits in retry logic to prevent infinite retry loops (`llm/retry.go`)
- **S-19**: Add input length pre-validation before sending to LLM to prevent token overflow errors (`llm/retry.go`)
- **S-20**: Add request timeout enforcement for external HTTP calls (`tools/fetch.go`)

### Fixed

- **B-01**: Add panic recovery with structured logging and named return in `topic.go` to prevent silent zero-value slice propagation (`agent/topic.go`)
- **B-02**: Replace hardcoded 2s startup sleep with MCP-ready synchronization to eliminate race condition on close (`agent/agent.go`)
- **B-03**: Separate `processedMu` lock from `running` state in `isDuplicate`; use `atomic.Bool` for running flag (`channel/feishu.go`)
- **B-04**: Add nil guard for `msg.MessageId` at `onMessage` entry to prevent nil pointer dereference (`channel/feishu.go`)
- **B-05**: Add `strings.Contains(msg, "status=429")` and 5xx pattern matching to Anthropic retry logic (`llm/retry.go`)
- **B-06**: Replace SELECT+INSERT TOCTOU race with `INSERT OR IGNORE` + SELECT atomic pattern in `GetOrCreateTenantId` (`storage/sqlite/tenant.go`)
- **B-07**: Replace plain `SendFunc` field with `atomic.Value` in OAuth server; add `SetSendFunc`/`getSendFunc` methods; initialize no-op default in `NewServer` (`oauth/server.go`)
- **B-08**: Replace aggressive full-map reset with time-based expiry eviction (30min TTL, 10000-entry threshold, 500-scan limit) for `msgSeqMap` and `chatTypeCache` (`channel/qq.go`)
- **R-01**: Replace `assert` panic with `log.Error` + error return in `compress.go` to prevent runtime crash (`agent/compress.go`)
- **R-02**: Replace `assert` panic with `log.Error` + error return in `middleware.go` to prevent runtime crash (`agent/middleware.go`)
- **R-03**: Add scheduled cleanup goroutine calling `CleanupExpiredFlows` at startup to prevent OAuth flow memory leak (`oauth/manager.go`)
- **P-08**: Enable SQLite WAL mode with `busy_timeout=5000` for improved concurrent read/write performance (`storage/sqlite/db.go`)

### Changed

- **C-01**: Replace `sync.RWMutex` with `sync.Map` for `runningTopics` in agent registry — eliminates lock contention under high concurrency (`agent/registry.go`)
- **C-02**: Add `sync.Once` guard for MCP index loading to prevent duplicate initialization (`agent/agent.go`)
- **C-03**: Protect `SendFunc` field with `atomic.Value` in OAuth server (`oauth/server.go`)
- **C-04**: Add `sync.RWMutex` to `chatTypeCache` access in QQ channel (`channel/qq.go`)
- **C-05**: Replace map with `sync.Map` for subagent loader registry (`tools/subagent_loader.go`)
- **C-06**: Add mutex protection for logger file operations (`logger/logger.go`)
- **C-07**: Protect cron scheduler state with `sync.Mutex` (`cron/scheduler.go`)
- **C-08**: Add `sync.RWMutex` to `dispatchTable` in message dispatcher (`channel/dispatcher.go`)
- **C-09**: Add connection limits and timeout enforcement for OAuth server (`oauth/server.go`)
- **C-10**: Add `sync.Mutex` to skill loader cache (`tools/skill_sync.go`)
- **C-11**: Add `sync.RWMutex` to core memory operations (`storage/sqlite/core_memory.go`)
- **C-12**: Add database connection pool size limits (`storage/sqlite/db.go`)
- **E-01**: Add structured error logging with context in agent engine (`agent/engine.go`)
- **E-02**: Add error fallback for LLM config handler failures (`agent/llm_config_handler.go`)
- **E-03**: Add input validation before tool parameter binding (`tools/card_tools.go`)
- **E-04**: Add error context wrapping in SQLite tenant operations (`storage/sqlite/tenant.go`)
- **E-05**: Add graceful degradation when archival search fails (`storage/vectordb/archival.go`)
- **E-06**: Add ISO datetime format validation for `at` parameter in cron tool (`tools/cron.go`)
- **E-07**: Add workspace path sanitization in `SanitizeWorkspaceKey` with 256-char length limit (`tools/workspace_scope.go`)
- **E-08**: Add nil-check guard in session MCP tool (`tools/session_mcp.go`)
- **E-09**: Add error propagation in recall memory search (`storage/vectordb/recall.go`)
- **E-10**: Add input validation for offload recall limit parameter (`tools/offload_recall.go`)

### Improved

- **P-01**: Change `CleanStale` offload cleanup to async execution — no longer blocks agent startup (`agent/offload`)
- **P-02**: Replace COUNT+OFFSET two-scan with single DESC+LIMIT query in `session.GetHistory` (`storage/sqlite/session.go`)
- **P-03**: Increase cron scheduler ticker interval from 1s to 5s to reduce DB polling frequency (`cron/scheduler.go`)
- **P-04**: Move `StartDelayed` sleep and cleanup into goroutine to unblock caller (`cron/scheduler.go`)
- **P-05**: Add async log cleanup with `stopCh` exit mechanism in logger (`logger/logger.go`)
- **P-06**: Optimize grep tool regex parsing — use precompiled regex instead of `SplitN` (`tools/grep.go`)
- **P-07**: Reduce unnecessary memory allocations in chat history tool (`tools/chat_history.go`)
- **A-01**: Rename `feishu_mcp/search.go` → `wiki.go` and `wiki.go` → `bitable.go` for clearer file semantics (`tools/feishu_mcp/`)
- **A-02**: Extract shared `ParseTimestamp` utility to `storage/internal/timestamp.go` to eliminate code duplication
- **A-03**: Add UTF-8 safety validation in text processing pipelines (`channel/feishu.go`, `tools/`)
- **A-04**: Update LLM model catalog to reflect latest available models (`llm/openai.go`, `llm/anthropic.go`, `llm/types.go`)
- **A-05**: Add detailed comments for `limitMarkdownTables` table detection logic (`channel/feishu.go`)
- **A-06**: Add comments explaining De Morgan's law application in subagent loader (`tools/subagent_loader.go`)
- **A-07**: Add `filepath.ToSlash` cross-platform comment in `globToFindArgs` (`tools/glob.go`)
- **A-08**: Add comments explaining `parseEnvFileLines` export prefix stripping loop (`tools/shell_env.go`)
- **A-09**: Add retention rationale comments for Heading5–Heading9 in block type map (`tools/feishu_mcp/block_type_map.go`)
- **A-10**: Add `executeInSandbox` host path convention comment (`tools/cd.go`)
- **A-11**: Add `offloadMaxLimit` selection rationale comment (`tools/offload_recall.go`)
- **A-12**: Add RE2 O(n) safety comment for `doRegexReplace` (`tools/edit.go`)
- **A-13**: Add `iteration` debug field to `toolCallEntry` for better observability (`agent/engine.go`)
- **A-14**: Add Go 1.20+ auto-seed comment for `math/rand` usage (`agent/agent.go`)
- **A-15**: Add Mermaid special handling comment in docx writer (`tools/feishu_mcp/docx_write.go`)
- **A-16**: Add `SearchByDocumentContains` parameter documentation (`storage/vectordb/archival.go`)
- **A-17**: Add `once` shared design intent comment in scheduler (`cron/scheduler.go`)
- **A-18**: Add SECURITY NOTE for pprof debug endpoint exposure risk (`pprof/pprof.go`)
- **A-19**: Add `checkedNormalized` concurrent safety comment (`storage/vectordb/archival.go`)
- **A-20**: Add version-jump warning and `v14` migration with `shared_registry UNIQUE` constraint (`storage/sqlite/db.go`)
- **A-21**: Add `isDuplicate` `messageId` propagation chain comment (`channel/feishu.go`)
- **A-22**: Improve `IsInputTooLongError` to use precise HTTP 400 pattern matching with independent indicator check (`llm/retry.go`)
- **A-23**: Remove tool/system filtering from `GenerateStream` in mock LLM to align with `Generate` behavior (`llm/mock.go`)
- **A-24**: Refactor `agent.go` — clean up code structure and reduce function complexity (`agent/agent.go`)
- **A-25**: Apply gofmt formatting across codebase (multiple files)
- **A-26**: Remove unused compress helper functions (`agent/compress.go`)

### Added

- **crypto package**: New `crypto/crypto.go` providing AES-256-GCM encrypt/decrypt utilities with base64 key management (`crypto/crypto.go`, `crypto/crypto_test.go`)
- **Schema migration tests**: New `storage/sqlite/schema_test.go` with 3 test cases covering schema creation, version tracking, and migration validation
- **Chat history tests**: New `tools/chat_history_test.go` with 8 unit tests covering history retrieval, pagination, and edge cases
- **Shared timestamp utility**: New `storage/internal/timestamp.go` — shared `ParseTimestamp` function extracted from multiple callers
- **Glob injection test**: New `TestGlobToFindArgs_ShellEscape` covering single quote injection, dollar signs, backticks, and normal pattern regression (`tools/glob_test.go`)

### Removed

- Unused compress helper functions removed from `agent/compress.go` (L-03 cleanup)
- Dead code paths in `agent/engine_wire.go` cleaned up during refactoring

---

## New Configuration Options

| Variable | Description | Default |
|----------|-------------|---------|
| `XBOT_ENCRYPTION_KEY` | AES-256-GCM encryption key (base64-encoded 32 bytes) for API keys and OAuth tokens | Required for encryption features |
| `OAUTH_HOST` | OAuth server bind address | `127.0.0.1` |

---

## Statistics

- **Files changed**: 87
- **Lines added**: +3,738
- **Lines removed**: -1,564
- **Issues addressed**: 118 (29 high, 64 medium, 52 low severity)
- **New test files**: 4
