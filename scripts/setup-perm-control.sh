#!/bin/bash
set -euo pipefail

# xbot permission-control setup script
# Configures NOPASSWD sudoers + xbot settings for default_user / privileged_user
#
# Usage: sudo ./setup-perm-control.sh [XBOT_WORK_DIR]
#   XBOT_WORK_DIR: xbot working directory (default: detect from cwd or ~/src/xbot)
#
# After running, xbot's LLM can:
#   - Run shell commands as "user" without approval (default_user)
#   - Request to run as "root" with user approval (privileged_user)

DEFAULT_USER="${DEFAULT_USER:-user}"
PRIVILEGED_USER="${PRIVILEGED_USER:-root}"

# --- Detect xbot process user (the user that runs xbot, NOT the target users) ---
if [[ $EUID -eq 0 ]]; then
    # Running as root — find the real xbot user (first non-root UID 1000+ user)
    XBOT_PROCESS_USER=$(awk -F: '$3 >= 1000 && $3 != 65534 && $1 != "root" {print $1; exit}' /etc/passwd)
    if [[ -z "$XBOT_PROCESS_USER" ]]; then
        echo "ERROR: Cannot determine xbot process user (running as root, no regular user found)"
        exit 1
    fi
    echo "Running as root, xbot process user detected: $XBOT_PROCESS_USER"
else
    XBOT_PROCESS_USER="$(whoami)"
    echo "xbot process user: $XBOT_PROCESS_USER"
fi

echo "Default user (no approval needed): $DEFAULT_USER"
echo "Privileged user (approval required): $PRIVILEGED_USER"
echo ""

# --- 1. Configure sudoers ---
SUDOERS_FILE="/etc/sudoers.d/xbot-perm-control"
SUDOERS_ENTRY="$XBOT_PROCESS_USER ALL=($DEFAULT_USER,$PRIVILEGED_USER) NOPASSWD: ALL"

echo "=== Step 1: Configure sudoers ==="
echo "Will write: $SUDOERS_ENTRY"
echo "Target: $SUDOERS_FILE"

if [[ $EUID -ne 0 ]]; then
    echo "Need root to write sudoers. Re-executing with sudo..."
    # Pass all args through, re-exec as root
    exec sudo bash "$0" "$@"
fi

# Validate sudoers syntax before writing
echo "$SUDOERS_ENTRY" | visudo -cf - > /dev/null 2>&1 || {
    echo "ERROR: sudoers entry failed validation"
    exit 1
}

cat > "$SUDOERS_FILE" <<EOF
# xbot permission control — allow xbot process to switch to default/privileged users
# Installed by setup-perm-control.sh on $(date -Iseconds)
$SUDOERS_ENTRY
EOF

chmod 440 "$SUDOERS_FILE"
echo "✓ sudoers configured: $SUDOERS_FILE"

# Verify
if sudo -n -u "$DEFAULT_USER" whoami > /dev/null 2>&1; then
    echo "✓ sudo -n -u $DEFAULT_USER: OK ($(sudo -n -u "$DEFAULT_USER" whoami))"
else
    echo "✗ sudo -n -u $DEFAULT_USER: FAILED (user may not exist)"
fi

if sudo -n -u "$PRIVILEGED_USER" whoami > /dev/null 2>&1; then
    echo "✓ sudo -n -u $PRIVILEGED_USER: OK ($(sudo -n -u "$PRIVILEGED_USER" whoami))"
else
    echo "✗ sudo -n -u $PRIVILEGED_USER: FAILED"
fi

echo ""

# --- 2. Configure xbot settings in SQLite ---
echo "=== Step 2: Configure xbot settings ==="

# Detect xbot DB path
# Priority: ~/.xbot/xbot.db (CLI default) > workdir/.xbot/xbot.db
REAL_HOME=$(eval echo "~$XBOT_PROCESS_USER")
DB_PATH=""

# Check ~/.xbot/xbot.db first (standard CLI location)
if [[ -f "$REAL_HOME/.xbot/xbot.db" ]]; then
    DB_PATH="$REAL_HOME/.xbot/xbot.db"
else
    # Fallback: try workdir argument or common locations
    WORK_DIR="${1:-}"
    if [[ -z "$WORK_DIR" ]]; then
        for candidate in "$PWD" "$REAL_HOME/src/xbot" "$REAL_HOME/xbot"; do
            if [[ -f "$candidate/.xbot/xbot.db" ]]; then
                DB_PATH="$candidate/.xbot/xbot.db"
                break
            fi
        done
    elif [[ -f "$WORK_DIR/.xbot/xbot.db" ]]; then
        DB_PATH="$WORK_DIR/.xbot/xbot.db"
    fi
fi

if [[ -z "$DB_PATH" ]]; then
    echo "ERROR: Cannot find xbot.db. Searched:"
    echo "  $REAL_HOME/.xbot/xbot.db"
    echo "  \$PWD/.xbot/xbot.db"
    echo ""
    echo "Pass xbot work directory as argument: sudo $0 /path/to/workdir"
    echo "Or set it up manually in xbot TUI: /settings → default_user, privileged_user"
    exit 1
fi
echo "Database: $DB_PATH"

NOW=$(date +%s)

# Insert or update settings (channel=cli, sender_id=cli_user)
sqlite3 "$DB_PATH" <<EOF
INSERT INTO user_settings (channel, sender_id, key, value, updated_at)
    VALUES ('cli', 'cli_user', 'default_user', '$DEFAULT_USER', $NOW)
    ON CONFLICT(channel, sender_id, key)
    DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;

INSERT INTO user_settings (channel, sender_id, key, value, updated_at)
    VALUES ('cli', 'cli_user', 'privileged_user', '$PRIVILEGED_USER', $NOW)
    ON CONFLICT(channel, sender_id, key)
    DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;
EOF

echo "✓ Settings written:"
echo "  default_user    = $DEFAULT_USER"
echo "  privileged_user = $PRIVILEGED_USER"
echo ""

# Verify
echo "=== Verification ==="
sqlite3 -header -column "$DB_PATH" \
    "SELECT channel, sender_id, key, value FROM user_settings WHERE key IN ('default_user', 'privileged_user') AND channel = 'cli';"

echo ""
echo "=== Done! ==="
echo "Restart xbot to pick up the new settings."
