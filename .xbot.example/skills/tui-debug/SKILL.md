---
name: tui-debug
description: "Debug xbot TUI and remote mode issues. Launches isolated CLI client (and optionally server) with --debug flags, auto-input key sequences, and periodic UI captures. Activate when you need to reproduce, diagnose, or verify TUI bugs (spinner freeze, key event delay, cancel flow, render issues) or end-to-end remote mode issues."
---

# TUI Debug Skill

Debug xbot TUI issues by controlling isolated debug instances from the agent's shell.

## Scope

This skill debugs the **CLI TUI client** (`xbot-cli`). It can run in three modes:

| Mode | What it does | When to use |
|------|-------------|-------------|
| `local` | Start CLI in local (in-process) mode | TUI-only bugs: rendering, key handling, panel navigation |
| `remote` | Start CLI connecting to an **existing** server | Client-side remote bugs: cancel flow, progress display, message rendering |
| `e2e` | Start **both** server + client in isolation | Full remote mode bugs: WS protocol, server-side cancel, progress delivery |

> **This skill does NOT debug the server process itself.** For pure server issues (agent loop, tool execution, DB), read server logs directly.

## Architecture

- **Isolation**: Debug instances use `XBOT_HOME=/tmp/xbot-debug-$UID` with dedicated workdir. No config, data, or session pollution.
- **Control**: `--debug` flag enables Unix socket for key injection and periodic UI capture (configurable interval, 2000-line ring buffer).
- **Auto-input**: `--debug-input "seq"` auto-injects key sequences after startup (e.g., `"esc,enter,sleep:5,ctrl+c"`).
- **PTY**: Client runs inside `script` to provide a pseudo-terminal (Bubble Tea needs raw mode).

## Key Paths

| Item | Path |
|------|------|
| XBOT_HOME | `/tmp/xbot-debug-$UID` |
| Config (client) | `/tmp/xbot-debug-$UID/config.json` |
| Config (server, e2e mode) | `/tmp/xbot-debug-$UID/server-home/config.json` |
| Debug socket | `/tmp/xbot-debug-$UID/debug/ctl.sock` |
| UI capture | `/tmp/xbot-debug-$UID/debug/ui_capture.log` |
| App logs (client) | `/tmp/xbot-debug-$UID/logs/xbot.log` |
| App logs (server, e2e mode) | `/tmp/xbot-debug-$UID/server.log` |
| Binary | `/tmp/xbot-debug-$UID/xbot-cli` |

## Requirements

- `socat` — for sending keys to the debug socket
- `script` — for providing a pseudo-terminal (pre-installed on Linux/macOS)
- `python3` — for config manipulation (optional, has sed fallback)

## Workflow

### 1. Quick Start

```bash
# Local mode (TUI-only)
export XBOT_DEBUG_HOME="/tmp/xbot-debug-$(id -u)"
bash <SKILL_DIR>/scripts/start.sh local

# Remote mode (client only, server must already be running)
bash <SKILL_DIR>/scripts/start.sh remote "ws://127.0.0.1:9999"

# End-to-end (starts both server + client)
bash <SKILL_DIR>/scripts/start.sh e2e
```

`start.sh` auto-detects the xbot source root via `git rev-parse`. Override with `XBOT_SRC`.

Additional flags (pass after mode):
```bash
# Custom capture interval and auto-input
bash <SKILL_DIR>/scripts/start.sh local --capture-ms 200 --input 'hello,enter,sleep:5,ctrl+c'
```

### 2. Auto-Input (One-Shot Replay)

For fully automated testing, use `--debug-input` (or `--input` via start.sh) to specify a key sequence injected automatically after startup (2s splash delay):

```bash
# Type message, enter, wait for response, ctrl+c
$BIN --debug --debug-input 'what is 2+2,enter,sleep:10,ctrl+c,sleep:3'

# High-frequency capture for freeze analysis
$BIN --debug --debug-capture-ms 200 --debug-input 'hello,enter,ctrl+c'

# Close setup panel first (esc), then interact
$BIN --debug --debug-input 'esc,sleep:1,hello,enter'
```

**Sequence syntax** (comma-separated items):
- **Special keys**: `enter`, `tab`, `esc`, `up`, `down`, `left`, `right`, `backspace`, `delete`, `space`, `f1`-`f12`, `home`, `end`, `pgup`, `pgdown`
- **Modifier combos**: `ctrl+c`, `ctrl+z`, `alt+enter`, `shift+tab`
- **Text**: any multi-char string (sent char-by-char, 50ms apart)
- **Sleep**: `sleep:N` — pause N seconds before next key

### 3. Send Keys (Manual)

```bash
SOCK="$XBOT_DEBUG_HOME/debug/ctl.sock"

# Single key
echo "ctrl+c" | socat - UNIX-CONNECT:"$SOCK"

# Type a string + trailing key (using helper script)
bash <SKILL_DIR>/scripts/send-keys.sh "hello world" "enter"
```

### 4. Read UI State

```bash
# Strip ANSI for readable output
strip() { sed 's/\x1b\[[0-9;]*m//g'; }

# Latest capture
cat "$XBOT_DEBUG_HOME/debug/ui_capture.log" | strip | tail -40

# Wait for pattern (strips ANSI automatically)
bash <SKILL_DIR>/scripts/wait-ui.sh "ready" 15
bash <SKILL_DIR>/scripts/wait-ui.sh "Thinking" 10
```

### 5. Read Logs

```bash
# Client logs
cat "$XBOT_DEBUG_HOME/logs/xbot.log"
grep -E 'level=(error|warn)' "$XBOT_DEBUG_HOME/logs/xbot.log" | tail -20

# Server logs (e2e mode only)
cat "$XBOT_DEBUG_HOME/server.log" | tail -30
```

### 6. Stop & Cleanup

```bash
# Stop all (preserves logs)
bash <SKILL_DIR>/scripts/stop.sh

# Stop and remove all debug files
bash <SKILL_DIR>/scripts/stop.sh clean
```

## Debugging Patterns

### Reproduce Ctrl+C Freeze (Remote Mode)

**Automated (recommended):**
```bash
export XBOT_DEBUG_HOME="/tmp/xbot-debug-$(id -u)"
bash <SKILL_DIR>/scripts/start.sh e2e --capture-ms 200 --input 'esc,sleep:1,write a 500 line python script,enter,sleep:10,ctrl+c,sleep:3'

# Wait for completion, then analyze
sleep 25
strip() { sed 's/\x1b\[[0-9;]*m//g'; }
echo "=== Progress -> Cancel -> Ready transition ==="
cat "$XBOT_DEBUG_HOME/debug/ui_capture.log" | strip | grep -E 'Thinking|Analyzing|Considering|ready|Cancel' | tail -20
```

**Expected (healthy):** `Thinking Ns` -> `ready` within 1 capture interval after ctrl+c.

**Frozen:** `Thinking Ns` continues for many seconds after ctrl+c with no state change.

**Manual (interactive):**
```bash
bash <SKILL_DIR>/scripts/start.sh remote "ws://server:port"
bash <SKILL_DIR>/scripts/send-keys.sh "write a 500 line python script" "enter"
bash <SKILL_DIR>/scripts/wait-ui.sh "Thinking" 10
echo "ctrl+c" | socat - UNIX-CONNECT:"$XBOT_DEBUG_HOME/debug/ctl.sock"
bash <SKILL_DIR>/scripts/wait-ui.sh "ready" 3
```

### Measure Event Loop Responsiveness

Compare capture timestamps to detect UI stalls:

```bash
strip() { sed 's/\x1b\[[0-9;]*m//g'; }
cat "$XBOT_DEBUG_HOME/debug/ui_capture.log" | strip | grep '^--- ' | awk '{print $1,$2}' | uniq -c

# Normal: consistent ~500ms gaps (matching capture interval)
# Frozen: gaps > 2x capture interval, or same timestamp repeating
```

### Diagnose Key Event Issues

```bash
# Check if key events appear in logs
grep 'DEBUG keypress' "$XBOT_DEBUG_HOME/logs/xbot.log" | tail -10
```

### Server-Side Cancel Verification

In e2e mode, check both client and server:

```bash
# Client: did cancel request send?
grep -i 'cancel' "$XBOT_DEBUG_HOME/logs/xbot.log" | tail -5

# Server: did it receive and process cancel?
grep -iE 'cancel|context canceled|abort' "$XBOT_DEBUG_HOME/server.log" | tail -10
```

## Tips

- **Timing**: After sending a key, add `sleep 0.5` before reading UI capture.
- **One at a time**: Kill previous instance before starting a new one (`start.sh` does this).
- **Config editing**: Edit `$XBOT_DEBUG_HOME/config.json` directly to change LLM/model settings.
- **Ring buffer**: `ui_capture.log` keeps 2000 lines. Each entry separated by `--- HH:MM:SS ---`.
- **No real terminal needed**: `script` provides the PTY, so the debug instance runs headless.
- **ANSI stripping**: Always pipe capture through `sed 's/\x1b\[[0-9;]*m//g'` before grepping.
- **Setup panel**: Isolated instances may show setup panel on first run. Use `esc` as first key in auto-input to dismiss it.
