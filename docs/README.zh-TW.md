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

[English](../README.md) | [العربية](README.ar.md) | [简体中文](README.zh-CN.md) | **繁體中文** | [Français](README.fr.md) | [Русский](README.ru.md) | [Español](README.es.md) | [日本語](README.ja.md)

Vocat 是一款面向 Quectel EC20/EC25 系列行動通訊模組的開源 Web 控制面板與工程工具套件。它在單一自包含的服務中整合了模組探索、即時射頻狀態、AT 與 USSD 終端、簡訊、WiFi Calling（WiFi 通話）、eSIM 管理、網路選擇、代理路由、通知、稽核日誌以及發佈自動化。

後端使用 Go 撰寫，介面採用 React 與 TypeScript 建構，生產環境前端被嵌入進 Go 二進位檔中。單一可執行檔即包含完整的 Web 應用，並使用 SQLite 進行持久化儲存。

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## 功能

| 領域 | Vocat 提供的能力 |
| --- | --- |
| 裝置管理 | 自動序列埠/USB 探索、多模組支援、裝置友善名稱、概覽即時更新、模組重新啟動、飛航模式以及 USB 網路卡模式控制。 |
| 射頻與網路 | 註冊狀態、電信業者、訊號指標、RSRP/RSRQ/SINR、網路模式、頻段、通道、電信業者掃描以及自動/手動選網。 |
| AT 與 USSD | 互動式 AT 終端、指令歷史、原始模組回應、USSD 發起/繼續/取消流程以及清晰的模組錯誤回報。 |
| 簡訊 | 行動通訊與 IMS 簡訊直接傳送、接收同步、分段簡訊處理、送達報告、對話歷史、未讀狀態、時間戳以及逐則訊息的送達狀態。 |
| WiFi Calling | IKEv2/ePDG 隧道建立、EAP-AKA 驗證、IMS 註冊、IMS 簡訊、重新連線控制、狀態診斷以及依裝置路由。 |
| eSIM 與 eUICC | eUICC 探索、EID 與生產資訊、憑證中繼資料、多 eUICC 清單、已安裝設定檔列表、啟用/停用/切換操作，以及在卡片支援時進行下載、重新命名與刪除。 |
| 卡片策略 | 基於 ICCID 的 WiFi Calling 與飛航模式行為，策略即時套用。 |
| 代理路由 | 上游 SOCKS 路由、裝置綁定、國家規則、TCP 可達性檢查以及面向 WiFi Calling 資料路徑的 UDP Associate 檢查。 |
| 通知 | 透過 Telegram、Bark、電子郵件、Pushplus 以及簽章 Webhook 轉發新接收簡訊，每則簡訊個別推送。 |
| Telegram 機器人 | 裝置狀態、已安裝設定檔列表與切換、WiFi Calling 控制以及簡訊傳送。敏感操作需要管理員確認。 |
| 維運 | 驗證、CSRF 防護、存取策略、稽核事件、即時日誌、日誌保留、健康檢查、響應式版面、深色模式以及中英文應用介面。 |
| 發佈 | 靜態 Linux 二進位檔、systemd 安裝腳本、具 SHA-256 校驗的自我更新、Docker 映像、GHCR 發佈以及 GitHub Actions 發佈建置。 |

## 支援的硬體

Vocat 面向基於高通晶片、並暴露相容 AT、QMI、序列埠與 USB 網路介面的 Quectel 模組，包括：

- Quectel EC20
- Quectel EC25
- Quectel EG25 系列
- 相容的 EG600 及相關模組

可用功能取決於模組韌體、USB 複合裝置配置、SIM/eSIM 能力、主機驅動、無線網路以及電信業者配置。

## 安裝

### Linux 一鍵安裝

已是 root（包括預設沒有 `sudo` 的 OpenWrt/Kwrt）：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash
```

一般 Linux 使用者且系統裝有 sudo：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

只檢查 VoWiFi/XFRM 環境，不安裝 VoCat：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash -s -- --check-env
```

安裝指定版本：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

VoWiFi IMS 必須使用 Linux XFRM/IPsec。OpenWrt/Kwrt 上安裝腳本會從目前韌體自己的軟體源嘗試安裝嚴格匹配的 `ip-full`、`kmod-ipsec`、`kmod-ipsec4/6`、`kmod-crypto-authenc`、AES-CBC 和 SHA1 元件。若軟體源沒有與目前核心匹配的模組，必須更換包含這些元件的韌體，禁止強裝其他核心版本的 kmod。

如果核心無法提供 XFRM/IPsec，而你只需要行動通訊簡訊或數據等非 VoWiFi 功能，可使用 `--skip-vowifi-check` 安裝：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh --skip-vowifi-check
```

安裝程式會：

- 偵測 `amd64`、`386`、`arm64`、`aarch64` 或 `armv7` 架構；
- 下載對應的 GitHub Release 二進位檔；
- 對照 `SHA256SUMS` 進行校驗；
- 將 Vocat 安裝到 `/opt/vocat`；
- 建立具有 Vocat 所需硬體與網路存取權限的強化版 systemd 服務；
- 將執行時配置存放在 `/etc/vocat/env`；
- 首次安裝時產生隨機初始管理員密碼。

安裝完成後開啟：

```text
http://<server-address>:7575
```

### 手動二進位安裝

從 GitHub Releases 下載對應的二進位檔與 `SHA256SUMS`：

| 平台 | 發佈檔案 |
| --- | --- |
| Linux x86-64 | `vocat-linux-amd64` |
| Linux x86 32 位元 | `vocat-linux-386` |
| Linux ARM64 | `vocat-linux-arm64` |
| Linux AArch64 | `vocat-linux-aarch64` |
| Linux ARMv7 | `vocat-linux-armv7` |

校驗並安裝：

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

該手動指令會在前台執行 Vocat。請使用 `vocat serve` 以直接啟動伺服器；在 TTY 下以 root 執行無參數的 `vocat` 會進入互動式管理選單。如需託管的 systemd 服務與自動重新啟動，請使用一鍵安裝腳本。

### Docker

如果 Linux 主機需要探索每一個接入的受支援 Quectel 模組，並持續感知 USB 熱插拔事件，請以硬體存取模式執行 Vocat：

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

容器啟動後開啟 `http://<server-address>:7575`。必須使用主機網路，才能讓 QMI 網路介面持續對 Vocat 可見；序列埠、QMI 控制節點、TUN 介面、網路設定以及容器啟動後新增的裝置則需要特權裝置存取。`/dev` 繫結掛載使新的 `ttyUSB*`、`ttyACM*`、`cdc-wdm*` 和 MHI `wwan*` 節點無需重建容器即可見。

此模式刻意賦予 Vocat 對主機裝置和網路堆疊的廣泛存取權限，請僅在受信任的 Linux 主機上使用。自動探索會辨識受支援的 Quectel USB 模組（USB 廠商 ID `2c7c`），以及透過 Linux WWAN 子系統提供的 PCIe/MHI 模組；它無法辨識任意模組配置。僅以 `--device` 對應個別節點，例如 `/dev/ttyUSB2`、`/dev/cdc-wdm0` 或 `/dev/wwan0qmi0`，會將容器限制在這些固定節點上，無法提供完整的多裝置或熱插拔探索。

GHCR 映像發佈為 `linux/amd64` 與 `linux/arm64`。

> [!TIP]
> **NAS / QNAP Container Station 部署說明**：
> 在 QNAP QTS / QuTS hero（Container Station）等 NAS 作業系統上，自訂的非 root 管理員帳號和磁碟區隔離機制可能導致 Docker 具名磁碟區（例如 `-v vocat-data:/opt/vocat/data`）在一次性的 `bootstrap-admin` 初始化和背景服務容器中解析為不同的隔離路徑，從而在 Web 登入時出現「密碼錯誤」。
> 在 NAS 環境中，強烈建議初始化和執行時均以主機絕對路徑的繫結掛載取代具名磁碟區（例如 QNAP 上的 `-v /share/Container/vocat/data:/opt/vocat/data`），以確保 SQLite 資料庫持久化儲存的一致性。

### USB SIM 讀卡機

USB SIM 讀卡機使用 Linux PC/SC 服務。一鍵安裝程式會在支援的套件管理員上自動安裝並啟動 `pcscd` 及 CCID 驅動程式。Debian/Ubuntu 上等效的手動安裝指令為 `apt install pcscd libccid`。如果 USB 已辨識 CCID 讀卡機，但 PC/SC 無法使用，VoCat 會繼續在新增裝置對話方塊中顯示該讀卡機，並回報缺少的服務或驅動程式，而不是悄悄將它隱藏。

### QMI 命令列工具

VoCat 使用 `qmicli` 驗證 QMI 控制通道是否就緒，並使用 `qmi-proxy` 多工存取該通道。封包數據連線階段由內建 QMI WDS 用戶端管理，而非使用 `qmi-network` 的 CID/PDH 狀態檔。一鍵安裝程式會安裝並驗證對應工具。手動部署時，Debian/Ubuntu 使用 `apt install libqmi-utils`；Arch Linux 使用 `pacman -S libqmi`，Alpine 使用 `apk add qmi-utils`，OpenWrt 使用 `opkg install qmi-utils`。

`vocat doctor --repair-dji-qmi` 會在變更任何 USB 驅動程式繫結或將 DTR 設為有效狀態之前檢查 `qmicli`。如果該工具無法使用，指令會停止並提供安裝提示，保持裝置目前狀態不變。

## 配置

Vocat 先從 `VOCAT_CONFIG` 讀取可選的 JSON 配置檔，再套用 `VOCAT_*` 環境變數。環境變數優先級更高。

| 環境變數 | 預設值 | 說明 |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | HTTP 監聽位址。 |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | SQLite 資料庫路徑。 |
| `VOCAT_SESSION_TTL` | `24h` | 驗證工作階段有效期。 |
| `VOCAT_SECURE_COOKIES` | `false` | 在使用 HTTPS 時將工作階段 Cookie 標記為安全。 |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | 優雅關閉逾時時間。 |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | API 請求主體最大位元組數。 |
| `VOCAT_REPO` | `MengMengCode/VoCat` | 自我更新器使用的受信任 GitHub 倉庫，格式為 `owner/name`。 |
| `GITHUB_TOKEN` | 空 | 可選的 GitHub token，用於私有倉庫或更高的 API 限額。 |

使用者提供的 Apple 電信業者設定套件可透過 `vocat carrier import-ipcc` 轉換為可供審查、符合允許清單的電信業者設定檔；請參閱 [docs/CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md)。

管理員認證資訊僅儲存在 SQLite 中。使用 `vocat bootstrap-admin` 對空資料庫執行一次初始化；環境變數和 JSON 設定無法設定或覆寫管理員使用者名稱或密碼。

請勿將 Telegram token、SMTP 密碼、Webhook 金鑰、SIM 認證資訊或其他私密資料存放在倉庫中。請透過應用設定或受保護的環境檔來配置它們。

## Telegram 機器人

啟用 Telegram 通知並配置好 Chat ID 與 Admin ID 後，機器人支援：

```text
/status [device]
/esim <device>
/switch <device> <iccid>
/wfc <device> <status|on|off|reconnect>
/sms <device> <number> <message>
```

設定檔切換與簡訊提交使用一次性確認按鈕。機器人不暴露 eSIM 下載、刪除或重新命名命令。

## 更新

檢查是否有更新的 GitHub Release：

```bash
vocat update --check --repo MengMengCode/VoCat
```

安裝最新發佈版：

```bash
sudo vocat update --repo MengMengCode/VoCat
```

更新器會下載與目前 Linux 架構匹配的二進位檔，使用已發佈的 `SHA256SUMS` 進行校驗，原子性地替換可執行檔，並在可用時重新啟動 `vocat` systemd 服務。

Docker 安裝的更新方式：

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

拉取新映像後重建容器。

## 開發

依賴要求：

- Go 1.25 或更新版本
- Node.js 20 或更新版本
- npm

執行前端開發伺服器：

```bash
cd web
npm install
npm run dev
```

建構嵌入的前端並啟動後端：

```bash
cd web
npm run build
cd ..
go run ./cmd/vocat
```

執行全部測試：

```bash
go test ./...
```

建構生產二進位檔：

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## 發佈自動化

推送版本標籤會觸發兩個 GitHub Actions 工作流程：

- `release-binaries` 建構並發佈 `amd64`、`386`、`arm64`、`aarch64` 與 `armv7` 二進位檔及 `SHA256SUMS`。
- `docker` 建構並向 GitHub Container Registry 發佈多架構映像。

```bash
git tag v0.2.0
git push origin v0.2.0
```

## 專案結構

```text
cmd/vocat/                  應用入口與 CLI
internal/device/            模組探索與裝置控制
internal/modem/             AT 工作階段與回應處理
internal/server/            HTTP API、通知與內嵌 Web 伺服器
internal/store/             SQLite 持久化
internal/update/            GitHub Release 自我更新器
internal/vowifi/            IKE、EAP-AKA、IMS 與 WiFi Calling 執行時
scripts/install.sh          Linux 安裝與更新腳本
web/src/                    React 與 TypeScript 前端
.github/workflows/          二進位檔與 Docker 發佈自動化
```

## 負責任地使用

行動通訊模組與 eSIM 操作可能影響用戶服務、已儲存的設定檔、網路註冊以及硬體狀態。請做好備份，謹慎審視破壞性操作，並僅在您被允許操作所連接的硬體與網路資源的合法環境中使用本軟體。

Vocat 不會繞過電信業者驗證、網路策略、硬體安全或 eSIM 信任要求。支援某項操作意味著 Vocat 能夠向模組或 eUICC 發起該請求；但裝置、設定檔、網路或電信業者仍可能拒絕。

## 貢獻

歡迎提交 Issue 與 Pull Request。請保持改動聚焦，在可行處附帶測試，避免提交認證資訊或用戶資料，並清晰地說明硬體相關行為。

提交改動前：

```bash
go test ./...
cd web && npm run build
```

## 致謝
- [Nodeseek.com](https://www.nodeseek.com) — 專注伺服器的社群
- [Linux.do](https://linux.do) — 富有啟發的技術社群
- [iniwex5](https://github.com/iniwex5) — 風格與功能指南

## 請我喝杯咖啡

| 網路 | 位址 |
| ------- | ------- |
| USDT-TRON (TRC20) | `TWSAkvzVsFc7KqncDLmUfRxpPQbpV5CgTB` |
| USDT-BSC (BEP20) | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |
| USDT-Polygon | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |

## 授權條款

參見 [LICENSE](../LICENSE)。

<a href="https://star-history.dera.page/#MengMengCode/VoCat">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://star-history.dera.page/svg?repos=MengMengCode/VoCat&theme=dark" />
   <source media="(prefers-color-scheme: light)" srcset="https://star-history.dera.page/svg?repos=MengMengCode/VoCat" />
   <img alt="Star History Chart" src="https://star-history.dera.page/svg?repos=MengMengCode/VoCat" />
 </picture>
</a>
