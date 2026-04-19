#!/usr/bin/env bash
# wait-ui.sh — Wait until UI capture contains a pattern (with timeout).
# Strips ANSI escape codes before matching so patterns work reliably.
#
# Usage: bash wait-ui.sh "ready" [timeout_seconds]
set -euo pipefail

XBOT_DEBUG_HOME="${XBOT_DEBUG_HOME:-/tmp/xbot-debug-$(id -u)}"
UI_LOG="$XBOT_DEBUG_HOME/debug/ui_capture.log"
PATTERN="${1:-}"
TIMEOUT="${2:-30}"

if [ -z "$PATTERN" ]; then
  echo "Usage: bash wait-ui.sh PATTERN [TIMEOUT_SECONDS]" >&2
  exit 1
fi

if [ ! -f "$UI_LOG" ]; then
  echo "ERROR: UI capture log not found: $UI_LOG" >&2
  echo "Start debug instance with --debug flag first." >&2
  exit 1
fi

# Strip ANSI escape codes for matching
strip_ansi() { sed 's/\x1b\[[0-9;]*m//g'; }

elapsed=0
interval=1
while [ "$elapsed" -lt "$TIMEOUT" ]; do
  if strip_ansi < "$UI_LOG" | grep -q "$PATTERN" 2>/dev/null; then
    echo "MATCHED after ${elapsed}s: '$PATTERN'"
    exit 0
  fi
  sleep "$interval"
  elapsed=$((elapsed + interval))
done

echo "TIMEOUT after ${TIMEOUT}s waiting for: '$PATTERN'" >&2
echo "Last 20 lines of UI capture (stripped):" >&2
strip_ansi < "$UI_LOG" | tail -20 >&2
exit 1
