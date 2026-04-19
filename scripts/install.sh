#!/usr/bin/env bash
set -euo pipefail

REPO="CjiW/xbot"
BINARY="xbot-cli"
INSTALL_PATH="${INSTALL_PATH:-/usr/local/bin}"
XBOT_HOME="${XBOT_HOME:-$HOME/.xbot}"
CONFIG_PATH="${CONFIG_PATH:-$XBOT_HOME/config.json}"
SERVICE_NAME="xbot-server"
DEFAULT_PORT="${PORT:-8080}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

require_cmd() {
    command -v "$1" >/dev/null 2>&1 || error "Missing required command: $1"
}

detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"
    case "$os" in
        linux) ;;
        darwin) ;;
        *) error "Unsupported OS: $os. Use the PowerShell installer on Windows." ;;
    esac
    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) error "Unsupported architecture: $arch. Only amd64 and arm64 are supported." ;;
    esac
    echo "${os}-${arch}"
}

resolve_version() {
    if [ -n "${VERSION:-}" ]; then
        echo "$VERSION"
        return
    fi
    local tag
    tag=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    [ -n "$tag" ] || error "Failed to determine latest version. Set VERSION env var explicitly."
    echo "$tag"
}

ask_mode() {
    echo "Choose install mode:" >&2
    echo "  1) standalone      - CLI runs locally in-process" >&2
    echo "  2) server-client   - install local server service, CLI connects remotely" >&2
    printf "Select [1/2] (default 1): " >&2
    read -r mode
    case "${mode:-1}" in
        1) echo "standalone" ;;
        2) echo "server-client" ;;
        *) error "Invalid selection: ${mode}" ;;
    esac
}

random_token() {
    if command -v python3 >/dev/null 2>&1; then
        python3 - <<'PY'
import secrets
print(secrets.token_hex(16))
PY
        return
    fi
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -hex 16
        return
    fi
    error "Need python3 or openssl to generate random token"
}

backup_config() {
    if [ -f "$CONFIG_PATH" ]; then
        mkdir -p "$XBOT_HOME"
        local ts backup
        ts="$(date +%Y%m%d-%H%M%S)"
        backup="${CONFIG_PATH}.bak.${ts}"
        cp "$CONFIG_PATH" "$backup"
        info "Backed up existing config to ${backup}"
    fi
}

write_config() {
    local mode="$1" port="$2" token="$3"
    mkdir -p "$XBOT_HOME"
    require_cmd python3
    python3 - "$CONFIG_PATH" "$mode" "$port" "$token" <<'PY'
import json, os, sys
path, mode, port, token = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
if os.path.exists(path):
    with open(path, 'r', encoding='utf-8') as f:
        try:
            cfg = json.load(f)
        except Exception:
            cfg = {}
else:
    cfg = {}

cfg.setdefault('server', {})
cfg.setdefault('web', {})
cfg.setdefault('cli', {})
cfg.setdefault('admin', {})
changes = []
preserved = []

def set_if_missing(section, key, value):
    if key not in cfg[section] or cfg[section][key] in (None, ''):
        cfg[section][key] = value
        changes.append(f'{section}.{key}={value}')
    else:
        preserved.append(f'{section}.{key}={cfg[section][key]}')

def set_always(section, key, value):
    old = cfg[section].get(key)
    cfg[section][key] = value
    if old != value:
        changes.append(f'{section}.{key}={value} (was {old})')
    else:
        preserved.append(f'{section}.{key}={old}')

set_if_missing('admin', 'token', token)
if mode == 'server-client':
    set_if_missing('server', 'host', '127.0.0.1')
    set_always('server', 'port', port)
    set_always('web', 'enable', True)
    set_if_missing('web', 'host', '127.0.0.1')
    set_always('web', 'port', port)
    set_always('cli', 'server_url', f'ws://127.0.0.1:{port}')
    set_always('cli', 'token', cfg['admin'].get('token') or token)
else:
    set_if_missing('cli', 'token', cfg['admin'].get('token') or token)

with open(path, 'w', encoding='utf-8') as f:
    json.dump(cfg, f, ensure_ascii=False, indent=2)

print('CONFIG_CHANGES_BEGIN')
for item in changes:
    print(item)
print('CONFIG_CHANGES_END')
print('CONFIG_PRESERVED_BEGIN')
for item in preserved:
    print(item)
print('CONFIG_PRESERVED_END')
PY
}

write_systemd_unit() {
    local bin_path="$1" config_path="$2" unit_file="$3"
    local install_user
    install_user="$(id -un)"
    local xbot_home
    xbot_home="$(cd "$XBOT_HOME" && pwd)"
    local work_dir
    work_dir="$(pwd)"
    cat > "$unit_file" <<EOF_UNIT
[Unit]
Description=xbot Agent Server
After=network.target

[Service]
Type=simple
User=${install_user}
Environment=XBOT_HOME=${xbot_home}
WorkingDirectory=${work_dir}
ExecStart=${bin_path} serve --config ${config_path}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF_UNIT
}

install_systemd() {
    local bin_path="$1" config_path="$2"
    [ "$(uname -s)" = "Linux" ] || return 0
    info "Installing/updating systemd service (requires passwordless sudo -n)..."
    write_systemd_unit "$bin_path" "$config_path" "$XBOT_HOME/${SERVICE_NAME}.service"
    sudo -n install -d /etc/systemd/system
    sudo -n install -m 0644 "$XBOT_HOME/${SERVICE_NAME}.service" "/etc/systemd/system/${SERVICE_NAME}.service"
    sudo -n systemctl daemon-reload
    sudo -n systemctl enable "$SERVICE_NAME"
    sudo -n systemctl restart "$SERVICE_NAME"
    info "systemd service installed/updated and restarted: ${SERVICE_NAME}"
}

install_launchd() {
    local bin_path="$1" config_path="$2"
    [ "$(uname -s)" = "Darwin" ] || return 0
    local plist="$HOME/Library/LaunchAgents/com.xbot.server.plist"
    mkdir -p "$HOME/Library/LaunchAgents" "$XBOT_HOME/logs"
    cat > "$plist" <<EOF_PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.xbot.server</string>
  <key>ProgramArguments</key>
  <array>
    <string>${bin_path}</string>
    <string>serve</string>
    <string>--config</string>
    <string>${config_path}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>EnvironmentVariables</key><dict>
    <key>XBOT_HOME</key><string>${XBOT_HOME}</string>
  </dict>
  <key>StandardOutPath</key><string>${XBOT_HOME}/logs/xbot-server.log</string>
  <key>StandardErrorPath</key><string>${XBOT_HOME}/logs/xbot-server.err</string>
</dict></plist>
EOF_PLIST
    launchctl unload -w "$plist" >/dev/null 2>&1 || true
    launchctl load -w "$plist"
    info "launchd service installed/updated and restarted: com.xbot.server"
}

main() {
    echo ""
    echo "  ╔══════════════════════════════════════╗"
    echo "  ║         xbot-cli Installer           ║"
    echo "  ╚══════════════════════════════════════╝"
    echo ""

    require_cmd curl
    PLATFORM=$(detect_platform)
    VERSION=$(resolve_version)
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/xbot-cli-${PLATFORM}"

    info "Platform:  ${PLATFORM}"
    info "Version:   ${VERSION}"
    info "URL:       ${DOWNLOAD_URL}"
    info "Install:   ${INSTALL_PATH}/${BINARY}"
    info "Config:    ${CONFIG_PATH}"
    echo ""

    MODE=$(ask_mode)
    TOKEN=$(random_token)
    PORT="$DEFAULT_PORT"
    if [ "$MODE" = "server-client" ]; then
        printf "WebSocket port for local server [${DEFAULT_PORT}]: "
        read -r input_port
        PORT="${input_port:-$DEFAULT_PORT}"
    fi

    info "Downloading..."
    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT
    if ! curl -fsSL --progress-bar -o "${TMPDIR}/${BINARY}" "$DOWNLOAD_URL"; then
        error "Download failed. Check the version and platform."
    fi

    if command -v shasum >/dev/null 2>&1; then
        info "Verifying checksum..."
        curl -fsSL "https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt" -o "${TMPDIR}/checksums.txt" 2>/dev/null || warn "Checksum file not found, skipping verification."
        if [ -f "${TMPDIR}/checksums.txt" ]; then
            expected=$(grep "xbot-cli-${PLATFORM}" "${TMPDIR}/checksums.txt" | awk '{print $1}')
            actual=$(shasum -a 256 "${TMPDIR}/${BINARY}" | awk '{print $1}')
            if [ -n "$expected" ] && [ "$expected" != "$actual" ]; then
                error "Checksum mismatch! Expected: ${expected}, Got: ${actual}"
            fi
            info "Checksum verified ✓"
        fi
    fi

    chmod +x "${TMPDIR}/${BINARY}"
    if [ ! -d "$INSTALL_PATH" ]; then
        if mkdir -p "$INSTALL_PATH" 2>/dev/null; then
            :
        else
            warn "Cannot create ${INSTALL_PATH} directly, using sudo..."
            sudo mkdir -p "$INSTALL_PATH"
        fi
    fi
    if [ -w "$INSTALL_PATH" ]; then
        mv "${TMPDIR}/${BINARY}" "${INSTALL_PATH}/${BINARY}"
    else
        warn "No write permission to ${INSTALL_PATH}, using sudo..."
        sudo mv "${TMPDIR}/${BINARY}" "${INSTALL_PATH}/${BINARY}"
    fi

    backup_config
    CONFIG_UPDATE_OUTPUT="$(write_config "$MODE" "$PORT" "$TOKEN")"
    echo "$CONFIG_UPDATE_OUTPUT" | sed -n '/^CONFIG_CHANGES_BEGIN$/,/^CONFIG_CHANGES_END$/p' | sed '1d;$d' | while IFS= read -r line; do
        [ -n "$line" ] && info "Config set: $line"
    done
    echo "$CONFIG_UPDATE_OUTPUT" | sed -n '/^CONFIG_PRESERVED_BEGIN$/,/^CONFIG_PRESERVED_END$/p' | sed '1d;$d' | while IFS= read -r line; do
        [ -n "$line" ] && warn "Config preserved: $line"
    done

    if [ "$MODE" = "server-client" ]; then
        case "$(uname -s)" in
            Linux) install_systemd "${INSTALL_PATH}/${BINARY}" "$CONFIG_PATH" ;;
            Darwin) install_launchd "${INSTALL_PATH}/${BINARY}" "$CONFIG_PATH" ;;
        esac
    fi

    echo ""
    info "✅ xbot-cli ${VERSION} installed to ${INSTALL_PATH}/${BINARY}"
    info "Mode: ${MODE}"
    info "Config: ${CONFIG_PATH}"
    if [ "$MODE" = "server-client" ]; then
        info "CLI will connect to the configured local server (see ${CONFIG_PATH})"
        info "Use '${BINARY}' for client, '${BINARY} serve --config ${CONFIG_PATH}' for manual server start"
    else
        info "Run '${BINARY}' to start."
    fi
    echo ""
}

main "$@"
