#!/usr/bin/env bash
#
# vocat install / update script for binary + systemd deployments.
#
# Usage:
#   sudo bash install.sh [version]        # install a specific version
#   sudo bash install.sh                  # install latest release
#   sudo bash install.sh --force          # reinstall even at the same version
#   curl -fsSL <raw url> | sudo bash      # one-liner (latest)
#
# Behavior:
#   - Prompts for script language (中文 / English) as soon as it runs.
#   - If the installed version equals the target version, does nothing (unless --force).
#   - On first install, generates a random 32-char admin password, writes it to
#     /etc/vocat/env (0600, loaded by the systemd unit), and prints it ONCE.
#   - On update, preserves the existing env file and credentials.
#   - (Re)writes the systemd unit and restarts the service.
#
# Published script: must contain no secrets, IPs, or passwords.

set -euo pipefail

# --- Publisher configuration -------------------------------------------------
# Default GitHub repository in owner/name form. Publishers: set this to your
# own repo, or override per-run with VOCAT_REPO.
REPO="${VOCAT_REPO:-MengMengCode/VoCat}"

INSTALL_DIR="/opt/vocat/bin"
BINARY_PATH="${INSTALL_DIR}/vocat"
LINK_PATH="/usr/local/bin/vocat"
ENV_DIR="/etc/vocat"
ENV_FILE="${ENV_DIR}/env"
UNIT_PATH="/etc/systemd/system/vocat.service"

# --- Language ----------------------------------------------------------------
LANG_CHOICE=""

msg() {
    # $1 = zh text, $2 = en text
    if [ "$LANG_CHOICE" = "en" ]; then
        printf '%s\n' "$2"
    else
        printf '%s\n' "$1"
    fi
}

prompt_language() {
    if ! ( : </dev/tty ) 2>/dev/null; then
        case "${VOCAT_LANG:-en}" in
            zh|zh-CN|cn) LANG_CHOICE="zh" ;;
            *) LANG_CHOICE="en" ;;
        esac
        return
    fi
    while true; do
        echo "选择语言 / Select language:  1) 中文   2) English" >/dev/tty
        printf '> ' >/dev/tty
        if ! read -r choice </dev/tty; then
            LANG_CHOICE="en"
            return
        fi
        case "$choice" in
            1|"") LANG_CHOICE="zh"; return ;;
            2) LANG_CHOICE="en"; return ;;
        esac
    done
}

die() {
    msg "$1" "$2" >&2
    exit 1
}

# --- Runtime dependencies ---------------------------------------------------
# VoCat invokes these host tools for QMI data sessions, DHCP, policy routing,
# and shared QMI access. qmi-proxy is intentionally installed outside PATH by
# both Debian/Ubuntu and Alpine, so check the known libexec locations too.
find_qmi_proxy() {
    if command -v qmi-proxy >/dev/null 2>&1; then
        command -v qmi-proxy
        return 0
    fi
    local candidate
    for candidate in \
        /usr/libexec/qmi-proxy \
        /usr/lib/libqmi-glib/qmi-proxy \
        /usr/lib64/libqmi-glib/qmi-proxy; do
        if [ -x "$candidate" ]; then
            printf '%s\n' "$candidate"
            return 0
        fi
    done
    return 1
}

runtime_dependencies_ready() {
    command -v ip >/dev/null 2>&1 &&
        command -v busybox >/dev/null 2>&1 &&
        busybox udhcpc --help >/dev/null 2>&1 &&
        command -v qmi-network >/dev/null 2>&1 &&
        command -v qmicli >/dev/null 2>&1 &&
        find_qmi_proxy >/dev/null 2>&1
}

missing_runtime_dependencies() {
    local missing=()
    command -v ip >/dev/null 2>&1 || missing+=(ip)
    if ! command -v busybox >/dev/null 2>&1 ||
        ! busybox udhcpc --help >/dev/null 2>&1; then
        missing+=("busybox udhcpc")
    fi
    command -v qmi-network >/dev/null 2>&1 || missing+=(qmi-network)
    command -v qmicli >/dev/null 2>&1 || missing+=(qmicli)
    find_qmi_proxy >/dev/null 2>&1 || missing+=(qmi-proxy)
    local IFS=', '
    printf '%s' "${missing[*]}"
}

ensure_runtime_dependencies() {
    runtime_dependencies_ready && return

    local missing
    missing=$(missing_runtime_dependencies)
    msg "缺少运行时依赖 ($missing)，正在安装。" "Missing runtime dependencies ($missing); installing them."

    if command -v apt-get >/dev/null 2>&1; then
        apt-get update
        DEBIAN_FRONTEND=noninteractive apt-get install -y iproute2 busybox libqmi-utils
    elif command -v apk >/dev/null 2>&1; then
        apk add --no-cache iproute2 busybox qmi-utils
    else
        die \
            "无法自动安装运行时依赖: $missing。请先安装 iproute2、带 udhcpc 的 busybox 及 libqmi 工具。" \
            "Cannot install runtime dependencies automatically: $missing. Install iproute2, BusyBox with udhcpc, and the libqmi utilities first."
    fi

    if ! runtime_dependencies_ready; then
        missing=$(missing_runtime_dependencies)
        die \
            "运行时依赖安装后仍不可用: $missing。" \
            "Runtime dependencies are still unavailable after installation: $missing."
    fi
}

# --- Root --------------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die "请以 root 身份运行此脚本。" "Run this script as root."

prompt_language

# --- Parse args --------------------------------------------------------------
FORCE=0
TARGET_VERSION=""
for arg in "$@"; do
    case "$arg" in
        --force) FORCE=1 ;;
        -h|--help)
            msg "用法: sudo bash install.sh [--force] [版本]" "Usage: sudo bash install.sh [--force] [version]"
            exit 0
            ;;
        *) TARGET_VERSION="${arg#v}" ;;
    esac
done

# --- Resolve target version --------------------------------------------------
resolve_target_version() {
    if [ -n "$TARGET_VERSION" ]; then
        TARGET_VERSION="${TARGET_VERSION#v}"
        return
    fi
    local api_url="https://api.github.com/repos/${REPO}/releases/latest"
    local auth_hdr=()
    if [ -n "${GITHUB_TOKEN:-}" ]; then
        auth_hdr=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
    fi
    local resp
    resp=$(curl -fsSL "${auth_hdr[@]}" "$api_url") || die "无法获取最新版本信息。检查网络或 REPO 设置。" "Failed to fetch latest release. Check network or REPO."
    # Parse "tag_name": "vX.Y.Z" without jq.
    local tag
    tag=$(printf '%s\n' "$resp" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')
    [ -n "$tag" ] || die "无法解析最新版本的 tag_name。" "Could not parse tag_name from the release response."
    TARGET_VERSION="${tag#v}"
}

# --- Skip if already installed at the same version ---------------------------
skip_if_equal() {
    [ -x "$BINARY_PATH" ] || return 0
    [ "$FORCE" -eq 1 ] && return 0
    local installed
    installed=$("$BINARY_PATH" version 2>/dev/null | awk '{print $2}' | sed -E 's/[[:space:]]*\(.*$//') || return 0
    [ -z "$installed" ] && return 0
    if [ "$installed" = "$TARGET_VERSION" ]; then
        install -d -m 0755 "$(dirname "$LINK_PATH")"
        ln -sfn "$BINARY_PATH" "$LINK_PATH"
        msg "已安装版本 $installed，与目标版本相同，跳过更新。" "Installed version $installed equals target; skipping."
        exit 0
    fi
    msg "当前 $installed -> $TARGET_VERSION，开始更新。" "Updating $installed -> $TARGET_VERSION."
}

# --- Detect architecture -----------------------------------------------------
detect_arch() {
    ARCH_FALLBACK=""
    case "$(uname -m)" in
        x86_64) ARCH="amd64" ;;
        i386|i486|i586|i686) ARCH="386" ;;
        aarch64) ARCH="aarch64"; ARCH_FALLBACK="arm64" ;;
        arm64) ARCH="arm64"; ARCH_FALLBACK="aarch64" ;;
        armv7l|armv7*) ARCH="armv7" ;;
        *) die "不支持的架构: $(uname -m)" "Unsupported architecture: $(uname -m)" ;;
    esac
}

# --- Download + verify -------------------------------------------------------
VOCAT_TMP=""
download_and_verify() {
    VOCAT_TMP=$(mktemp -d)
    trap 'rm -rf "$VOCAT_TMP"' EXIT
    local base="https://github.com/${REPO}/releases/download/v${TARGET_VERSION}"
    local asset="vocat-linux-${ARCH}"
    if [ -n "$ARCH_FALLBACK" ] && ! curl -fsIL -o /dev/null "${base}/${asset}"; then
        asset="vocat-linux-${ARCH_FALLBACK}"
    fi
    msg "下载 $asset ..." "Downloading $asset ..."
    curl -fsSL -o "${VOCAT_TMP}/vocat" "${base}/${asset}" || die "下载二进制失败。" "Failed to download the binary."
    curl -fsSL -o "${VOCAT_TMP}/SHA256SUMS" "${base}/SHA256SUMS" || die "下载 SHA256SUMS 失败。" "Failed to download SHA256SUMS."

    local expected actual
    # Match a line whose filename field equals the asset (with optional binary-mode * prefix).
    expected=$(awk -v a="$asset" '$2 == a || $2 == ("*" a) {print $1; exit}' "${VOCAT_TMP}/SHA256SUMS")
    [ -n "$expected" ] || die "SHA256SUMS 中找不到 $asset 的校验行。" "$asset not found in SHA256SUMS."
    actual=$(sha256sum "${VOCAT_TMP}/vocat" | awk '{print $1}')
    [ "$actual" = "$expected" ] || die "SHA-256 校验失败。" "SHA-256 verification failed."
}

# --- Install binary ----------------------------------------------------------
install_binary() {
    install -d -m 0755 "$INSTALL_DIR"
    install -m 0755 "${VOCAT_TMP}/vocat" "${BINARY_PATH}.new"
    if [ -e "$BINARY_PATH" ]; then
        cp -a "$BINARY_PATH" "${BINARY_PATH}.bak"
    fi
    mv -f "${BINARY_PATH}.new" "$BINARY_PATH"
    install -d -m 0755 "$(dirname "$LINK_PATH")"
    ln -sfn "$BINARY_PATH" "$LINK_PATH"
}

# --- Data directory ----------------------------------------------------------
ensure_data_dir() {
    install -d -m 0755 /opt/vocat/data
    chown -R root:root /opt/vocat
}

# --- Env file (first install only) -------------------------------------------
# Generates a random 32-char secret, stores it in the 0600 env file, and flags
# FIRST_INSTALL so we can print the secret once at the end.
FIRST_INSTALL=0
setup_env() {
    if [ -f "$ENV_FILE" ]; then
        return
    fi
    install -d -m 0755 "$ENV_DIR"
    local secret
    secret=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
    [ -n "$secret" ] || die "生成随机密钥失败。" "Failed to generate a random secret."
    printf 'VOCAT_ADMIN_PASSWORD=%s\n' "$secret" > "$ENV_FILE"
    chmod 0600 "$ENV_FILE"
    FIRST_INSTALL=1
}

# --- systemd unit ------------------------------------------------------------
write_unit() {
    cat > "$UNIT_PATH" <<EOF
[Unit]
Description=vocat cellular and VoWiFi control service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
WorkingDirectory=/opt/vocat
EnvironmentFile=${ENV_FILE}
Environment=VOCAT_ADDR=0.0.0.0:7575
Environment=VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db
# Fail before starting the service if the external networking/QMI helpers are
# missing. This prevents a healthy-looking UI with unusable modem controls.
ExecStartPre=/bin/sh -ec 'command -v ip >/dev/null; command -v busybox >/dev/null; busybox udhcpc --help >/dev/null 2>&1; command -v qmi-network >/dev/null; command -v qmicli >/dev/null; command -v qmi-proxy >/dev/null 2>&1 || test -x /usr/libexec/qmi-proxy || test -x /usr/lib/libqmi-glib/qmi-proxy || test -x /usr/lib64/libqmi-glib/qmi-proxy'
ExecStart=${BINARY_PATH}
Restart=on-failure
RestartSec=3s
TimeoutStartSec=30s
# HTTP, VoWiFi, and modem cleanup have bounded shutdown contexts totalling up
# to 30 seconds. Leave a small margin before systemd resorts to SIGKILL.
TimeoutStopSec=40s

AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=false
ProtectSystem=strict
ProtectHome=true
ProtectKernelLogs=true
ProtectKernelModules=true
ProtectKernelTunables=true
ProtectControlGroups=true
# The web/CLI self-updater verifies a release in this directory and atomically
# renames it over the running binary. Keep the rest of the host read-only.
ReadWritePaths=/opt/vocat/data /opt/vocat/bin
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK AF_PACKET
RestrictRealtime=true
LockPersonality=true
MemoryDenyWriteExecute=true
UMask=0077
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
    chmod 0644 "$UNIT_PATH"
}

enable_and_start() {
    systemctl daemon-reload
    systemctl enable vocat
    if systemctl restart vocat; then
        return
    fi
    if [ -e "${BINARY_PATH}.bak" ]; then
        msg "新版本启动失败，正在恢复旧二进制。" "The new version failed to start; restoring the previous binary."
        cp -a "${BINARY_PATH}.bak" "$BINARY_PATH"
        systemctl restart vocat || true
    fi
    die "vocat 服务启动失败。" "The vocat service failed to start."
}

# --- Main --------------------------------------------------------------------
ensure_runtime_dependencies
resolve_target_version
detect_arch
skip_if_equal
download_and_verify
install_binary
ensure_data_dir
setup_env
write_unit
enable_and_start

if [ "$FIRST_INSTALL" -eq 1 ]; then
    secret=$(grep -E '^VOCAT_ADMIN_PASSWORD=' "$ENV_FILE" | cut -d= -f2-)
    echo
    msg "================ 安装完成 ================" "================ Install complete ================"
    msg "首次安装已生成管理员初始密码 (仅显示一次):" "First-install admin password (shown once):"
    echo
    echo "    $secret"
    echo
    msg "用户名为 admin。请立即记录此密码。" "Username is admin. Record this password now."
    msg "登录后或运行以下命令修改密码:" "Change it via the web UI or run:"
    echo "    sudo vocat menu"
    msg "==========================================" "=============================================="
else
    echo
    msg "================ 更新完成 ================" "================ Update complete ================"
    msg "已更新到 $TARGET_VERSION，服务已重启。" "Updated to $TARGET_VERSION; service restarted."
    msg "管理员密码保持不变。" "Admin password unchanged."
    msg "==========================================" "=============================================="
fi
