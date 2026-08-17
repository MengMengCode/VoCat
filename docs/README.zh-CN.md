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

[English](../README.md) | [العربية](README.ar.md) | **简体中文** | [繁體中文](README.zh-TW.md) | [Français](README.fr.md) | [Русский](README.ru.md) | [Español](README.es.md) | [日本語](README.ja.md)

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
| WiFi Calling | IKEv2/ePDG 隧道建立、EAP-AKA 鉴权、IMS 注册、IMS 短信、重连控制、状态诊断以及按设备路由。 |
| eSIM 与 eUICC | eUICC 发现、EID 与生产信息、证书元数据、多 eUICC 清单、已安装配置文件列表、启用/禁用/切换操作,以及在卡片支持时进行下载、重命名和删除。 |
| 卡策略 | 基于 ICCID 的 WiFi Calling 与飞行模式行为,策略即时应用。 |
| 代理路由 | 上游 SOCKS 路由、设备绑定、国家规则、TCP 可达性检查以及面向 WiFi Calling 数据路径的 UDP Associate 检查。 |
| 通知 | 通过 Telegram、Bark、邮件、Pushplus 以及签名 Webhook 转发新入站短信,每条短信单独推送。 |
| Telegram 机器人 | 设备状态、已安装配置文件列表与切换、WiFi Calling 控制以及短信发送。敏感操作需要管理员确认。 |
| 运维 | 鉴权、CSRF 防护、访问策略、审计事件、实时日志、日志留存、健康检查、响应式布局、深色模式以及中英文应用界面。 |
| 分发 | 静态 Linux 二进制、systemd 安装脚本、带 SHA-256 校验的自更新、Docker 镜像、GHCR 发布以及 GitHub Actions 发布构建。 |

## 支持的硬件

Vocat 面向基于高通芯片、并暴露兼容 AT、QMI、串口与 USB 网络接口的 Quectel 模组,包括:

- Quectel EC20
- Quectel EC25
- Quectel EG25 系列
- 兼容的 EG600 及相关模组

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

安装指定版本:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

VoWiFi IMS 必须使用 Linux XFRM/IPsec。OpenWrt/Kwrt 上安装脚本会从当前固件自己的软件源尝试安装严格匹配的 `ip-full`、`kmod-ipsec`、`kmod-ipsec4/6`、`kmod-crypto-authenc`、AES-CBC 和 SHA1 组件。若软件源没有与当前内核匹配的模块，必须更换包含这些组件的固件，禁止强装其他内核版本的 kmod。

安装程序会:

- 检测 `amd64`、`386`、`arm64` 或 `armv7` 架构;
- 下载对应的 GitHub Release 二进制;
- 对照 `SHA256SUMS` 进行校验;
- 将 Vocat 安装到 `/opt/vocat`;
- 创建具有 Vocat 所需硬件与网络访问权限的强化版 systemd 服务;
- 将运行时配置存放在 `/etc/vocat/env`;
- 首次安装时生成随机初始管理员密码。

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

校验并安装:

```bash
sha256sum -c SHA256SUMS --ignore-missing
sudo install -d -m 0755 /opt/vocat/bin /opt/vocat/data
sudo install -m 0755 vocat-linux-amd64 /opt/vocat/bin/vocat
read -rsp "管理员密码: " VOCAT_BOOTSTRAP_PASSWORD; echo
printf '%s\n' "$VOCAT_BOOTSTRAP_PASSWORD" | sudo /opt/vocat/bin/vocat bootstrap-admin
unset VOCAT_BOOTSTRAP_PASSWORD
sudo env \
  VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db \
  /opt/vocat/bin/vocat serve
```

该手动命令会在前台运行 Vocat。请使用 `vocat serve` 以直接启动服务器；在 TTY 下以 root 运行无参数的 `vocat` 会进入交互式管理菜单。如需托管的 systemd 服务与自动重启,请使用一键安装脚本。

### Docker

如果 Linux 主机需要发现每一个接入的受支持 Quectel 模组,并持续感知 USB 热插拔事件,请以硬件访问模式运行 Vocat:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest

read -rsp "管理员密码: " VOCAT_BOOTSTRAP_PASSWORD; echo
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

容器启动后打开 `http://<服务器地址>:7575`。主机网络是必需的,这样 QMI 网络接口才能对 Vocat 可见;而特权设备访问是串口、QMI 控制节点、TUN 接口、网络配置以及容器启动后新增设备所必需的。`/dev` 挂载使新的 `ttyUSB*`、`ttyACM*` 和 `cdc-wdm*` 节点无需重建容器即可见。

该模式有意赋予 Vocat 对主机设备与网络栈的广泛访问权限,仅在受信任的 Linux 主机上使用。自动发现目前仅识别受支持的 Quectel USB 模组(USB 厂商 ID `2c7c`),不识别任意品牌的模组。仅用 `--device` 映射单个节点(例如 `/dev/ttyUSB2` 与 `/dev/cdc-wdm0`)会将容器限定在这些固定节点上,无法提供完整的多设备或热插拔发现。

GHCR 镜像发布为 `linux/amd64` 与 `linux/arm64`。

### USB SIM 读卡器

USB SIM 读卡器通过 Linux PC/SC 服务访问。一键安装脚本会在支持的软件包管理器上
自动安装并启动 `pcscd` 和 CCID 驱动；Debian/Ubuntu 手动安装命令为
`apt install pcscd libccid`。如果 USB 已识别 CCID 读卡器但 PC/SC 尚未就绪，
VoCat 会继续在添加设备窗口显示该硬件，并明确提示缺少服务或驱动，不再静默隐藏。

## 配置

Vocat 先从 `VOCAT_CONFIG` 读取可选的 JSON 配置文件,再应用 `VOCAT_*` 环境变量。环境变量优先级更高。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | HTTP 监听地址。 |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | SQLite 数据库路径。 |
| `VOCAT_SESSION_TTL` | `24h` | 鉴权会话有效期。 |
| `VOCAT_SECURE_COOKIES` | `false` | 在使用 HTTPS 时将会话 Cookie 标记为安全。 |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | 优雅关闭超时时间。 |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | API 请求体最大字节数。 |
| `VOCAT_REPO` | `MengMengCode/VoCat` | 自更新器使用的受信任 GitHub 仓库，格式为 `owner/name`。 |
| `GITHUB_TOKEN` | 空 | 可选的 GitHub token,用于私有仓库或更高的 API 限额。 |

管理员账号和密码只保存在 SQLite 数据库中。空数据库需要执行一次
`vocat bootstrap-admin` 完成初始化；环境变量和 JSON 配置都不能设置或覆盖管理员凭据。

请勿将 Telegram token、SMTP 密码、Webhook 密钥、SIM 凭据或其他私密数据存放在仓库中。请通过应用设置或受保护的环境文件来配置它们。

## Apple IPCC 运营商规则导入

VoCat 可以离线解析用户提供的 `.ipcc`，将 Apple 的 XML/二进制 plist
转换为可审查的运营商 Profile。默认只预览，不会修改配置：

```bash
vocat carrier import-ipcc Carrier_iPhone.ipcc
```

确认警告和匹配范围后，使用 `--install` 安装；重启 VoCat 后生效：

```bash
vocat carrier import-ipcc --install Carrier_iPhone.ipcc
```

导入器不会复制关闭证书验证、绕过运营商授权、APN 凭据、紧急呼叫或
设备型号专属媒体参数。完整字段和冲突处理说明见
[CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md)。

## Telegram 机器人

启用 Telegram 通知并配置好 Chat ID 与 Admin ID 后,机器人支持:

```text
/status [设备]
/esim <设备>
/switch <设备> <iccid>
/wfc <设备> <status|on|off|reconnect>
/sms <设备> <号码> <内容>
```

配置文件切换与短信提交使用一次性确认按钮。机器人不暴露 eSIM 下载、删除或重命名命令。

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
internal/vowifi/            IKE、EAP-AKA、IMS 与 WiFi Calling 运行时
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
