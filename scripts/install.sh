#!/usr/bin/env bash
set -euo pipefail

REPO="CjiW/xbot"
BINARY="xbot-cli"
INSTALL_PATH="${INSTALL_PATH:-/usr/local/bin}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

# --- Detect platform ---
detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"

    case "$os" in
        linux)  ;;
        darwin) ;;
        *)      error "Unsupported OS: $os. xbot-cli only supports Linux and macOS." ;;
    esac

    case "$arch" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) error "Unsupported architecture: $arch. Only amd64 and arm64 are supported." ;;
    esac

    echo "${os}-${arch}"
}

# --- Resolve version ---
resolve_version() {
    if [ -n "${VERSION:-}" ]; then
        echo "$VERSION"
        return
    fi

    # Try to get latest release tag from GitHub API
    local tag
    tag=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    if [ -z "$tag" ]; then
        error "Failed to determine latest version. Set VERSION env var explicitly, e.g.: VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh | bash"
    fi
    echo "$tag"
}

# --- Main ---
main() {
    echo ""
    echo "  ╔══════════════════════════════════════╗"
    echo "  ║         xbot-cli Installer           ║"
    echo "  ╚══════════════════════════════════════╝"
    echo ""

    PLATFORM=$(detect_platform)
    VERSION=$(resolve_version)
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/xbot-cli-${PLATFORM}"

    info "Platform:  ${PLATFORM}"
    info "Version:   ${VERSION}"
    info "URL:       ${DOWNLOAD_URL}"
    info "Install:   ${INSTALL_PATH}/${BINARY}"
    echo ""

    # Download
    info "Downloading..."
    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT

    if ! curl -fsSL --progress-bar -o "${TMPDIR}/${BINARY}" "$DOWNLOAD_URL"; then
        error "Download failed. Check the version and platform."
    fi

    # Verify checksum if shasum available
    if command -v shasum &>/dev/null; then
        info "Verifying checksum..."
        curl -fsSL "https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt" -o "${TMPDIR}/checksums.txt" 2>/dev/null || warn "Checksum file not found, skipping verification."
        if [ -f "${TMPDIR}/checksums.txt" ]; then
            expected=$(grep "xbot-cli-${PLATFORM}" "${TMPDIR}/checksums.txt" | awk '{print $1}')
            actual=$(shasum -a 256 "${TMPDIR}/${BINARY}" | awk '{print $1}')
            if [ "$expected" != "$actual" ]; then
                error "Checksum mismatch! Expected: ${expected}, Got: ${actual}"
            fi
            info "Checksum verified ✓"
        fi
    fi

    # Install
    chmod +x "${TMPDIR}/${BINARY}"
    if [ -w "$INSTALL_PATH" ]; then
        mv "${TMPDIR}/${BINARY}" "${INSTALL_PATH}/${BINARY}"
    else
        warn "No write permission to ${INSTALL_PATH}, using sudo..."
        sudo mv "${TMPDIR}/${BINARY}" "${INSTALL_PATH}/${BINARY}"
    fi

    echo ""
    info "✅ xbot-cli ${VERSION} installed to ${INSTALL_PATH}/${BINARY}"
    echo ""
    info "Run 'xbot-cli' to start."
    echo ""
    echo "  Project:  https://github.com/${REPO}"
    echo "  License:  MIT"
    echo ""
}

main "$@"
