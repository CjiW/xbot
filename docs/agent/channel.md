# channel/ — Channel Adapters

## Files

| File | Purpose |
|------|---------|
| `channel.go` | Channel interface: Name/Start/Stop/Send |
| `dispatcher.go` | Outbound message routing to channels |
| `cli.go` | CLI channel entry: BubbleTea init, channel lifecycle |
| `cli_message.go` | Message rendering, streaming, tool call display (~1715 lines) |
| `cli_panel.go` | Input panels, tool status, sidebar (~2050 lines) |
| `cli_view.go` | Message list layout, markdown rendering (~783 lines) |
| `cli_model.go` | BubbleTea Model: Update/View loop (~595 lines) |
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
- `AskUser` tool works via CLI channel's interactive input panel
- ApprovalHook handler injected after program creation (`cli.go:139`)
