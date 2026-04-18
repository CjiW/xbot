# channel/ — Channel Adapters

## Files

| File | Purpose |
|------|---------|
| `channel.go` | Channel interface: Name/Start/Stop/Send |
| `dispatcher.go` | Outbound message routing to channels |
| `cli.go` | CLI channel entry: BubbleTea init, channel lifecycle, asyncCh drain |
| `cli_message.go` | Message rendering, streaming, tool call display, iteration snapshot (~1820 lines) |
| `cli_panel.go` | Input panels, tool status, sidebar (~2050 lines) |
| `cli_view.go` | Message list layout, markdown rendering, title bar (~830 lines) |
| `cli_model.go` | BubbleTea Model: Update/View loop (~600 lines) |
| `cli_debug.go` | Debug mode: UI capture, key injection socket, auto-input (`--debug-input`) |
| `cli_theme.go` | Lipgloss styles, color schemes, glamour config |
| `cli_runner.go` | Runner integration, process management |
| `cli_approval.go` | Tool execution confirmation dialog |
| `feishu.go` | Feishu webhook, message send, card messages (~2802 lines) |
| `feishu_settings.go` | Feishu settings UI (~1924 lines) |
| `web.go` | HTTP server, WebSocket (~1065 lines) |
| `web_api.go` | REST API endpoints (~1178 lines) |
| `web_auth.go` | OAuth/token auth (~628 lines) |
| `qq.go` | QQ bot API (~1736 lines) |
| `napcat.go` | NapCat HTTP API (~821 lines) |
| `i18n.go` | Internationalization: zh/en UI strings (~1342 lines) |
| `mermaid.go` | Mermaid → ASCII chart rendering |

## Capabilities

Optional channel capabilities via interfaces in `capability.go`:
- `SettingsCapability` — channel supports user settings UI
- `UIBuilder` — channel can render custom UI elements

## CLI Conventions

- Settings save is synchronous (`doSaveSettings` in `cli_helpers.go`) — all local I/O
- Remote CLI settings RPC must use business sender identity (for example `cli_user`) rather than WS auth user (`admin`)
- Server-side `get_settings`/`set_setting` accept payload `sender_id`; for first-time non-admin users with empty settings, they seed a small user-scoped whitelist from global CLI config (`context_mode`, `max_iterations`, `max_concurrency`, `max_context_tokens`, `enable_auto_compress`, `theme`)
- CLI TUI now centralizes user-scoped setting keys in `channel/cli_helpers.go` and uses shared merge/persist helpers instead of duplicating per-call switch lists; current user-scoped keys: `theme`, `language`, `context_mode`, `max_iterations`, `max_concurrency`, `max_context_tokens`, `enable_auto_compress`, `runner_server`, `runner_token`, `runner_workspace`
- `AskUser` tool works via CLI channel's interactive input panel
- ApprovalHook handler injected after program creation (`cli.go:139`)

### CLI Debug Infrastructure (`--debug`)

- `--debug` enables Unix socket for key injection + periodic UI capture (2000-line ring buffer)
- `--debug-input "seq"` auto-injects key sequences after 2s splash delay (e.g., `"esc,sleep:1,hello,enter,ctrl+c"`)
- `--debug-capture-ms N` controls capture interval (default 1000ms)
- **`parseKeyInput` must NOT set `Text` field when modifier is present.** `Key.String()` returns `Text` if non-empty, bypassing `Keystroke()` — so `{Code:'c', Text:"c", Mod:ModCtrl}.String()` returns `"c"` not `"ctrl+c"`, breaking cancel detection.

### CLI asyncCh Pattern (Remote Mode)

- `asyncCh` (buffered-64) is the **sole intermediary** for all non-startup `program.Send()` calls
- `handleAsyncDrain` goroutine is the only `program.Send()` caller (prevents keyboard readLoop starvation)
- All progress, outbound messages, SetProcessing, SendToast, InjectUserMessage route through `asyncCh`
- `progressCh` (buffered-1) drains into `asyncCh` via `handleProgressDrain`

### CLI Iteration Snapshots (Tool Summary)

- Iteration snapshots track reasoning, thinking, tools, and wall-clock time per iteration
- **Deduplication**: when `PhaseDone` and `handleAgentMessage` both snapshot the same iteration, prefer PhaseDone version (has complete reasoning from server)
- `ElapsedWall` must be set in ALL snapshot creation paths (iteration change, PhaseDone, handleAgentMessage) — missing it causes fallback to sum only last iteration's tool.Elapsed
- Title bar shows `[host:port]` in remote mode (parsed from `RemoteBackend.ServerURL()`)
