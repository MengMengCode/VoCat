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

Vocat 是一款面向 Quectel EC20/EC25 系列與中移物聯 ML307 系列行動通訊模組的開源 Web 控制面板與工程工具套件。它在單一自包含的服務中整合了模組探索、即時射頻狀態、AT 與 USSD 終端、簡訊、WiFi Calling（WiFi 通話）、eSIM 管理、網路選擇、代理路由、通知、稽核日誌以及發佈自動化。

後端使用 Go 撰寫，介面採用 React 與 TypeScript 建構，生產環境前端被嵌入進 Go 二進位檔中。單一可執行檔即包含完整的 Web 應用，並使用 SQLite 進行持久化儲存。

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## 功能

| 領域 | Vocat 提供的能力 |
| --- | --- |
| 裝置管理 | 自動序列埠/USB 探索、多模組支援、裝置友善名稱、概覽即時更新、模組重新啟動、飛航模式以及 USB 網路卡模式控制。 |
| 射頻與網路 | 註冊狀態、電信業者、訊號指標、RSRP/RSRQ/SINR、網路模式、頻段、通道、電信業者掃描、自動/手動選網，以及 LTE 小區查詢與單一小區鎖定/解除鎖定（EARFCN/PCI）。 |
| AT 與 USSD | 互動式 AT 終端、指令歷史、原始模組回應、USSD 發起/繼續/取消流程以及清晰的模組錯誤回報。 |
| 簡訊 | 行動通訊與 IMS 簡訊直接傳送、接收同步、分段簡訊處理、送達報告、對話歷史、未讀狀態、時間戳以及逐則訊息的送達狀態。 |
| WiFi Calling | IKEv2/ePDG 隧道建立、EAP-AKA 驗證、IMS 註冊、IMS 簡訊、重新連線控制、狀態診斷以及依裝置路由。 |
| eSIM 與 eUICC | eUICC 探索、EID 與生產資訊、憑證中繼資料、多 eUICC 清單、已安裝設定檔列表、啟用/停用/切換操作，以及在卡片支援時進行下載、重新命名與刪除。 |
| 卡片策略 | 基於 ICCID 的 WiFi Calling 與飛航模式行為，策略即時套用。 |
| 代理路由 | 上游 SOCKS 路由、裝置綁定、國家規則、TCP 可達性檢查以及面向 WiFi Calling 資料路徑的 UDP Associate 檢查。 |
| 通知 | 透過 Telegram、Bark、電子郵件、Pushplus 以及簽章 Webhook 轉發新接收簡訊，每則簡訊個別推送。 |
| Telegram 機器人 | 裝置狀態、已安裝設定檔列表與切換、WiFi Calling 控制以及簡訊傳送。敏感操作需要管理員確認。 |
| 維運 | 密碼與 SSO 驗證（Pocket ID、GitHub、Generic OAuth2）、CSRF 防護、存取策略、稽核事件、即時日誌、日誌保留、健康檢查、響應式版面、深色模式以及中英文應用介面。 |
| 發佈 | 靜態 Linux 二進位檔、systemd 安裝腳本、具 SHA-256 校驗的自我更新、Docker 映像、GHCR 發佈以及 GitHub Actions 發佈建置。 |

## 支援的硬體

Vocat 面向基於高通晶片的 Quectel 模組與中移物聯 ML307 系列模組，這些模組需暴露相容的 AT、QMI、序列埠與 USB 網路介面，包括：

- Quectel EC20
- Quectel EC25
- Quectel EG25 系列
- 相容的 EG600 及相關模組
- 中移物聯 ML307 系列（例如 ML307A-E5，及各 OEM 變體）

可用功能取決於模組韌體、USB 複合裝置配置、SIM/eSIM 能力、主機驅動程式、無線網路以及電信業者配置。

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

此模式刻意賦予 Vocat 對主機裝置和網路堆疊的廣泛存取權限，請僅在受信任的 Linux 主機上使用。自動探索會辨識受支援的 Quectel USB 模組（USB 廠商 ID `2c7c`）、中移物聯 ML307 系列模組（依產品字串辨識），以及透過 Linux WWAN 子系統提供的 PCIe/MHI 模組；它無法辨識任意模組配置。僅以 `--device` 對應個別節點，例如 `/dev/ttyUSB2`、`/dev/cdc-wdm0` 或 `/dev/wwan0qmi0`，會將容器限制在這些固定節點上，無法提供完整的多裝置或熱插拔探索。

在 OpenWrt 主機上，fw4 的 input 鏈會丟棄來自其未管理介面的新連線，其中包括 Vocat 為每個 VoWiFi 工作階段建立的 TUN 介面。因此 Vocat 會使用映像內建的 `nft` 插入範圍很窄的 `vocat-ims-*` 放行規則（僅 P-CSCF 到協商得到的內部 IP 位址）。缺少這些規則時 IMS 註冊與傳送簡訊仍然正常，但接收簡訊與來電會被拒絕。若映像內建的 `nft` 無法讀取主機規則集，Vocat 會記錄 `nft could not inspect the OpenWrt fw4 input chain`；此時可讓 `VOCAT_NFT` 指向主機自帶的 nft：

```bash
cat > /opt/vocat-host-nft <<'EOF'
#!/bin/sh
exec /host/lib/ld-musl-$(uname -m).so.1 --library-path /host/usr/lib:/host/lib /host/usr/sbin/nft "$@"
EOF
chmod 755 /opt/vocat-host-nft
# 在上面的 docker run 指令中加入：
#   -v /usr:/host/usr:ro -v /lib:/host/lib:ro \
#   -v /opt/vocat-host-nft:/usr/local/bin/host-nft:ro \
#   -e VOCAT_NFT=/usr/local/bin/host-nft
```

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

### AT 行動通訊主機資料

基於 AT 的 ECM/RNDIS 資料需要 Linux、iproute2 以及帶有 `udhcpc` 的 BusyBox。
VoCat 必須獨佔管理模組網路介面；請停用該介面上競爭的 DHCP 用戶端或網路管理設定檔。VoCat 會維護 IPv4 租約與標記策略路由，而不會變更系統預設路由或 DNS 解析設定。該路徑目前需要支援 IPv4 的行動通訊服務。

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
| `VOCAT_MCP_ENABLED` | `true` | 在 `/mcp` 提供面向 AI Agent 的 MCP 端點。設為 `false` 可停用。 |
| `VOCAT_PROBE_UNKNOWN_USB` | `false` | 允許使用 AT 指令探測未識別的 USB 序列埠裝置。探測會變更序列埠設定並清空輸入快取；僅當這些連接埠專用於模組時才啟用。已驗證的模組身分會自動被探索。JSON 鍵名：`probe_unknown_usb`。 |
| `VOCAT_REPO` | `MengMengCode/VoCat` | 自我更新器使用的受信任 GitHub 倉庫，格式為 `owner/name`。 |
| `GITHUB_TOKEN` | 空 | 可選的 GitHub token，用於私有倉庫或更高的 API 限額。 |

使用者提供的 Apple 電信業者設定套件可透過 `vocat carrier import-ipcc` 轉換為可供審查、符合允許清單的電信業者設定檔；請參閱 [docs/CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md)。

管理員認證資訊僅儲存在 SQLite 中。使用 `vocat bootstrap-admin` 對空資料庫執行一次初始化；環境變數和 JSON 設定無法設定或覆寫管理員使用者名稱或密碼。

請勿將 Telegram token、SMTP 密碼、Webhook 金鑰、SIM 認證資訊或其他私密資料存放在倉庫中。請透過應用設定或受保護的環境檔來配置它們。

## 單一登入（SSO）

VoCat 支援 Pocket ID（OpenID Connect）、GitHub.com OAuth App 和 Generic OAuth2，可在**設定 → 單一登入（SSO）**中選擇其中一個識別提供者。SSO 預設停用，密碼登入始終可用。請先使用本地管理員登入、儲存設定並明確綁定外部帳號，再啟用 SSO。未綁定的外部帳號無法註冊或成為管理員；即使共用已綁定帳號的電子郵件地址也不會自動合併。

### Pocket ID

1. 為 VoCat 設定 HTTPS 根路徑公開來源，例如 `https://vocat.example.com`，並設定 `VOCAT_SECURE_COOKIES=true`。在 SSO 設定中選擇 **Pocket ID**。公開來源初始使用目前瀏覽器存取位址，需要調整時直接編輯**VoCat 公開來源**。頁面會立即顯示簡潔的回呼 URL 與複製按鈕，無需先建立 OIDC 用戶端或儲存憑據即可複製：`https://vocat.example.com/api/auth/sso/callback`。
2. 在 Pocket ID 為 VoCat 個別建立 **Confidential** OIDC 用戶端。登記精確的回呼 URL，並確認目標使用者或群組獲准使用該用戶端。VoCat 與 Komari 應分別建立用戶端並簽發本地工作階段。
3. 在 VoCat 輸入 Pocket ID 精確的公開 `APP_URL` 作為簽發者（Issuer），並填入步驟 2 建立的用戶端之 Client ID 與 Client Secret。Client ID 欄位包含何處尋找該數值的提示。Scopes 與 PKCE S256 由 VoCat 自動處理。權杖客戶端驗證根據已驗證的 Discovery 中繼資料自動選擇，優先使用 `client_secret_basic`，其次為 `client_secret_post`。在 SSO 保持停用的狀態下儲存。修改公開來源會立即更新顯示的回呼位址，儲存後方用於實際授權。
4. 檢查已儲存的設定；成功時顯示**設定檢查通過**。接著發起帳號綁定，完成 Pocket ID 的 Passkey 登入與授權同意，並在 VoCat 中確認傳回的綁定。設定檢查僅驗證中繼資料與連線能力；在實際登入前無法確認 Client Secret 或使用者授權。
5. 啟用 SSO 並儲存。使用無痕視窗驗證 SSO 登入、返回請求頁面以及登入後的第一次狀態變更操作。

設定頁面顯示已綁定帳號與解除綁定操作。若要更換外部帳號，請先解除綁定，再綁定目標帳號並重新啟用 SSO。

Issuer 會精確比對，包含其路徑與結尾斜線。Pocket ID 登入使用經過驗證的 ID Token 之 `issuer + sub` 關聯帳號，不依賴 UserInfo 端點或電子郵件聲明。Discovery 及其授權、Token 與 JWKS 端點必須與設定的 Issuer 同來源（Origin）。首期不支援使用 `INTERNAL_APP_URL` 搭配不同後端來源的部署方式。Issuer 請維持填寫公開 `APP_URL`。儲存時會保留現有已明確設定的驗證方法與 Scopes。

### GitHub 與 Generic OAuth2

GitHub：在 GitHub.com 建立 OAuth App 並登記完全相同的回呼 URL，在 VoCat 選擇 **GitHub**，填入 Client ID 與 Secret，並依序執行上述的儲存、檢查、綁定、啟用步驟。端點固定為 GitHub.com，預設 scope 為 `read:user`，且強制要求 PKCE S256。綁定採用穩定的 GitHub 數值帳號 ID，因此修改使用者名稱不會變更綁定身份。GitHub Enterprise 不在此版本支援範圍內。

Generic OAuth2：除了用戶端憑據外，還需提供授權、Token 與 UserInfo URL。選取 Generic OAuth2 時會直接顯示 Scopes、頂層使用者 ID 欄位名稱（預設 `id`）、Token 驗證與 PKCE 相容性選項。UserInfo 透過 Bearer 存取權杖請求；使用者 ID 必須為非空字串或整數，大整數會保留精確數值。預設 Token 驗證為 `client_secret_post`，預設 PKCE 為 S256。僅針對不支援 PKCE 的提供者才關閉 PKCE。Generic OAuth2 不執行 OIDC ID Token 驗證。

### 網路、憑證與工作階段

VoCat 現有的來源網路策略同樣適用於 SSO 路由。設定 SSO 並不會使安裝實例公開對外開放。瀏覽器必須能連線至 VoCat 與授權端點；VoCat 處理程序必須能連線至 Discovery、Token 與 JWKS/UserInfo 端點並信任其 TLS 憑證。儲存精確的 Pocket ID HTTPS Issuer 或 Generic 各端點 URL 即代表核准這些位址進行 HTTPS 連線，包括 RFC1918/IPv6 ULA 私有網路位址。GitHub 的固定端點仍僅限於公開位址。

針對 Generic 提供者的內部 HTTP 服務，請在 Token/UserInfo 欄位明確填寫完整的 HTTP URL。這些 HTTP 目的地的每次 DNS 解析候選與實際連線都必須為獲准的私有位址。Pocket ID 的 Discovery 無法自動核准其他來源；其端點必須使用所設定的 Issuer 來源。Issuer、Discovery 擷取 URL 與瀏覽器授權端點始終需要 HTTPS。IdP 連線仍拒絕 loopback、link-local、雲端中繼資料與其他特殊用途位址。私有 CA 憑證應安裝至主機/容器的信任庫中；本系統不提供略過 TLS 驗證的選項。瀏覽器存取 VoCat 使用的 localhost 或 HTTPS 來源與這些 IdP 連線權限相互獨立。

提供的 Compose 部署使用 host 網路。請在實際容器內驗證 DNS、路由與 CA 信任；請勿預設橋接網路的服務名稱可用。反向代理必須保留所設定的公開來源。VoCat 不會從轉發的 Host 標頭推導回呼 URL。必須採用根路徑部署。本機 Vite 開發時請統一使用一致的 loopback 來源（例如 `http://127.0.0.1:5173`）作為公開來源與回呼登記位址；IdP 傳輸例外僅限於測試 fixture 中使用。

工作階段與 CSRF Cookie 維持 `SameSite=Strict`；短期授權 Cookie 使用 `SameSite=Lax` 以支援最頂層 GET 跨站回呼。儲存任何 SSO 設定（包括停用 SSO 或輪換 Secret）均會撤銷所有現有 SSO 工作階段。解除綁定帳號會撤銷該綁定的 SSO 工作階段。密碼工作階段與 API 權杖在上述操作後仍會保留。變更本機管理員密碼會撤銷所有工作階段與 API 權杖、取消進行中的授權，並保留外部綁定。當操作成功並撤銷目前 SSO 工作階段時，介面會提示重新登入。

Secret 輸入欄位留空代表保留原值，輸入新 Secret 則會替換原值。設定頁面不提供清除已儲存 Secret 的控制項。變更提供者、Client ID、Issuer 或 Generic 身分端點/ID 欄位時，會切換身分識別命名空間，需要填寫新憑據並重新綁定；既有綁定需要明確確認重設。若要更換綁定帳號，請先解除綁定再建立新綁定。Client Secret 僅儲存在伺服器受保護的 SQLite 資料庫中；敏感設定標誌僅用於介面回應脫敏，不代表資料庫加密。第三方權杖不會被持久化儲存，也不會傳回瀏覽器。

一般登出僅結束目前的 VoCat 工作階段。在 IdP 停用帳號或登出 Pocket ID 不會立即撤銷既有的 VoCat 工作階段；它會依據 `VOCAT_SESSION_TTL` 到期，亦可在 VoCat 中停用 SSO 或解除綁定以立即撤銷。公開功能端點為 `GET /api/auth/providers`。SSO 設定與綁定 API 寫入操作需要瀏覽器工作階段與 CSRF；Bearer 權杖無法管理這些憑據。設定版本衝突會傳回 `409 sso_config_changed`；身分來源變更需要重新綁定時傳回 `409 sso_rebind_required`。

### 升級與復原

SSO 新增 SQLite schema 版本 29。升級會保留原管理員、工作階段、API 權杖與裝置資料；SSO 在設定前仍保持停用。升級前請記錄舊二進位檔或映像摘要，並在 VoCat 停止時備份設定、CA 資料與完整的持久化資料目錄；線上備份應使用 SQLite 的 backup API 或 `.backup`。在 WAL 模式下，僅複製執行中的 `.db` 檔案並非可靠的備份方式。

若新版本設定發生問題，可先停用 SSO，以繼續使用密碼登入並保留目前業務資料。若要退回僅支援 schema 28 的舊二進位檔，必須停止 VoCat 並還原對應的升級前資料與設定快照。僅復原容器映像或手動降低 `PRAGMA user_version` 無法降級資料庫。建議在可行情況下還原至獨立目錄/磁碟區，並保留新資料庫以供調查。還原快照會遺失備份後收到的簡訊與任務變更。若使用 Docker，請透過拉取映像並重建容器來更新。

在生產發佈前，應在實際部署環境中完成 Pocket ID Passkey/存取策略整合測試，以及升級與復原演練。

## API 權杖與 MCP

非瀏覽器用戶端可以使用 Bearer 權杖向相同的 `/api/...` 端點進行驗證，無需使用瀏覽器的工作階段 Cookie 與 CSRF 流程：

```bash
vocat token create --read-only --ttl 720h ci-runner   # 僅列印一次權杖
curl -H "Authorization: Bearer vcat_..." http://127.0.0.1:7575/api/devices
```

`vocat token list` 與 `vocat token revoke <name>` 用於管理權杖清單。權杖具有管理員權限；`--read-only` 權杖僅能發起 `GET` 請求（一個經文件記錄的例外：讀取簡訊對話會在本地將其標記為已讀——規則是「不產生硬體或網路變更」）。輪換管理員密碼會撤銷所有權杖。請在首次執行 `bootstrap-admin` 後建立權杖。遠端 Bearer 用戶端還需要其來源位址在網路存取策略許可範圍內；預設的 `internal` 模式僅允許私有網路。

預設情況下（設定 `VOCAT_MCP_ENABLED=false` 可停用），同一服務會在 `http://<host>:7575/mcp` 為 AI Agent 提供一個基於 Streamable HTTP 傳輸與 Bearer 權杖驗證的 MCP（Model Context Protocol）端點。提供的工具涵蓋裝置狀態、簡訊對話及受保護的傳送、eSIM 設定檔列表與切換，以及 WiFi Calling 狀態/重新連線；敏感操作（原始 AT、eSIM 下載/刪除、SIM PIN、網路選擇、系統更新）未暴露，且工具輸出中絕不包含 IMSI、SIM PIN 或啟用碼。唯讀權杖取得的工具列表中不包含寫入操作工具。簡訊傳送遵循與 Web 介面相同的受阻目的地策略與全域每小時上限，並經由行動通訊 AT 路徑傳送（Web 介面在啟用 WiFi Calling 時的 IMS 優先路由仍保留在 Web 端，且透過 MCP 傳送的簡訊暫未寫入 Web 對話歷史）。

用戶端設定範例（以 Claude Code 為例）：

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

設計記錄請參閱 [docs/API_MCP_DEVELOPMENT_PLAN.md](API_MCP_DEVELOPMENT_PLAN.md)。

## Telegram 機器人

啟用 Telegram 通知並配置好 Chat ID 與 Admin ID 後，機器人支援：

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

設定檔切換與簡訊提交使用一次性確認按鈕。機器人不暴露 eSIM 下載、刪除或重新命名命令。

網頁終端與 Telegram 共用 USSD 路由。啟用 WiFi Calling 時，請求使用 IMS/USSI；IMS 未就緒時會明確回報錯誤並提示等待註冊，不會回退到行動通訊 AT+CUSD。關閉 WiFi Calling 時使用行動通訊 USSD。選單回覆與取消始終綁定原裝置與對話。取消失敗會回報錯誤並保留工作階段以供重試；傳送結果不確定時不會自動換通道重新提交。

USSI 對話處理遵循 [3GPP TS 24.390](https://www.etsi.org/deliver/etsi_ts/124300_124399/124390/18.00.00_60/ts_124390v180000p.pdf)。BYE 失敗時保留取消記錄，但開始取消後禁止繼續提交選單輸入（[RFC 3261 第 15.1.1 節](https://www.rfc-editor.org/rfc/rfc3261.html#section-15.1.1)）。

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

## Star History

<a href="https://www.star-history.com/?repos=mengmengcode%2Fvocat&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
 </picture>
</a>
