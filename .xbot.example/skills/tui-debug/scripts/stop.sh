#!/usr/bin/env bash
# stop.sh — Stop debug instances and optionally cleanup.
# Usage: bash stop.sh [clean]
set -euo pipefail

XBOT_DEBUG_HOME="${XBOT_DEBUG_HOME:-/tmp/xbot-debug-$(id -u)}"

# Kill debug client (match exact binary path to avoid killing real xbot)
CLIENT_BIN="$XBOT_DEBUG_HOME/xbot-cli"
if [ -f "$CLIENT_BIN" ]; then
  pgrep -f "$CLIENT_BIN" >/dev/null 2>&1 && pkill -f "$CLIENT_BIN" 2>/dev/null || true
fi

# Kill debug server (e2e mode)
SERVER_BIN="$XBOT_DEBUG_HOME/server-home/xbot-cli"
if [ -f "$SERVER_BIN" ]; then
  pgrep -f "$SERVER_BIN" >/dev/null 2>&1 && pkill -f "$SERVER_BIN" 2>/dev/null || true
fi

# Also kill any process with XBOT_HOME pointing to our debug dir
# (catches processes started via 'setsid script ...')
pgrep -f "XBOT_HOME=$XBOT_DEBUG_HOME" >/dev/null 2>&1 && \
  pkill -f "XBOT_HOME=$XBOT_DEBUG_HOME" 2>/dev/null || true

sleep 0.3

if [ "${1:-}" = "clean" ]; then
  echo "Cleaning up $XBOT_DEBUG_HOME..."
  rm -rf "$XBOT_DEBUG_HOME"
  echo "Done."
else
  echo "Debug instance stopped."
  echo "Logs preserved at: $XBOT_DEBUG_HOME"
  echo "Run 'bash stop.sh clean' to remove everything."
fi
