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
  <img alt="Linux" src="https://img.shields.io/badge/Linux-amd64_%7C_386_%7C_arm64_%7C_armv7-FCC624?style=flat-square&logo=linux&logoColor=111111">
  <img alt="Docker" src="https://img.shields.io/badge/Docker-Multi--Arch-2496ED?style=flat-square&logo=docker&logoColor=white">
  <img alt="WiFi Calling" src="https://img.shields.io/badge/WiFi_Calling-IMS_SMS-7B1FA2?style=flat-square">
  <img alt="eSIM" src="https://img.shields.io/badge/eSIM-LPA_%2F_eUICC-009688?style=flat-square">
  <img alt="Telegram" src="https://img.shields.io/badge/Telegram-Bot-26A5E4?style=flat-square&logo=telegram&logoColor=white">
  <img alt="GitHub Actions" src="https://img.shields.io/badge/GitHub_Actions-Release-2088FF?style=flat-square&logo=githubactions&logoColor=white">
</p>

[English](../README.md) | **简体中文**

Vocat 是一款面向 Quectel EC20/EC25 系列蜂窝模组的开源 Web 控制面板与工程工具套件。它在一个自包含的服务中整合了模组发现、实时射频状态、AT 与 USSD 终端、短信、WiFi Calling(WiFi 通话)、eSIM 管理、网络选择、代理路由、通知、审计日志以及发布自动化。

后端使用 Go 编写,界面采用 React 与 TypeScript 构建,生产环境前端被嵌入进 Go 二进制中。单个可执行文件即包含完整的 Web 应用,并使用 SQLite 进行持久化存储。

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## 功能

| 领域 | Vocat 提供的能力 |
| --- | --- |
| 设备管理 | 自动串口/USB 发现、多模组支持、设备友好名称、概览实时刷新、模组重启、飞行模式以及 USB 网卡模式控制。 |
| 射频与网络 | 注册状态、运营商、信号指标、RSRP/RSRQ/SINR、网络模式、频段、信道、运营商扫描以及自动/手动选网。 |
| AT 与 USSD | 交互式 AT 终端、命令历史、原始模组响应、USSD 发起/继续/取消流程以及清晰的模组错误上报。 |
| 短信 | 蜂窝与 IMS 短信直接发送、入站同步、长短信合并、送达报告、会话历史、未读状态、时间戳以及逐条消息的送达状态。 |
| WiFi Calling | IKEv2/ePDG 隧道、EAP-AKA/EAP-AKA' 鉴权、IMS 注册、IMS 短信与通话、重连控制、状态诊断及按设备路由的工程实现；运营商互通性需另行验证。 |
| eSIM 与 eUICC | eUICC 发现、EID 与生产信息、证书元数据、多 eUICC 清单、已安装配置文件列表、启用/禁用/切换操作,以及在卡片支持时进行下载、重命名和删除。 |
| 卡策略 | 基于 ICCID 的 WiFi Calling 与飞行模式行为,策略即时应用。 |
| 代理路由 | 上游 SOCKS 路由、设备绑定、国家规则、TCP 可达性检查以及面向 WiFi Calling 数据路径的 UDP Associate 检查。 |
| 通知 | 通过 Telegram、Bark、邮件、Pushplus 以及签名 Webhook 转发新入站短信,每条短信单独推送。 |
| Telegram 机器人 | 设备状态、已安装配置文件列表与切换、WiFi Calling 控制、短信发送、定时拨号并自动挂断、通话状态、接听与挂断命令。敏感操作需要管理员确认。 |
| 运维 | 鉴权、CSRF 防护、访问策略、审计事件、实时日志、日志留存、健康检查、响应式布局、深色模式以及中英文应用界面。 |
| 分发 | 静态 Linux 二进制、systemd 安装脚本、带 SHA-256 校验的自更新、Docker 镜像、GHCR 发布以及 GitHub Actions 发布构建。 |

## 支持的硬件

Vocat 面向基于高通芯片、并暴露兼容 AT、QMI、串口与 USB 网络接口的 Quectel 模组,包括:

- Quectel EC20
- Quectel EC25
- Quectel EG25 系列
- 兼容的 EG600 及相关模组

可用功能取决于模组固件、USB 复合设备配置、SIM/eSIM 能力、主机驱动、无线网络以及运营商配置。

仅凭 Snapdragon 410/MSM8916 或 OpenStick 外形不能视为受支持。该路径还
需要匹配的基带、可用的 UIM/QMI 控制口、主机 WWAN 驱动以及运营商
IMS/VoWiFi 权限，并必须在实际硬件与 SIM 组合上验收。

## 能力与验收边界

Vocat **不是**完整的高通蜂窝 IMS/VoLTE 栈。蜂窝侧通过 QMI 建立分组数据
会话，通过模组 AT 命令完成拨号、接听、挂断及通话列表操作。通话最终使用
VoLTE、回落到电路域还是失败，由模组固件、SIM、无线网络及运营商配置决定。
Vocat 目前不自行实现蜂窝 IMS PDN/注册、专用语音承载、QMI IMSA/VOICE、
SRVCC 或 LTE 与 WiFi 之间的通话连续性。

VoWiFi 代码是工程实现，不代表运营商认证，也不能证明任意 Snapdragon 410
设备支持 WiFi Calling。不要只看界面状态；应按以下顺序保存目标设备的基带
日志、抓包及通话/媒体证据：

| 阶段 | 必需证据 | 当前责任边界 |
| --- | --- | --- |
| 0. 平台 | MSM8916/基带版本、UIM、QMI/AT 端口、主机驱动 | 硬件镜像与设备集成；逐台验证 |
| 1. LTE 数据 | LTE 注册、默认数据会话、地址、DNS 与路由流量 | Vocat QMI 数据控制加模组/网络 |
| 2. 蜂窝 IMS | 独立 IMS 连通性与 P-CSCF 发现 | 模组/运营商；Vocat 未实现蜂窝 IMS 栈 |
| 3. VoLTE | IMS 已注册、主被叫、双向媒体与紧急策略检查 | 模组/运营商；仅 AT 拨号成功不够 |
| 4. ePDG | 正确的运营商 ePDG FQDN 与可达 DNS 结果 | Vocat 加运营商 DNS/配置 |
| 5. IKE | 算法协商及 ePDG 证书/AUTH 验证 | Vocat；用外层隧道抓包确认 |
| 6. EAP | 配置的 EAP-AKA 或 EAP-AKA' 使用目标 UICC 成功 | Vocat/UICC/运营商；收到挑战不等于成功 |
| 7. CHILD_SA | 内层 IP、流量选择器及可达 P-CSCF | Vocat；验证路由与加密流量 |
| 8. VoWiFi IMS | IMS 注册、主被叫短信/通话及双向媒体 | Vocat/运营商互通；逐配置验证 |
| 9. 连续性 | LTE→WiFi 与 WiFi→LTE 通话切换成功 | 当前未实现 |

紧急呼叫和运营商登记的紧急地址需要运营商集成及当地认证。Vocat 不能作为
已认证的紧急呼叫方案，因此会拒绝紧急号码拨号，也不声称能够登记紧急地址。

验收时还需注意以下实现限制：

- QMI 分组数据后端目前只支持 IPv4。请求 `IPV4V6` 时会明确报告降级为
  IPv4；IPv6-only 请求会在启动数据会话前失败。
- VoWiFi 媒体支持 G.711 PCMA/PCMU、RTCP 及协商后的 RFC 4733 DTMF；
  不提供 AMR-NB/AMR-WB 转码，运营商只提供 AMR 时会明确拒绝。
- IMS APN、私有/公共身份、传输协议、SMSC 与 EAP 方法按设备配置，并与
  蜂窝数据 APN 分离。保存后会在下一次 VoWiFi 重连时生效；如果运行态已启用，
  Vocat 会自动排队重连，不需要重启进程。
- Vocat 会按 SIM 的 home PLMN 解析与 VoHive 兼容的运营商 Profile（包括
  `O2_de_26203`），并在运行态诊断中记录匹配 PLMN、preset、ePDG 来源和 IMS
  身份来源；未知 PLMN 使用标准 fallback。
- 只要 VoWiFi 目标策略仍开启，启动失败或检测到 SIM/PLMN 变化后会按 30 秒、
  1 分钟、2 分钟退避恢复，避免持续撞击运营商注册接口。
- IKE 默认使用 MODP2048 与 SHA-256。SHA-1 兼容和 MODP1024 是两个独立的
  设备开关，默认均关闭；只应按已验证运营商 Profile 的实际要求启用对应项，
  升级时不会再按 PLMN 静默开启弱算法。
- IMS SMSC 留空时，Vocat 优先读取 SIM/模组的 `AT+CSCA?`；不再提供任何
  运营商专用的全局 SMSC 默认值。升级不会保留旧版仅适用于 Vodafone 的
  硬编码回退；SIM 返回空值时需明确填写运营商 SMSC。

## 安装

### Linux 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

安装指定版本:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

安装程序会:

- 检测 `amd64`、`386`、`arm64` 或 `armv7` 架构;
- 下载对应的 GitHub Release 二进制;
- 对照 `SHA256SUMS` 进行校验;
- 将 Vocat 安装到 `/opt/vocat`;
- 创建具有 Vocat 所需硬件与网络访问权限的强化版 systemd 服务;
- 将运行时配置存放在 `/etc/vocat/env`;
- 首次安装时生成随机初始管理员密码。

安装器还会安装并验证运行时网络工具：`iproute2`、包含 `udhcpc` applet 的
BusyBox，以及 libqmi 的 `qmi-network`、`qmicli` 和 `qmi-proxy`。Debian/
Ubuntu 使用 `iproute2`、`busybox`、`libqmi-utils`，Alpine 使用
`iproute2`、`busybox`、`qmi-utils`；其他发行版需安装等价软件包。

安装完成后打开:

```text
http://<服务器地址>:7575
```

### 手动二进制安装

从 GitHub Releases 下载对应的二进制与 `SHA256SUMS`:

| 平台 | 发布文件 |
| --- | --- |
| Linux x86-64 | `vocat-linux-amd64` |
| Linux x86 32 位 | `vocat-linux-386` |
| Linux ARM64 | `vocat-linux-arm64` |
| Linux ARMv7 | `vocat-linux-armv7` |

先安装上述运行时软件包，并确认工具可用：

```bash
command -v ip busybox qmi-network qmicli
busybox udhcpc --help >/dev/null
command -v qmi-proxy >/dev/null 2>&1 || test -x /usr/libexec/qmi-proxy
```

校验并安装:

```bash
sha256sum -c SHA256SUMS --ignore-missing
sudo install -d -m 0755 /opt/vocat/bin /opt/vocat/data
sudo install -m 0755 vocat-linux-amd64 /opt/vocat/bin/vocat
sudo env \
  VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db \
  VOCAT_ADMIN_PASSWORD=change-this-password \
  /opt/vocat/bin/vocat serve
```

该手动命令会在前台运行 Vocat。请使用 `vocat serve` 以直接启动服务器；在 TTY 下以 root 运行无参数的 `vocat` 会进入交互式管理菜单。如需托管的 systemd 服务与自动重启,请使用一键安装脚本。

### Docker

默认 Compose 以普通桥接容器启动，不提供主机设备权限。它适合查看界面与配置，
不能发现或控制模组，也不是 Snapdragon 410 的测试或部署方式：

```bash
cp .env.example .env
# 在 .env 中设置 VOCAT_ADMIN_PASSWORD。
docker compose up -d
```

在受信任的 Linux 模组主机上，显式叠加硬件配置：

```bash
docker compose \
  -f docker-compose.yml \
  -f docker-compose.hardware.yml \
  up -d
```

硬件配置使用 Compose `!reset` 标签，需要 Docker Compose 2.24 或更新版本。
它会切换到主机网络、以特权 root 运行，并挂载主机 `/dev` 与 `/sys`。这为
QMI 创建的 WWAN 接口、串口/QMI 热插拔、TUN/XFRM 和策略路由所需，同时也
赋予容器广泛的主机控制权；使用前请审查 `docker-compose.hardware.yml`。

官方镜像包含并在构建时验证 `iproute2`、BusyBox `udhcpc`、
`qmi-network`、`qmicli` 与 `qmi-proxy`。仅使用镜像部署硬件时，等价命令为：

```bash
docker pull ghcr.io/mengmengcode/vocat:latest

docker run -d \
  --name vocat \
  --restart unless-stopped \
  --network host \
  --privileged \
  --user 0:0 \
  -e VOCAT_ADMIN_PASSWORD=change-this-password \
  -v vocat-data:/opt/vocat/data \
  -v /dev:/dev \
  -v /sys:/sys:ro \
  ghcr.io/mengmengcode/vocat:latest
```

容器启动后打开 `http://<服务器地址>:7575`。`/dev` 挂载使新的
`ttyUSB*`、`ttyACM*` 和 `cdc-wdm*` 节点无需重建容器即可见。

该模式有意赋予 Vocat 对主机设备与网络栈的广泛访问权限,仅在受信任的 Linux 主机上使用。自动发现目前仅识别受支持的 Quectel USB 模组(USB 厂商 ID `2c7c`),不识别任意品牌的模组。仅用 `--device` 映射单个节点(例如 `/dev/ttyUSB2` 与 `/dev/cdc-wdm0`)会将容器限定在这些固定节点上,无法提供完整的多设备或热插拔发现。

GHCR 镜像发布为 `linux/amd64` 与 `linux/arm64`。

## 配置

Vocat 先从 `VOCAT_CONFIG` 读取可选的 JSON 配置文件,再应用 `VOCAT_*` 环境变量。环境变量优先级更高。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | HTTP 监听地址。 |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | SQLite 数据库路径。 |
| `VOCAT_ADMIN_USERNAME` | `admin` | 初始管理员用户名。 |
| `VOCAT_ADMIN_PASSWORD` | `admin` | 初始管理员密码。暴露服务前请务必修改。 |
| `VOCAT_SESSION_TTL` | `24h` | 鉴权会话有效期。 |
| `VOCAT_SECURE_COOKIES` | `false` | 在使用 HTTPS 时将会话 Cookie 标记为安全。 |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | 优雅关闭超时时间。 |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | API 请求体最大字节数。 |
| `VOCAT_REPO` | `MengMengCode/VoCat` | 自更新器使用的受信任 GitHub 仓库，格式为 `owner/name`。 |
| `GITHUB_TOKEN` | 空 | 可选的 GitHub token,用于私有仓库或更高的 API 限额。 |

请勿将 Telegram token、SMTP 密码、Webhook 密钥、SIM 凭据或其他私密数据存放在仓库中。请通过应用设置或受保护的环境文件来配置它们。

## Telegram 机器人

启用 Telegram 通知并配置好 Chat ID 与 Admin ID 后,机器人支持:

```text
/status [设备]
/esim <设备>
/switch <设备> <iccid>
/wfc <设备> <status|on|off|reconnect>
/sms <设备> <号码> <内容>
/call <设备> <号码> <秒数>
/calls <设备>
/answer <设备>
/hangup <设备>
```

配置文件切换、短信提交与拨号使用一次性确认按钮。定时拨号会执行模组拨号动作,并在 1–600 秒后自动挂断;不会捕获或处理通话音频。机器人不暴露 eSIM 下载、删除或重命名命令。

## 更新

检查是否有更新的 GitHub Release:

```bash
vocat update --check --repo MengMengCode/VoCat
```

安装最新发布版:

```bash
sudo vocat update --repo MengMengCode/VoCat
```

更新器会下载与当前 Linux 架构匹配的二进制,使用已发布的 `SHA256SUMS` 进行校验,原子性地替换可执行文件,并在可用时重启 `vocat` systemd 服务。

Docker 安装的更新方式:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

拉取新镜像后重建容器。

## 开发

依赖要求:

- Go 1.25 或更新版本
- Node.js 20 或更新版本
- npm

运行前端开发服务器:

```bash
cd web
npm install
npm run dev
```

构建嵌入的前端并启动后端:

```bash
cd web
npm run build
cd ..
go run ./cmd/vocat
```

运行全部测试:

```bash
go test ./...
```

构建生产二进制:

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## 发布自动化

推送版本标签会触发两个 GitHub Actions 工作流:

- `release-binaries` 构建并发布 `amd64`、`386`、`arm64` 与 `armv7` 二进制及 `SHA256SUMS`。
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
internal/vowifi/            IKE、EAP-AKA/EAP-AKA'、IMS 与 WiFi Calling 运行时
scripts/install.sh          Linux 安装与更新脚本
web/src/                    React 与 TypeScript 前端
.github/workflows/          二进制与 Docker 发布自动化
```

## 合规使用

蜂窝模组与 eSIM 操作可能影响用户服务、已存储的配置文件、网络注册以及硬件状态。请做好备份,谨慎审视破坏性操作,并仅在您被允许操作所连接的硬件与网络资源的合法环境中使用本软件。

Vocat 不会绕过运营商鉴权、网络策略、硬件安全或 eSIM 信任要求。支持某项操作意味着 Vocat 能够向模组或 eUICC 发起该请求;但设备、配置文件、网络或运营商仍可能拒绝。

## 贡献

欢迎提交 Issue 与 Pull Request。请保持改动聚焦,在可行处附带测试,避免提交凭据或用户数据,并清晰地说明硬件相关行为。

提交改动前:

```bash
go test ./...
cd web && npm run build
```

## 致谢
- [Nodeseek.com](https://www.nodeseek.com) — 专注服务器的社群
- [Linux.do](https://linux.do) — 富有启发的技术社群
- [iniwex5](https://github.com/iniwex5) — 风格与功能指南

## 许可证

参见 [LICENSE](../LICENSE)。
