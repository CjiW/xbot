#!/usr/bin/env bash
# send-keys.sh — Send a string of characters and optional trailing key via debug socket.
# Usage: bash send-keys.sh "hello world" "enter"
#        bash send-keys.sh "a"       # just type 'a'
#        bash send-keys.sh "" "ctrl+c"  # just send ctrl+c
set -euo pipefail

XBOT_DEBUG_HOME="${XBOT_DEBUG_HOME:-/tmp/xbot-debug-$(id -u)}"
SOCK="$XBOT_DEBUG_HOME/debug/ctl.sock"

TEXT="${1:-}"
TRAILING_KEY="${2:-}"

if [ ! -S "$SOCK" ]; then
  echo "ERROR: Debug socket not found: $SOCK" >&2
  echo "Start debug instance first." >&2
  exit 1
fi

# Send each character of TEXT individually
if [ -n "$TEXT" ]; then
  while IFS= read -r -n1 -d '' char; do
    printf '%s\n' "$char" | socat - UNIX-CONNECT:"$SOCK" 2>/dev/null || true
    sleep 0.02  # small delay to avoid flooding
  done < <(printf '%s' "$TEXT")
fi

# Send trailing key (e.g., "enter", "ctrl+c", "esc")
if [ -n "$TRAILING_KEY" ]; then
  printf '%s\n' "$TRAILING_KEY" | socat - UNIX-CONNECT:"$SOCK" 2>/dev/null || true
fi
