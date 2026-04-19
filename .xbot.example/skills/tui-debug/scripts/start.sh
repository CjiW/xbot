#!/usr/bin/env bash
# start.sh — Build, setup isolated env, and start xbot-cli in debug mode.
#
# Usage:
#   bash start.sh local   [--capture-ms N] [--input "seq"]
#   bash start.sh remote <ws_url> [--capture-ms N] [--input "seq"]
#   bash start.sh e2e     [--capture-ms N] [--input "seq"]
#
# Modes:
#   local  — CLI in-process mode (no server)
#   remote — CLI connects to an existing server (must provide ws_url)
#   e2e    — Starts both a test server AND client in isolation
#
# Environment variables:
#   XBOT_DEBUG_HOME  — Isolated XBOT_HOME directory (default: /tmp/xbot-debug-$UID)
#   XBOT_SRC         — xbot source root (auto-detected via git if not set)
#   E2E_SERVER_PORT  — Port for e2e test server (default: 18080)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

XBOT_DEBUG_HOME="${XBOT_DEBUG_HOME:-/tmp/xbot-debug-$(id -u)}"
XBOT_DEBUG_WORKDIR="$XBOT_DEBUG_HOME/workdir"
XBOT_DEBUG_BIN="$XBOT_DEBUG_HOME/xbot-cli"

# Auto-detect xbot source root
if [ -n "${XBOT_SRC:-}" ]; then
  XBOT_SRC="$XBOT_SRC"
elif [ -f "$SCRIPT_DIR/../../../../cmd/xbot-cli/main.go" ]; then
  XBOT_SRC="$(cd "$SCRIPT_DIR/../../.." && pwd)"
elif command -v git >/dev/null 2>&1; then
  XBOT_SRC="$(git rev-parse --show-toplevel 2>/dev/null)" || true
fi
if [ -z "$XBOT_SRC" ] || [ ! -f "$XBOT_SRC/cmd/xbot-cli/main.go" ]; then
  echo "ERROR: Cannot find xbot source root. Set XBOT_SRC or run from within the repo." >&2
  exit 1
fi

# Parse mode (first arg)
MODE="${1:-local}"
shift || true

# For remote mode, next arg is server URL (if it doesn't start with --)
SERVER_URL=""
TOKEN=""
if [ "$MODE" = "remote" ] && [ $# -gt 0 ] && [ "${1#--}" = "$1" ]; then
  SERVER_URL="$1"
  shift
fi

# Parse optional flags
CAPTURE_MS=""
INPUT_SEQ=""
while [ $# -gt 0 ]; do
  case "$1" in
    --capture-ms) CAPTURE_MS="$2"; shift 2 ;;
    --input)      INPUT_SEQ="$2"; shift 2 ;;
    *) shift ;;
  esac
done

echo "=== TUI Debug Setup ==="
echo "XBOT_HOME:  $XBOT_DEBUG_HOME"
echo "Source:     $XBOT_SRC"
echo "Mode:       $MODE"
[ "$MODE" = "remote" ] && [ -n "$SERVER_URL" ] && echo "Server:     $SERVER_URL"
echo "Workdir:    $XBOT_DEBUG_WORKDIR"
[ -n "$CAPTURE_MS" ] && echo "Capture:    ${CAPTURE_MS}ms"
[ -n "$INPUT_SEQ" ] && echo "Auto-input: $INPUT_SEQ"
echo ""

# Kill any existing debug instance
if [ -f "$XBOT_DEBUG_BIN" ]; then
  # Use pgrep with exact binary path to avoid killing wrong processes
  pgrep -f "$XBOT_DEBUG_BIN" >/dev/null 2>&1 && pkill -f "$XBOT_DEBUG_BIN" 2>/dev/null || true
fi
sleep 0.5

# Build
echo "[1/5] Building..."
cd "$XBOT_SRC"
go build -o "$XBOT_DEBUG_BIN" ./cmd/xbot-cli/
echo "  Binary: $XBOT_DEBUG_BIN"

# Create isolated environment
echo "[2/5] Creating isolated environment..."
mkdir -p "$XBOT_DEBUG_HOME/logs" "$XBOT_DEBUG_WORKDIR"

# Seed config from real config (preserving LLM keys etc.)
if [ -f ~/.xbot/config.json ]; then
  cp ~/.xbot/config.json "$XBOT_DEBUG_HOME/config.json"
  echo "  Config: copied from ~/.xbot/config.json"
else
  echo '{}' > "$XBOT_DEBUG_HOME/config.json"
  echo "  Config: created minimal empty config"
fi

> "$XBOT_DEBUG_HOME/logs/xbot.log"

# --- e2e mode: start server ---
SERVER_PID=""
if [ "$MODE" = "e2e" ]; then
  echo "[3/5] Starting test server..."

  SERVER_PORT="${E2E_SERVER_PORT:-18080}"
  SERVER_HOME="$XBOT_DEBUG_HOME/server-home"
  mkdir -p "$SERVER_HOME/logs"

  # Create server config from real config
  if [ -f ~/.xbot/config.json ]; then
    cp ~/.xbot/config.json "$SERVER_HOME/config.json"
    python3 -c "
import json
with open('$SERVER_HOME/config.json') as f:
    cfg = json.load(f)
cfg.setdefault('web', {})
cfg['web']['enable'] = True
cfg['web']['host'] = '127.0.0.1'
cfg['web']['port'] = $SERVER_PORT
cfg.setdefault('server', {})
cfg['server']['host'] = '127.0.0.1'
cfg['server']['port'] = $SERVER_PORT
with open('$SERVER_HOME/config.json', 'w') as f:
    json.dump(cfg, f, indent=2)
" 2>/dev/null || true
  fi

  XBOT_HOME="$SERVER_HOME" "$XBOT_DEBUG_BIN" serve --config "$SERVER_HOME/config.json" \
    > "$XBOT_DEBUG_HOME/server.log" 2>&1 &
  SERVER_PID=$!

  # Wait for server to listen
  for i in $(seq 1 20); do
    if ss -tlnp 2>/dev/null | grep -q ":${SERVER_PORT} " || \
       netstat -tlnp 2>/dev/null | grep -q ":${SERVER_PORT} "; then
      echo "  Server PID: $SERVER_PID (port $SERVER_PORT)"
      SERVER_URL="ws://127.0.0.1:$SERVER_PORT"
      TOKEN=$(python3 -c "
import json
print(json.load(open('$SERVER_HOME/config.json')).get('admin',{}).get('token',''))
" 2>/dev/null || true)
      break
    fi
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
      echo "  ERROR: Server exited early." >&2
      cat "$XBOT_DEBUG_HOME/server.log" >&2
      exit 1
    fi
    sleep 0.5
  done

  if [ -z "$SERVER_URL" ]; then
    echo "  ERROR: Server did not start within 10s." >&2
    kill "$SERVER_PID" 2>/dev/null || true
    exit 1
  fi
else
  echo "[3/5] Skipping server (not e2e mode)"
fi

# --- Strip cli section for local mode ---
if [ "$MODE" = "local" ]; then
  python3 -c "
import json
with open('$XBOT_DEBUG_HOME/config.json') as f:
    c = json.load(f)
c.pop('cli', None)
with open('$XBOT_DEBUG_HOME/config.json', 'w') as f:
    json.dump(c, f, indent=2)
" 2>/dev/null || true
fi

# --- Build client args ---
echo "[4/5] Starting debug client..."
ARGS="--debug --new"
if [ "$MODE" = "remote" ] || [ "$MODE" = "e2e" ]; then
  ARGS="$ARGS --server $SERVER_URL"
  [ -n "$TOKEN" ] && ARGS="$ARGS --token $TOKEN"
fi
[ -n "$CAPTURE_MS" ] && ARGS="$ARGS --debug-capture-ms $CAPTURE_MS"
[ -n "$INPUT_SEQ" ] && ARGS="$ARGS --debug-input $INPUT_SEQ"
echo "  Args: $ARGS"

# Use 'script' to provide a pseudo-terminal so Bubble Tea raw mode works.
export XBOT_HOME="$XBOT_DEBUG_HOME"
SCRIPT_LOG="$XBOT_DEBUG_HOME/logs/script.log"
script -q -c "'$XBOT_DEBUG_BIN' $ARGS" "$SCRIPT_LOG" \
  2>"$XBOT_DEBUG_HOME/logs/xbot.log" &
SCRIPT_PID=$!
echo "  Client PID: $SCRIPT_PID"

# Wait for debug socket to appear
echo "[5/5] Waiting for debug socket..."
SOCK="$XBOT_DEBUG_HOME/debug/ctl.sock"
for i in $(seq 1 30); do
  if [ -S "$SOCK" ]; then
    echo "  Socket: $SOCK"
    echo ""
    echo "=== Ready ==="
    echo "  UI capture:  $XBOT_DEBUG_HOME/debug/ui_capture.log"
    echo "  Client logs: $XBOT_DEBUG_HOME/logs/xbot.log"
    [ -n "$SERVER_PID" ] && echo "  Server logs: $XBOT_DEBUG_HOME/server.log"
    echo "  Send key:    echo 'ctrl+c' | socat - UNIX-CONNECT:'$SOCK'"
    echo "  Type text:   bash '$SCRIPT_DIR/send-keys.sh' 'hello' 'enter'"
    echo "  Wait UI:     bash '$SCRIPT_DIR/wait-ui.sh' 'ready' 15"
    echo "  Stop:        bash '$SCRIPT_DIR/stop.sh'"
    return 0 2>/dev/null || exit 0
  fi
  sleep 0.5
  if ! kill -0 "$SCRIPT_PID" 2>/dev/null; then
    echo "ERROR: Client exited early." >&2
    cat "$XBOT_DEBUG_HOME/logs/xbot.log" >&2
    [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
    return 1 2>/dev/null || exit 1
  fi
done

echo "ERROR: Socket did not appear within 15s" >&2
kill "$SCRIPT_PID" 2>/dev/null || true
[ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
return 1 2>/dev/null || exit 1
