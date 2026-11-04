<p align="center">
  <img src="../web/public/favicon.svg" width="96" alt="Vocat">
</p>

<h1 align="center">VoCat</h1>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go&logoColor=white">
  <img alt="React" src="https://img.shields.io/badge/React-19-61DAFB?style=flat-square&logo=react&logoColor=111111">
  <img alt="TypeScript" src="https://img.shields.io/badge/TypeScript-5.8-3178C6?style=flat-square&logo=typescript&logoColor=white">
  <img alt="Vite" src="https://img.shields.io/badge/Vite-7-646CFF?style=flat-square&logo=vite&logoColor=white">
  <img alt="Tailwind CSS" src="https://img.shields.io/badge/Tailwind_CSS-3-06B6D4?style=flat-square&logo=tailwindcss&logoColor=white">
  <img alt="SQLite" src="https://img.shields.io/badge/SQLite-Embedded-003B57?style=flat-square&logo=sqlite&logoColor=white">
</p>

<p align="center">
  <img alt="Linux" src="https://img.shields.io/badge/Linux-amd64_%7C_386_%7C_arm64_%7C_aarch64_%7C_armv7-FCC624?style=flat-square&logo=linux&logoColor=111111">
  <img alt="Docker" src="https://img.shields.io/badge/Docker-Multi--Arch-2496ED?style=flat-square&logo=docker&logoColor=white">
  <img alt="WiFi Calling" src="https://img.shields.io/badge/WiFi_Calling-IMS_SMS-7B1FA2?style=flat-square">
  <img alt="eSIM" src="https://img.shields.io/badge/eSIM-LPA_%2F_eUICC-009688?style=flat-square">
  <img alt="Telegram" src="https://img.shields.io/badge/Telegram-Bot-26A5E4?style=flat-square&logo=telegram&logoColor=white">
  <img alt="GitHub Actions" src="https://img.shields.io/badge/GitHub_Actions-Release-2088FF?style=flat-square&logo=githubactions&logoColor=white">
</p>

[English](../README.md) | [العربية](README.ar.md) | **简体中文** | [繁體中文](README.zh-TW.md) | [Français](README.fr.md) | [Русский](README.ru.md) | [Español](README.es.md) | [日本語](README.ja.md)

Vocat 是一款面向 Quectel EC20/EC25 系列与中移物联 ML307 系列蜂窝模组的开源 Web 控制面板与工程工具套件。它在一个自包含的服务中整合了模组发现、实时射频状态、AT 与 USSD 终端、短信、WiFi Calling（WiFi 通话）、eSIM 管理、网络选择、代理路由、通知、审计日志以及发布自动化。

后端使用 Go 编写，界面采用 React 与 TypeScript 构建，生产环境前端被嵌入进 Go 二进制中。单个可执行文件即包含完整的 Web 应用，并使用 SQLite 进行持久化存储。

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## 功能

| 领域 | Vocat 提供的能力 |
| --- | --- |
| 设备管理 | 自动串口/USB 发现、多模组支持、设备友好名称、概览实时刷新、模组重启、飞行模式以及 USB 网卡模式控制。 |
| 射频与网络 | 注册状态、运营商、信号指标、RSRP/RSRQ/SINR、网络模式、频段、信道、运营商扫描、自动/手动选网，以及 LTE 小区查询与单小区锁定/解锁（EARFCN/PCI）。 |
| AT 与 USSD | 交互式 AT 终端、命令历史、原始模组响应、USSD 发起/继续/取消流程以及清晰的模组错误上报。 |
| 短信 | 蜂窝与 IMS 短信直接发送、入站同步、分段短信处理、送达报告、会话历史、未读状态、时间戳以及逐条消息的送达状态。 |
| WiFi Calling | IKEv2/ePDG 隧道建立、EAP-AKA 鉴权、IMS 注册、IMS 短信、重连控制、状态诊断以及按设备路由。 |
| eSIM 与 eUICC | eUICC 发现、EID 与生产信息、证书元数据、多 eUICC 清单、已安装配置文件列表、启用/禁用/切换操作，以及在卡片支持时进行下载、重命名和删除。 |
| 卡策略 | 基于 ICCID 的 WiFi Calling 与飞行模式行为，策略即时应用。 |
| 代理路由 | 上游 SOCKS 路由、设备绑定、国家规则、TCP 可达性检查以及面向 WiFi Calling 数据路径的 UDP Associate 检查。 |
| 通知 | 通过 Telegram、Bark、邮件、Pushplus 以及签名 Webhook 转发新入站短信，每条短信单独推送。 |
| Telegram 机器人 | 设备状态、已安装配置文件列表与切换、WiFi Calling 控制以及短信发送。敏感操作需要管理员确认。 |
| 运维 | 密码与 SSO 鉴权（Pocket ID、GitHub、Generic OAuth2）、CSRF 防护、访问策略、审计事件、实时日志、日志留存、健康检查、响应式布局、深色模式以及中英文应用界面。 |
| 分发 | 静态 Linux 二进制、systemd 安装脚本、带 SHA-256 校验的自更新、Docker 镜像、GHCR 发布以及 GitHub Actions 发布构建。 |

## 支持的硬件

Vocat 面向基于高通芯片的 Quectel 模组与中移物联 ML307 系列模组，这些模组需暴露兼容的 AT、QMI、串口与 USB 网络接口，包括：

- Quectel EC20
- Quectel EC25
- Quectel EG25 系列
- 兼容的 EG600 及相关模组
- 中移物联 ML307 系列（例如 ML307A-E5，及各 OEM 变体）

可用功能取决于模组固件、USB 复合设备配置、SIM/eSIM 能力、主机驱动、无线网络以及运营商配置。

## 安装

### Linux 一键安装

已是 root（包括默认没有 `sudo` 的 OpenWrt/Kwrt）：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash
```

普通 Linux 用户且系统装有 sudo：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

只检查 VoWiFi/XFRM 环境，不安装 VoCat：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash -s -- --check-env
```

安装指定版本：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

VoWiFi IMS 必须使用 Linux XFRM/IPsec。OpenWrt/Kwrt 上安装脚本会从当前固件自己的软件源尝试安装严格匹配的 `ip-full`、`kmod-ipsec`、`kmod-ipsec4/6`、`kmod-crypto-authenc`、AES-CBC 和 SHA1 组件。若软件源没有与当前内核匹配的模块，必须更换包含这些组件的固件，禁止强装其他内核版本的 kmod。

如果内核无法提供 XFRM/IPsec，而你只需要蜂窝短信或数据等非 VoWiFi 功能，可使用 `--skip-vowifi-check` 安装：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh --skip-vowifi-check
```

安装程序会：

- 检测 `amd64`、`386`、`arm64`、`aarch64` 或 `armv7` 架构；
- 下载对应的 GitHub Release 二进制；
- 对照 `SHA256SUMS` 进行校验；
- 将 Vocat 安装到 `/opt/vocat`；
- 创建具有 Vocat 所需硬件与网络访问权限的强化版 systemd 服务；
- 将运行时配置存放在 `/etc/vocat/env`；
- 首次安装时生成随机初始管理员密码。

安装完成后打开：

```text
http://<server-address>:7575
```

### 手动二进制安装

从 GitHub Releases 下载对应的二进制与 `SHA256SUMS`：

| 平台 | 发布文件 |
| --- | --- |
| Linux x86-64 | `vocat-linux-amd64` |
| Linux x86 32 位 | `vocat-linux-386` |
| Linux ARM64 | `vocat-linux-arm64` |
| Linux AArch64 | `vocat-linux-aarch64` |
| Linux ARMv7 | `vocat-linux-armv7` |

校验并安装：

```bash
sha256sum -c SHA256SUMS --ignore-missing
sudo install -d -m 0755 /opt/vocat/bin /opt/vocat/data
sudo install -m 0755 vocat-linux-amd64 /opt/vocat/bin/vocat
read -rsp "Admin password: " VOCAT_BOOTSTRAP_PASSWORD; echo
printf '%s\n' "$VOCAT_BOOTSTRAP_PASSWORD" | sudo /opt/vocat/bin/vocat bootstrap-admin
unset VOCAT_BOOTSTRAP_PASSWORD
sudo env \
  VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db \
  /opt/vocat/bin/vocat serve
```

该手动命令会在前台运行 Vocat。请使用 `vocat serve` 以直接启动服务器；在 TTY 下以 root 运行无参数的 `vocat` 会进入交互式管理菜单。如需托管的 systemd 服务与自动重启，请使用一键安装脚本。

### Docker

如果 Linux 主机需要发现每一个接入的受支持 Quectel 模组，并持续感知 USB 热插拔事件，请以硬件访问模式运行 Vocat：

```bash
docker pull ghcr.io/mengmengcode/vocat:latest

read -rsp "Admin password: " VOCAT_BOOTSTRAP_PASSWORD; echo
printf '%s\n' "$VOCAT_BOOTSTRAP_PASSWORD" | docker run --rm -i \
  --user 0:0 \
  -v vocat-data:/opt/vocat/data \
  --entrypoint /opt/vocat/bin/vocat \
  ghcr.io/mengmengcode/vocat:latest bootstrap-admin
unset VOCAT_BOOTSTRAP_PASSWORD

docker run -d \
  --name vocat \
  --restart unless-stopped \
  --network host \
  --privileged \
  --user 0:0 \
  -v vocat-data:/opt/vocat/data \
  -v /dev:/dev \
  -v /sys:/sys:ro \
  ghcr.io/mengmengcode/vocat:latest
```

容器启动后打开 `http://<server-address>:7575`。必须使用主机网络，才能让 QMI 网络接口保持对 Vocat 可见；串口、QMI 控制节点、TUN 接口、网络配置以及容器启动后新增的设备则需要特权设备访问。`/dev` 绑定挂载使新的 `ttyUSB*`、`ttyACM*`、`cdc-wdm*` 和 MHI `wwan*` 节点无需重建容器即可见。

该模式有意赋予 Vocat 对主机设备和网络栈的广泛访问权限，请仅在受信任的 Linux 主机上使用。自动发现会识别受支持的 Quectel USB 模组（USB 厂商 ID `2c7c`）、中移物联 ML307 系列模组（按产品字符串识别），以及通过 Linux WWAN 子系统暴露的 PCIe/MHI 模组；它不能识别任意模组布局。仅用 `--device` 映射单个节点，例如 `/dev/ttyUSB2`、`/dev/cdc-wdm0` 或 `/dev/wwan0qmi0`，会将容器限定在这些固定节点上，无法提供完整的多设备或热插拔发现。

在 OpenWrt 主机上，fw4 的 input 链会丢弃来自其未管理接口的新连接，其中包括 Vocat 为每个 VoWiFi 会话创建的 TUN 接口。因此 Vocat 会用镜像自带的 `nft` 插入范围很窄的 `vocat-ims-*` 放行规则（仅 P-CSCF 到协商得到的内层地址）。缺少这些规则时 IMS 注册和发短信仍然正常，但收短信和来电会被拒绝。如果镜像自带的 `nft` 无法读取主机规则集，Vocat 会记录 `nft could not inspect the OpenWrt fw4 input chain`，此时可让 `VOCAT_NFT` 指向主机自己的 nft：

```bash
cat > /opt/vocat-host-nft <<'EOF'
#!/bin/sh
exec /host/lib/ld-musl-$(uname -m).so.1 --library-path /host/usr/lib:/host/lib /host/usr/sbin/nft "$@"
EOF
chmod 755 /opt/vocat-host-nft
# 在上面的 docker run 命令中追加：
#   -v /usr:/host/usr:ro -v /lib:/host/lib:ro \
#   -v /opt/vocat-host-nft:/usr/local/bin/host-nft:ro \
#   -e VOCAT_NFT=/usr/local/bin/host-nft
```

GHCR 镜像发布为 `linux/amd64` 与 `linux/arm64`。

> [!TIP]
> **NAS / QNAP Container Station 部署说明**：
> 在 QNAP QTS / QuTS hero（Container Station）等 NAS 操作系统上，自定义的非 root 管理员账号和卷隔离机制可能导致 Docker 命名卷（例如 `-v vocat-data:/opt/vocat/data`）在一次性的 `bootstrap-admin` 初始化和后台服务容器中解析为不同的隔离路径，从而在 Web 登录时出现“密码错误”。
> 在 NAS 环境中，强烈建议初始化和运行时均以主机绝对路径的绑定挂载替代命名卷（例如 QNAP 上的 `-v /share/Container/vocat/data:/opt/vocat/data`），以保证 SQLite 数据库持久化存储的一致性。

### USB SIM 读卡器

USB SIM 读卡器使用 Linux PC/SC 服务。一键安装程序会在支持的软件包管理器上自动安装并启动 `pcscd` 及 CCID 驱动。Debian/Ubuntu 上等效的手动安装命令为 `apt install pcscd libccid`。如果 USB 已识别 CCID 读卡器，但 PC/SC 不可用，VoCat 会继续在添加设备对话框中显示该读卡器，并报告缺少的服务或驱动，而不是静默隐藏它。

### QMI 命令行工具

VoCat 使用 `qmicli` 验证 QMI 控制通道是否就绪，并使用 `qmi-proxy` 复用对该通道的访问。分组数据会话由内置 QMI WDS 客户端管理，而非使用 `qmi-network` 的 CID/PDH 状态文件。一键安装程序会安装并验证相应工具。手动部署时，Debian/Ubuntu 使用 `apt install libqmi-utils`；Arch Linux 使用 `pacman -S libqmi`，Alpine 使用 `apk add qmi-utils`，OpenWrt 使用 `opkg install qmi-utils`。

`vocat doctor --repair-dji-qmi` 会在更改任何 USB 驱动绑定或置位 DTR 之前检查 `qmicli`。如果该工具不可用，命令会停止并给出安装提示，保持设备当前状态不变。

### AT 蜂窝主机数据

基于 AT 的 ECM/RNDIS 数据需要 Linux、iproute2 以及带有 `udhcpc` 的 BusyBox。
VoCat 必须独占管理模组网络接口；请禁用该接口上竞争的 DHCP 客户端或网络管理配置。VoCat 会维护 IPv4 租约和打标策略路由，而不会更改系统默认路由或解析器配置。该路径目前需要支持 IPv4 的蜂窝服务。

## 配置

Vocat 先从 `VOCAT_CONFIG` 读取可选的 JSON 配置文件，再应用 `VOCAT_*` 环境变量。环境变量优先级更高。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | HTTP 监听地址。 |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | SQLite 数据库路径。 |
| `VOCAT_SESSION_TTL` | `24h` | 鉴权会话有效期。 |
| `VOCAT_SECURE_COOKIES` | `false` | 可选：强制所有入口使用安全 Cookie；默认按 HTTP/HTTPS 入口自动适配。 |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | 优雅关闭超时时间。 |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | API 请求体最大字节数。 |
| `VOCAT_MCP_ENABLED` | `true` | 在 `/mcp` 提供面向 AI Agent 的 MCP 端点。设为 `false` 可禁用。 |
| `VOCAT_PROBE_UNKNOWN_USB` | `false` | 允许使用 AT 命令探测未识别的 USB 串口设备。探测会更改串口设置并清空输入缓存；仅当这些端口专用于模组时再启用。已验证的模组身份会自动被发现。JSON 键名：`probe_unknown_usb`。 |
| `VOCAT_REPO` | `MengMengCode/VoCat` | 自更新器使用的受信任 GitHub 仓库，格式为 `owner/name`。 |
| `GITHUB_TOKEN` | 空 | 可选的 GitHub token，用于私有仓库或更高的 API 限额。 |

用户提供的 Apple 运营商配置包可通过 `vocat carrier import-ipcc` 转换为可审查、符合允许列表的运营商配置文件；参见 [docs/CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md)。

管理员凭据仅存储在 SQLite 中。使用 `vocat bootstrap-admin` 对空数据库执行一次初始化；环境变量和 JSON 配置不能设置或覆盖管理员用户名或密码。

请勿将 Telegram token、SMTP 密码、Webhook 密钥、SIM 凭据或其他私密数据存放在仓库中。请通过应用设置或受保护的环境文件来配置它们。

## 单点登录（SSO）

VoCat 支持 Pocket ID（OpenID Connect）、GitHub.com OAuth App 和 Generic OAuth2，
在**设置 → 单点登录（SSO）**中选择其中一个身份提供方。SSO 默认关闭，密码登录始终可用。
先使用本地管理员登录、保存配置并主动绑定外部账号，再启用 SSO。
未绑定的外部用户不能注册或获得管理员权限；相同邮箱也不会自动合并账号。

### 配置 Pocket ID

1. 为 VoCat 配置 HTTPS 根路径入口，例如 `https://vocat.example.com`，并设置
   HTTPS 公开地址。Cookie 会按访问入口自动适配，无需设置 `VOCAT_SECURE_COOKIES`；该环境变量只用于强制所有入口使用安全 Cookie。在 SSO 设置选择 **Pocket ID**，公开入口初始使用当前
   浏览器访问地址，需要调整时直接编辑**VoCat 公开地址**。页面立即显示紧凑的回调 URL 和复制按钮，
   无需先保存或填写客户端凭据：
   `https://vocat.example.com/api/auth/sso/callback`。
2. 在 Pocket ID 为 VoCat 单独创建 **Confidential** OIDC Client，登记精确回调地址，
   并确认目标用户或用户组获准使用该客户端。VoCat 与 Komari 应分别注册客户端，
   各自签发本地会话。
3. 在 VoCat 填写 Pocket ID 精确的公开 `APP_URL` 作为 Issuer，并填入新客户端的
   Client ID 和 Client Secret。Client ID 字段提示为“填写身份服务中为 VoCat 创建的客户端 ID”。
   Scopes 和 PKCE S256 由程序处理，Token 客户端认证根据
   已验证的 Discovery 自动选择，优先 Basic，其次 Post。保持 SSO 关闭并保存。
   修改访问地址会立即更新显示的回调地址，保存后用于实际授权。
4. 检查已保存的配置，成功时显示“配置检查通过”。然后发起账号绑定，完成 Pocket ID Passkey 登录和授权，
   返回 VoCat 确认绑定状态。配置检查仅验证元数据、结构和可达性；真实绑定前不能
   据此认定 Client Secret 正确或用户获准访问。
5. 启用并保存 SSO，在隐身窗口验证登录、原页面恢复及登录后的第一次写操作。

设置页显示已绑定账号和解绑操作。更换外部账号时，先解绑，再绑定目标账号并重新启用 SSO。

Issuer 按发布值精确匹配，包括路径和尾斜杠。Pocket ID 使用经过验证的 ID Token 的
`issuer + sub` 关联账号，不依赖 UserInfo 端点或邮箱。Discovery、授权、Token 和 JWKS
端点必须与配置的 Issuer 同 origin；首期不支持通过 `INTERNAL_APP_URL` 发布不同 origin
的分离后端。Issuer 仍填写公开 `APP_URL`。已保存的显式客户端认证方式和 Scopes 会继续保留。

### GitHub 和 Generic OAuth2

GitHub：在 GitHub.com 新建 OAuth App，登记相同的精确回调地址；在 VoCat 选择
**GitHub**，填写 Client ID 和 Secret，依次保存、检查、绑定、启用。
端点固定为 GitHub.com，默认 scope 为 `read:user`，强制使用 PKCE S256。
绑定使用稳定数值账号 ID，修改 GitHub 用户名不会更换绑定身份。首期不支持 GitHub Enterprise。

Generic OAuth2：除客户端凭据外，还需填写授权、Token、UserInfo URL。Scopes、顶层
用户 ID 字段名（默认 `id`）、Token 认证和 PKCE 兼容选项在选择 Generic 时直接显示。
UserInfo 通过 Bearer access token 请求；用户 ID
必须为非空字符串或整数，大整数保留精确值。默认 Token 认证为 `client_secret_post`，
默认 PKCE 为 S256。只有确实不支持 PKCE 的旧服务才使用关闭选项。
Generic OAuth2 不提供 OIDC ID Token 验证。

### 同时使用域名和内网 IP 登录

同一份身份服务配置和账号绑定可用于已配置的 HTTPS 公开入口，以及 localhost、loopback 和明确的私网 IP 入口。请在身份服务登记每个入口的完整回调 URL（路径为 `/api/auth/sso/callback`）。程序按当前入口生成授权回调地址，并在 Token 交换时使用同一个地址；跨入口提交回调会被拒绝。普通公网 HTTP 地址仍不被允许。

IPv6 回调地址采用浏览器压缩后的写法，IPv4 映射 IPv6 地址也使用十六进制形式。请在身份服务中登记设置页面显示的回调地址。已有的展开形式配置会自动识别，但身份服务必须允许规范化后的回调地址。

直接 TLS、已配置的 HTTPS 反代入口或可信的 `X-Forwarded-Proto` 会自动启用安全 Cookie；未受信任的转发头不会参与判断，反代应保留 Host。密码会话依据请求传输方式、显式强制设置以及同源 HTTPS Origin/Referer 信息选择 Cookie 属性；浏览器信息只可加强 Cookie 保护，不参与鉴权、网络访问控制或回调地址选择。

会话在登录时保存安全 Cookie 要求，之后识别到 HTTPS 时只会加强保护；即使 CSRF Cookie 丢失或失效、刷新请求又没有协议提示，补发时也会保留这个要求。数据库升级会保留已有会话；旧会话无法反推出此前的传输协议，HTTPS 用户重新登录一次或进行一次带有可识别 HTTPS 信息的刷新后，即可保存该要求。

保存或关闭 SSO 不会改变密码客户端的行为。若代理剥除全部浏览器与传输协议信息，需要显式提供可信 HTTPS 标识。公开地址的推断仅用于 SSO 登录和 SSO 会话的 CSRF 刷新。独立的内网 HTTP 入口无需切换配置或重启。混合使用两种协议时请移除旧的全局 `VOCAT_SECURE_COOKIES=true` 强制设置。HTTP 入口的登录凭据会以明文传输。

### 网络、凭据与会话

SSO 路由继续受现有来源网络策略限制，启用 SSO 不会将内网入口自动开放到公网。
浏览器须能访问 VoCat 和授权页，VoCat 进程须能访问 Discovery、Token 和 JWKS/UserInfo
端点并信任其 TLS 证书。管理员保存的 Pocket ID HTTPS Issuer 或 Generic 各端点 URL
即为精确 origin 许可，可连接 RFC1918/IPv6 ULA 私网 HTTPS 服务；
GitHub 固定端点仍仅允许公网地址。

Generic 的内部服务需要 HTTP 时，在 Token/UserInfo 字段显式填写完整 HTTP URL。
每次 DNS 解析的所有候选和实际拨号地址都必须为获许可的私网。
Pocket ID 的 Discovery 不会自动批准其他 origin，其端点必须与配置的 Issuer 同 origin。
Issuer、Discovery 获取地址与浏览器授权页始终要求 HTTPS。IdP 连接继续拒绝 loopback、
link-local、云元数据和其他特殊用途地址。私有 CA 应安装到主机或容器的受控信任库，
不提供跳过 TLS 验证的开关。VoCat 自身使用 localhost 或 HTTPS 入口，与这些 IdP 出站
许可相互独立。

当前 Compose 使用 host network，应在实际容器内验证 DNS、路由和 CA，不假定桥接网络
service name 可用。公开域名反代入口应与已保存的公开 origin 一致；同时允许显式私网 IP 入口，
不从任意转发 Host 头推导回调地址。首期只支持根路径部署。本地 Vite 开发时统一使用一个 loopback origin，例如
`http://127.0.0.1:5173`，并在身份平台登记同一回调地址；IdP 出站网络例外仅用于测试 fixture。

主 Session 和 CSRF Cookie 保持 `SameSite=Strict`，短期授权 Cookie 使用
`SameSite=Lax` 以支持顶层 GET 跨站回调。任何成功的 SSO 配置保存，包括停用或轮换密钥，
都会撤销已有 SSO 会话；解绑会撤销旧绑定的 SSO 会话。这些操作保留密码会话和
API Token。修改本地管理员密码会撤销全部 Session 和 API Token、取消在途授权，并保留
外部绑定。当前 SSO 会话被成功操作撤销时，界面会提示重新登录。

密钥输入留空代表保留旧值，输入新密钥可替换原值；设置页不提供清除已保存密钥的控件。
更换 Provider、Client ID、Issuer，或 Generic 的身份端点/ID 字段时，会切换身份命名空间，
需要重新填写凭据并绑定；已有绑定时必须明确确认清除。更换绑定账号应先解绑，再重新绑定。
Client Secret 仅保存在服务器受权限保护的 SQLite 中；Sensitive 标志用于接口脱敏，
不代表数据库加密。第三方 Token 不持久化，也不返回浏览器。

普通退出仅结束当前 VoCat 会话。在 IdP 停用用户或退出 Pocket ID，不会立即撤销既有
VoCat 会话；它会按 `VOCAT_SESSION_TTL` 到期，也可在 VoCat 停用 SSO 或解绑以立即撤销。
公开登录能力接口为 `GET /api/auth/providers`；SSO 设置和绑定管理仅接受浏览器 Session，
写操作还要求 CSRF，Bearer 不可管理这些凭据。版本冲突返回 `409 sso_config_changed`，
身份源变更需确认时返回 `409 sso_rebind_required`。

### 升级与回滚

SSO 增加 SQLite schema 29。升级保留原管理员、Session、API Token 和设备业务数据，
且不会自动启用 SSO。升级前记录旧二进制版本或镜像 digest、配置和 CA，并在停服务后
备份完整持久化目录；在线备份应使用 SQLite backup API 或 `.backup`。
WAL 模式下不能只裸拷贝正在运行的 `.db` 文件作为可靠备份。

配置故障时优先在新版中关闭 SSO，继续使用密码登录并保留当前业务数据。
回退到只支持 schema 28 的旧程序时，必须停服务并恢复匹配的升级前数据与配置快照。
仅回退镜像或手工降低 `PRAGMA user_version` 无法降级数据库。建议将旧快照恢复到独立
目录/卷后切换，并保留新库排查；恢复快照会失去备份后收到的短信和任务变更。
Docker 通过拉取镜像并重建容器升级。

生产发布前，应在实际部署完成 Pocket ID Passkey/访问策略联调和升级、回滚演练。

## API Token 与 MCP

非浏览器客户端可以使用 Bearer Token 向相同的 `/api/...` 端点进行身份验证，无需使用浏览器的会话 Cookie 和 CSRF 流程：

```bash
vocat token create --read-only --ttl 720h ci-runner   # 仅打印一次 Token
curl -H "Authorization: Bearer vcat_..." http://127.0.0.1:7575/api/devices
```

`vocat token list` 和 `vocat token revoke <name>` 用于管理 Token 清单。Token 拥有管理员权限；`--read-only` Token 只能发起 `GET` 请求（一个经文档记录的特例：读取短信会话会将其在本地标记为已读——规则是“不产生硬件或网络变更”）。轮换管理员密码会撤销所有 Token。请在首次执行 `bootstrap-admin` 后创建 Token。远程 Bearer 客户端还需要其来源地址在网络访问策略许可范围内；默认的 `internal` 模式仅允许私有网络。

默认情况下（设置 `VOCAT_MCP_ENABLED=false` 可禁用），同一服务会在 `http://<host>:7575/mcp` 为 AI Agent 暴露一个基于 Streamable HTTP 传输和 Bearer Token 认证的 MCP（Model Context Protocol）端点。提供的工具涵盖设备状态、短信会话及受保护的发送、eSIM 配置文件列表与切换，以及 WiFi Calling 状态/重连；敏感操作（原始 AT、eSIM 下载/删除、SIM PIN、网络选择、系统更新）未暴露，且工具输出中绝不包含 IMSI、SIM PIN 或激活码。只读 Token 获取的工具列表中不包含写操作工具。短信发送遵循与 Web 界面相同的受阻目的地策略和全局每小时限额，并走蜂窝 AT 路径发送（Web 界面在启用 WiFi Calling 时的 IMS 优先路由仍保留在 Web 端，且通过 MCP 发送的短信暂未写入 Web 会话历史）。

客户端配置示例（以 Claude Code 为例）：

```json
{
  "mcpServers": {
    "vocat": {
      "type": "http",
      "url": "http://192.168.1.10:7575/mcp",
      "headers": { "Authorization": "Bearer vcat_..." }
    }
  }
}
```

设计记录请参阅 [docs/API_MCP_DEVELOPMENT_PLAN.md](API_MCP_DEVELOPMENT_PLAN.md)。

## Telegram 机器人

启用 Telegram 通知并配置好 Chat ID 与 Admin ID 后，机器人支持：

```text
/status [device]
/esim <device>
/switch <device> <iccid>
/wfc <device> <status|on|off|reconnect>
/sms <device> <number> <message>
/ussd <device> <code>
/ussd_reply <session> <input>
/ussd_cancel <session>
```

配置文件切换与短信提交使用一次性确认按钮。机器人不暴露 eSIM 下载、删除或重命名命令。

网页终端与 Telegram 共用 USSD 路由。启用 WiFi Calling 时，请求使用 IMS/USSI；IMS 未就绪时会明确报错并提示等待注册，不会回退到蜂窝 AT+CUSD。关闭 WiFi Calling 时使用蜂窝 USSD。菜单回复与取消始终绑定原设备和会话。取消失败会返回错误并保留会话供重试；发送结果不确定时不会自动换通道重新提交。

USSI 会话处理遵循 [3GPP TS 24.390](https://www.etsi.org/deliver/etsi_ts/124300_124399/124390/18.00.00_60/ts_124390v180000p.pdf)。BYE 失败时保留取消记录，但开始取消后禁止继续提交菜单输入（[RFC 3261 第 15.1.1 节](https://www.rfc-editor.org/rfc/rfc3261.html#section-15.1.1)）。

## 更新

检查是否有更新的 GitHub Release：

```bash
vocat update --check --repo MengMengCode/VoCat
```

安装最新发布版：

```bash
sudo vocat update --repo MengMengCode/VoCat
```

更新器会下载与当前 Linux 架构匹配的二进制，使用已发布的 `SHA256SUMS` 进行校验，原子性地替换可执行文件，并在可用时重启 `vocat` systemd 服务。

Docker 安装的更新方式：

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

拉取新镜像后重建容器。

## 开发

依赖要求：

- Go 1.25 或更新版本
- Node.js 20 或更新版本
- npm

运行前端开发服务器：

```bash
cd web
npm install
npm run dev
```

构建嵌入的前端并启动后端：

```bash
cd web
npm run build
cd ..
go run ./cmd/vocat
```

运行全部测试：

```bash
go test ./...
```

构建生产二进制：

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## 发布自动化

推送版本标签会触发两个 GitHub Actions 工作流：

- `release-binaries` 构建并发布 `amd64`、`386`、`arm64`、`aarch64` 与 `armv7` 二进制及 `SHA256SUMS`。
- `docker` 构建并向 GitHub Container Registry 发布多架构镜像。

```bash
git tag v0.2.0
git push origin v0.2.0
```

## 项目结构

```text
cmd/vocat/                  应用入口与 CLI
internal/device/            模组发现与设备控制
internal/modem/             AT 会话与响应处理
internal/server/            HTTP API、通知与内嵌 Web 服务器
internal/store/             SQLite 持久化
internal/update/            GitHub Release 自更新器
internal/vowifi/            IKE、EAP-AKA、IMS 与 WiFi Calling 运行时
scripts/install.sh          Linux 安装与更新脚本
web/src/                    React 与 TypeScript 前端
.github/workflows/          二进制与 Docker 发布自动化
```

## 负责任地使用

蜂窝模组与 eSIM 操作可能影响用户服务、已存储的配置文件、网络注册以及硬件状态。请做好备份，谨慎审视破坏性操作，并仅在您被允许操作所连接的硬件与网络资源的合法环境中使用本软件。

Vocat 不会绕过运营商鉴权、网络策略、硬件安全或 eSIM 信任要求。支持某项操作意味着 Vocat 能够向模组或 eUICC 发起该请求；但设备、配置文件、网络或运营商仍可能拒绝。

## 贡献

欢迎提交 Issue 与 Pull Request。请保持改动聚焦，在可行处附带测试，避免提交凭据或用户数据，并清晰地说明硬件相关行为。

提交改动前：

```bash
go test ./...
cd web && npm run build
```

## 致谢
- [Nodeseek.com](https://www.nodeseek.com) — 专注服务器的社群
- [Linux.do](https://linux.do) — 富有启发的技术社群
- [iniwex5](https://github.com/iniwex5) — 风格与功能指南

## 请我喝杯咖啡

| 网络 | 地址 |
| ------- | ------- |
| USDT-TRON (TRC20) | `TWSAkvzVsFc7KqncDLmUfRxpPQbpV5CgTB` |
| USDT-BSC (BEP20) | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |
| USDT-Polygon | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |

## 许可证

参见 [LICENSE](../LICENSE)。

## Star History

<a href="https://www.star-history.com/?repos=mengmengcode%2Fvocat&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
 </picture>
</a>
