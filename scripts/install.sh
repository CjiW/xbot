#!/usr/bin/env bash
set -euo pipefail

REPO="CjiW/xbot"
BINARY="xbot-cli"
# Default to user-local install (no sudo required)
INSTALL_PATH="${INSTALL_PATH:-$HOME/.local/bin}"
XBOT_HOME="${XBOT_HOME:-$HOME/.xbot}"
CONFIG_PATH="${CONFIG_PATH:-$XBOT_HOME/config.json}"
SERVICE_NAME="xbot-server"
DEFAULT_PORT="${PORT:-8082}"

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

# Non-interactive: use MODE env var. Interactive: prompt user.
ask_mode() {
    if [ -n "${MODE:-}" ]; then
        case "$MODE" in
            standalone|server-client) echo "$MODE" ;;
            *) error "Invalid MODE='${MODE}'. Use 'standalone' or 'server-client'." ;;
        esac
        return
    fi
    # When piped (curl | bash), stdin is not a terminal.
    # Default to standalone in that case.
    if [ ! -t 0 ]; then
        info "Non-interactive mode (stdin is pipe). Defaulting to standalone."
        info "Use MODE=server-client or run interactively to change."
        echo "standalone"
        return
    fi
    echo ""
    echo "Choose install mode:"
    echo "  1) standalone      - CLI runs locally in-process"
    echo "  2) server-client   - install local server service, CLI connects remotely"
    printf "Select [1/2] (default 1): "
    read -r mode </dev/tty
    case "${mode:-1}" in
        1) echo "standalone" ;;
        2) echo "server-client" ;;
        *) error "Invalid selection: ${mode}" ;;
    esac
}

# Generate random hex token without external dependencies
random_token() {
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -hex 16
        return
    fi
    # Fallback: read from /dev/urandom (available on all Linux/macOS)
    if [ -r /dev/urandom ]; then
        od -A n -t x1 -N 16 /dev/urandom | tr -d ' \n'
        return
    fi
    # Last resort: python3
    if command -v python3 >/dev/null 2>&1; then
        python3 -c 'import secrets; print(secrets.token_hex(16))'
        return
    fi
    error "Cannot generate random token: need openssl, /dev/urandom, or python3"
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

# Write config.json using jq (preferred) or python3 fallback.
write_config() {
    local mode="$1" port="$2" token="$3"
    mkdir -p "$XBOT_HOME"

    if command -v jq >/dev/null 2>&1; then
        write_config_jq "$mode" "$port" "$token"
    elif command -v python3 >/dev/null 2>&1; then
        write_config_python3 "$mode" "$port" "$token"
    else
        error "Need jq or python3 to write config.json"
    fi
}

write_config_jq() {
    local mode="$1" port="$2" token="$3"
    local changes=() preserved=()

    # Create config if missing or invalid
    if [ ! -f "$CONFIG_PATH" ] || ! jq empty "$CONFIG_PATH" 2>/dev/null; then
        echo '{}' > "$CONFIG_PATH"
    fi

    # Ensure top-level sections exist
    jq '{server: .server // {}, web: .web // {}, cli: .cli // {}, admin: .admin // {}, agent: .agent // {}} * .' \
        "$CONFIG_PATH" > "${CONFIG_PATH}.tmp" && mv "${CONFIG_PATH}.tmp" "$CONFIG_PATH"

    _set_if_missing() {
        local section="$1" key="$2" value="$3"
        local current
        current=$(jq -r ".${section}.${key} // empty" "$CONFIG_PATH" 2>/dev/null)
        if [ -z "$current" ]; then
            jq --arg v "$value" ".${section}.${key} = \$v" "$CONFIG_PATH" > "${CONFIG_PATH}.tmp" && mv "${CONFIG_PATH}.tmp" "$CONFIG_PATH"
            changes+=("${section}.${key}=${value}")
        else
            preserved+=("${section}.${key}=${current}")
        fi
    }

    _set_always() {
        local section="$1" key="$2" value="$3"
        local old
        old=$(jq -r ".${section}.${key} // empty" "$CONFIG_PATH" 2>/dev/null)
        jq --arg v "$value" ".${section}.${key} = \$v" "$CONFIG_PATH" > "${CONFIG_PATH}.tmp" && mv "${CONFIG_PATH}.tmp" "$CONFIG_PATH"
        if [ "$old" != "$value" ]; then
            changes+=("${section}.${key}=${value} (was ${old})")
        else
            preserved+=("${section}.${key}=${old}")
        fi
    }

    _set_if_missing admin token "$token"
    _set_if_missing agent work_dir "$HOME"
    _set_if_missing llm provider "openai"
    _set_if_missing llm model "gpt-4o-mini"
    _set_if_missing llm api_key ""
    _set_if_missing llm base_url ""

    if [ "$mode" = "server-client" ]; then
        _set_if_missing server host "127.0.0.1"
        _set_always server port "$port"
        _set_always web enable true
        _set_if_missing web host "127.0.0.1"
        _set_always web port "$port"
        _set_always cli server_url "ws://127.0.0.1:${port}"
        local admin_token
        admin_token=$(jq -r '.admin.token // empty' "$CONFIG_PATH")
        _set_always cli token "${admin_token:-$token}"
    else
        local admin_token
        admin_token=$(jq -r '.admin.token // empty' "$CONFIG_PATH")
        _set_if_missing cli token "${admin_token:-$token}"
    fi

    for item in "${changes[@]+"${changes[@]}"}"; do
        [ -n "$item" ] && info "Config set: $item"
    done
    for item in "${preserved[@]+"${preserved[@]}"}"; do
        [ -n "$item" ] && warn "Config preserved: $item"
    done
}

write_config_python3() {
    local mode="$1" port="$2" token="$3"
    python3 - "$CONFIG_PATH" "$mode" "$port" "$token" "$HOME" <<'PY'
import json, os, sys
path, mode, port, token, home = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4], sys.argv[5]
if os.path.exists(path):
    with open(path, 'r', encoding='utf-8') as f:
        try: cfg = json.load(f)
        except Exception: cfg = {}
else:
    cfg = {}
cfg.setdefault('server', {})
cfg.setdefault('web', {})
cfg.setdefault('cli', {})
cfg.setdefault('admin', {})
cfg.setdefault('agent', {})
cfg.setdefault('llm', {})
changes, preserved = [], []
def set_if_missing(s, k, v):
    if k not in cfg[s] or cfg[s][k] in (None, ''):
        cfg[s][k] = v; changes.append(f'{s}.{k}={v}')
    else:
        preserved.append(f'{s}.{k}={cfg[s][k]}')
def set_always(s, k, v):
    old = cfg[s].get(k); cfg[s][k] = v
    (changes if old != v else preserved).append(f'{s}.{k}={v}' + (f' (was {old})' if old != v else ''))
set_if_missing('admin', 'token', token)
set_if_missing('agent', 'work_dir', home)
set_if_missing('llm', 'provider', 'openai')
set_if_missing('llm', 'model', 'gpt-4o-mini')
set_if_missing('llm', 'api_key', '')
set_if_missing('llm', 'base_url', '')
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
for c in changes: print(f'[INFO] Config set: {c}')
for p in preserved: print(f'[WARN] Config preserved: {p}', file=sys.stderr)
PY
}

download_web_dist() {
    local version="$1" target_dir="$2"
    local dist_url="https://github.com/${REPO}/releases/download/${version}/xbot-web-dist.tar.gz"
    info "Downloading Web UI frontend..."
    mkdir -p "$target_dir"
    local curl_progress=""
    if [ -t 2 ]; then
        curl_progress="--progress-bar"
    fi
    if curl -fSL ${curl_progress} "$dist_url" | tar xzf - -C "$target_dir" 2>/dev/null; then
        info "Web UI installed to ${target_dir} ✓"
    else
        warn "Failed to download Web UI frontend. The server will run in API-only mode."
        warn "You can manually download it later from: ${dist_url}"
        warn "Extract to: ${target_dir}"
    fi
}

# --- User-level systemd service (no sudo required) ---
write_systemd_user_unit() {
    local bin_path="$1" config_path="$2" unit_file="$3"
    local xbot_home work_dir
    xbot_home="$(cd "$XBOT_HOME" && pwd)"
    work_dir="$HOME"
    mkdir -p "$HOME/.config/systemd/user"
    cat > "$unit_file" <<EOF_UNIT
[Unit]
Description=xbot Agent Server (user)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=XBOT_HOME=${xbot_home}
Environment=PATH=${INSTALL_PATH}:/usr/local/bin:/usr/bin:/bin
WorkingDirectory=${work_dir}
ExecStart=${bin_path} serve --config ${config_path}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF_UNIT
}

install_systemd_user() {
    local bin_path="$1" config_path="$2"
    [ "$(uname -s)" = "Linux" ] || return 0
    info "Installing systemd --user service (no sudo required)..."
    write_systemd_user_unit "$bin_path" "$config_path" "$HOME/.config/systemd/user/${SERVICE_NAME}.service"
    info "systemd --user unit written: ${SERVICE_NAME}.service"
    if [ -z "${NONINTERACTIVE:-}" ]; then
        systemctl --user daemon-reload
        systemctl --user enable "$SERVICE_NAME"
        systemctl --user restart "$SERVICE_NAME"
        info "systemd --user service started: ${SERVICE_NAME}"
        info "  Logs: journalctl --user -u ${SERVICE_NAME} -f"
    else
        info "NONINTERACTIVE: skipped daemon-reload/enable/start"
    fi
    info "  Stop: systemctl --user stop ${SERVICE_NAME}"
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
  <key>WorkingDirectory</key><string>${HOME}</string>
  <key>EnvironmentVariables</key><dict>
    <key>XBOT_HOME</key><string>${XBOT_HOME}</string>
  </dict>
  <key>StandardOutPath</key><string>${XBOT_HOME}/logs/xbot-server.log</string>
  <key>StandardErrorPath</key><string>${XBOT_HOME}/logs/xbot-server.err</string>
</dict></plist>
EOF_PLIST
    info "launchd plist written: ${plist}"
    if [ -z "${NONINTERACTIVE:-}" ]; then
        launchctl unload -w "$plist" >/dev/null 2>&1 || true
        launchctl load -w "$plist"
        info "launchd service loaded: com.xbot.server"
    else
        info "NONINTERACTIVE: skipped launchctl load"
    fi
    info "  Logs: ${XBOT_HOME}/logs/xbot-server.log"
    info "  Stop: launchctl unload -w ${plist}"
}

add_to_path() {
    case ":${PATH}:" in
        *":${INSTALL_PATH}:"*) return 0 ;;
    esac
    local profile=""
    if [ -n "${ZSH_VERSION:-}" ] || [ "$(basename "${SHELL:-}")" = "zsh" ]; then
        profile="$HOME/.zshrc"
    else
        profile="$HOME/.bashrc"
    fi
    if [ -f "$profile" ]; then
        if ! grep -qF "$INSTALL_PATH" "$profile" 2>/dev/null; then
            echo "" >> "$profile"
            echo "# Added by xbot installer" >> "$profile"
            echo "export PATH=\"${INSTALL_PATH}:\$PATH\"" >> "$profile"
            info "Added ${INSTALL_PATH} to PATH in ${profile}"
        fi
    fi
    export PATH="${INSTALL_PATH}:${PATH}"
}

# Ensure systemd --user lingering is enabled (so service runs at boot without login)
enable_linger() {
    [ "$(uname -s)" = "Linux" ] || return 0
    [ -z "${NONINTERACTIVE:-}" ] || return 0
    if command -v loginctl >/dev/null 2>&1; then
        if loginctl enable-linger "$(id -un)" 2>/dev/null; then
            info "Enabled lingering: server will start at boot"
        else
            warn "Could not enable linger (server starts on first login instead)"
            warn "  Run: loginctl enable-linger $(id -un)"
        fi
    fi
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
    if [ "$MODE" = "server-client" ] && [ -z "${NONINTERACTIVE:-}" ]; then
        printf "Server port (HTTP + WebSocket + Web UI) [${DEFAULT_PORT}]: " >&2
        read -r input_port </dev/tty
        PORT="${input_port:-$DEFAULT_PORT}"
    fi

    info "Downloading..."
    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT
    local curl_progress=""
    if [ -t 2 ]; then
        curl_progress="--progress-bar"
    fi
    if ! curl -fSL ${curl_progress} -o "${TMPDIR}/${BINARY}" "$DOWNLOAD_URL"; then
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

    # Stop running xbot-cli before overwriting the binary
    if [ -x "${INSTALL_PATH}/${BINARY}" ]; then
        info "Checking for running xbot-cli..."
        if systemctl --user status "$SERVICE_NAME" >/dev/null 2>&1; then
            systemctl --user stop "$SERVICE_NAME" 2>/dev/null || true
        fi
        # Kill any running instances
        pkill -f "${INSTALL_PATH}/${BINARY}" 2>/dev/null || true
        # Wait for process to fully exit
        for i in 1 2 3 4 5; do
            pgrep -f "${INSTALL_PATH}/${BINARY}" >/dev/null 2>&1 || break
            sleep 1
        done
    fi

    # Install binary to user-local directory (no sudo)
    chmod +x "${TMPDIR}/${BINARY}"
    mkdir -p "$INSTALL_PATH"
    mv "${TMPDIR}/${BINARY}" "${INSTALL_PATH}/${BINARY}"
    info "Binary installed to ${INSTALL_PATH}/${BINARY}"

    add_to_path

    backup_config
    write_config "$MODE" "$PORT" "$TOKEN"

    if [ "$MODE" = "server-client" ]; then
        download_web_dist "$VERSION" "$XBOT_HOME/web/dist"
        case "$(uname -s)" in
            Linux)
                install_systemd_user "${INSTALL_PATH}/${BINARY}" "$CONFIG_PATH"
                enable_linger
                ;;
            Darwin)
                install_launchd "${INSTALL_PATH}/${BINARY}" "$CONFIG_PATH"
                ;;
        esac
    fi

    echo ""
    info "✅ xbot-cli ${VERSION} installed to ${INSTALL_PATH}/${BINARY}"
    info "Mode: ${MODE}"
    info "Config: ${CONFIG_PATH}"
    if [ "$MODE" = "server-client" ]; then
        info "Web UI: http://localhost:${PORT}"
        info "CLI will connect to the configured local server (see ${CONFIG_PATH})"
        case "$(uname -s)" in
            Linux)
                info "Service: systemd --user (${SERVICE_NAME})"
                info "  Logs:  journalctl --user -u ${SERVICE_NAME} -f"
                info "  Stop:  systemctl --user stop ${SERVICE_NAME}"
                info "  Start: systemctl --user start ${SERVICE_NAME}"
                ;;
            Darwin)
                info "Service: launchd (com.xbot.server)"
                info "  Logs:  ${XBOT_HOME}/logs/xbot-server.log"
                info "  Stop:  launchctl unload -w ~/Library/LaunchAgents/com.xbot.server.plist"
                ;;
        esac
    else
        info "Run '${BINARY}' to start."
    fi
    if ! command -v "$BINARY" >/dev/null 2>&1; then
        echo ""
        warn "Note: ${INSTALL_PATH} is not yet in your shell PATH."
        warn "  Restart your shell or run: export PATH=\"${INSTALL_PATH}:\$PATH\""
    fi
    echo ""
}

main "$@"
