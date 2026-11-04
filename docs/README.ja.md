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

[English](../README.md) | [العربية](README.ar.md) | [简体中文](README.zh-CN.md) | [繁體中文](README.zh-TW.md) | [Français](README.fr.md) | [Русский](README.ru.md) | [Español](README.es.md) | **日本語**

Vocat は、Quectel EC20/EC25 クラスおよび China Mobile ML307 シリーズのセルラーモデム向けのオープンソース Web コントロールパネル兼エンジニアリングツールキットです。モデムの検出、リアルタイムの無線ステータス、AT / USSD ターミナル、SMS、WiFi Calling、eSIM 管理、ネットワーク選択、プロキシルーティング、通知、監査ログ、リリース自動化を、自己完結型の単一サービスに統合しています。

バックエンドは Go で書かれ、インターフェースは React と TypeScript で構築され、本番フロントエンドは Go バイナリに埋め込まれています。単一の実行ファイルに Web アプリケーション全体が含まれ、永続的な状態には SQLite を使用します。

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## 機能

| 領域 | Vocat が提供する機能 |
| --- | --- |
| デバイス管理 | シリアル/USB の自動検出、複数モデムのサポート、わかりやすいデバイス名、概要のライブ更新、モジュールの再起動、機内モード、USB ネットワークモードの制御。 |
| 無線とネットワーク | 登録ステータス、オペレーター、信号指標、RSRP/RSRQ/SINR、ネットワークモード、バンド、チャネル、オペレータースキャン、自動または手動のネットワーク選択、および LTE セルクエリと単一セルロック/アンロック（EARFCN/PCI）。 |
| AT と USSD | 対話型 AT ターミナル、コマンド履歴、モデムの生出力、USSD の開始/継続/キャンセルフロー、明確なモデムエラーレポート。 |
| SMS | セルラーおよび IMS SMS の直接送信、受信同期、マルチパート処理、配信レポート、会話履歴、未読状態、タイムスタンプ、メッセージごとの配信ステータス。 |
| WiFi Calling | IKEv2/ePDG トンネルの確立、EAP-AKA 認証、IMS 登録、IMS SMS、再接続制御、ステータス診断、デバイスごとのルーティング。 |
| eSIM と eUICC | eUICC の検出、EID と製造情報、証明書メタデータ、複数 eUICC のインベントリ、インストール済みプロファイルの一覧、有効化/無効化/切り替え操作、およびカードが対応している場合のダウンロード、名前変更、削除操作。 |
| カードポリシー | ICCID ベースの WiFi Calling および機内モードの動作で、ポリシーが即時に適用されます。 |
| プロキシルーティング | アップストリーム SOCKS ルーティング、デバイスバインディング、国別ルール、TCP 到達性チェック、WiFi Calling データパス向けの UDP Associate チェック。 |
| 通知 | Telegram、Bark、メール、Pushplus、署名付き Webhook を介した新着 SMS の転送。各 SMS は個別の通知として配信されます。 |
| Telegram ボット | デバイスステータス、インストール済みプロファイルの一覧と切り替え、WiFi Calling 制御、SMS 送信。機密性の高い操作には管理者の確認が必要です。 |
| 運用 | パスワードおよび SSO 認証（Pocket ID、GitHub、Generic OAuth2）、CSRF 保護、アクセスポリシー、監査イベント、リアルタイムログ、ログ保持、ヘルスチェック、レスポンシブレイアウト、ダークモード、英語/中国語のアプリケーション UI。 |
| 配布 | 静的 Linux バイナリ、systemd インストールスクリプト、SHA-256 検証付きの自己更新、Docker イメージ、GHCR 公開、GitHub Actions リリースビルド。 |

## 対応ハードウェア

Vocat は、互換性のある AT、QMI、シリアル、USB ネットワークインターフェースを公開する Qualcomm ベースの Quectel モジュールおよび China Mobile ML307 シリーズモジュールを対象としています。対象には以下が含まれます:

- Quectel EC20
- Quectel EC25
- Quectel EG25 ファミリー
- 互換性のある EG600 および関連モジュール
- China Mobile ML307 シリーズ（例: ML307A-E5、各種 OEM）

利用可能な機能は、モジュールのファームウェア、USB 構成、SIM/eSIM の機能、ホストドライバー、無線ネットワーク、キャリア設定によって異なります。

## インストール

### ワンクリック Linux インストール

root ユーザー（`sudo` が通常存在しない OpenWrt/Kwrt を含む）として実行:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash
```

sudo を備えたディストリビューションの通常ユーザーから実行:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

VoCat をインストールせずにホストの VoWiFi/XFRM 前提条件を確認する:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash -s -- --check-env
```

特定バージョンをインストールする:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

VoWiFi IMS には Linux XFRM/IPsec が必要です。OpenWrt/Kwrt では、インストーラーがファームウェア自身のフィードから適合する `ip-full`、`kmod-ipsec`、`kmod-ipsec4/6`、`kmod-crypto-authenc`、AES-CBC および SHA1 パッケージのインストールを試みます。適合するカーネルモジュールが利用できない場合は、それらが含まれるファームウェアを使用してください。別のカーネル向けにビルドされた kmod を強制インストールしないでください。

カーネルが XFRM/IPsec を提供できず、セルラー SMS やデータなどの非 VoWiFi 機能のみが必要な場合は、`--skip-vowifi-check` を指定してインストールしてください:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh --skip-vowifi-check
```

インストーラーの処理内容:

- `amd64`、`386`、`arm64`、`aarch64`、または `armv7` を検出します。
- 一致する GitHub Release バイナリをダウンロードします。
- `SHA256SUMS` と照合して検証します。
- Vocat を `/opt/vocat` にインストールします。
- Vocat に必要なハードウェアおよびネットワークアクセス権を備えた、セキュリティ強化された systemd サービスを作成します。
- 実行時設定を `/etc/vocat/env` に保存します。
- 初回インストール時にランダムな初期管理者パスワードを生成します。

インストール後、以下を開きます:

```text
http://<server-address>:7575
```

### 手動バイナリインストール

GitHub Releases から対応するバイナリと `SHA256SUMS` をダウンロードします:

| プラットフォーム | リリースファイル |
| --- | --- |
| Linux x86-64 | `vocat-linux-amd64` |
| Linux x86 32 ビット | `vocat-linux-386` |
| Linux ARM64 | `vocat-linux-arm64` |
| Linux AArch64 | `vocat-linux-aarch64` |
| Linux ARMv7 | `vocat-linux-armv7` |

検証してインストールします:

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

この手動コマンドは Vocat をフォアグラウンドで実行します。プロセスがサーバーを直接起動するように `vocat serve` を使用してください。TTY 上で root として引数なしで `vocat` を実行すると、代わりに対話型管理メニューが開きます。管理された systemd サービスと自動再起動が必要な場合は、ワンクリックインストーラーを使用してください。

### Docker

接続されているすべての対応 Quectel モデムを検出し、USB ホットプラグイベントを認識し続ける必要がある Linux ホストの場合は、ハードウェアアクセスモードで Vocat を実行します:

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

コンテナの起動後に `http://<server-address>:7575` を開きます。QMI ネットワークインターフェースを Vocat から引き続き認識できるようにするにはホストネットワークが必要です。また、シリアルポート、QMI 制御ノード、TUN インターフェース、ネットワーク設定、コンテナ起動後に追加されたデバイスへのアクセスには特権が必要です。`/dev` のバインドマウントにより、コンテナを再作成せずに新しい `ttyUSB*`、`ttyACM*`、`cdc-wdm*` および MHI の `wwan*` ノードを認識できます。

このモードは意図的に Vocat にホストのデバイスとネットワークスタックへの広範なアクセス権を与えます。信頼できる Linux ホストでのみ使用してください。自動検出は、サポート対象の Quectel USB モデム（USB ベンダー ID `2c7c`）および製品文字列による China Mobile ML307 シリーズモジュール、ならびに Linux WWAN サブシステムを通じて公開される PCIe/MHI モデムを識別します。任意のモデム構成を識別するものではありません。`--device` で `/dev/ttyUSB2`、`/dev/cdc-wdm0`、`/dev/wwan0qmi0` などの個別ノードのみをマッピングすると、コンテナはそれらの固定ノードに限定され、複数デバイスやホットプラグを完全には検出できません。

OpenWrt ホストでは、fw4 の input チェーンが管理外インターフェース（Vocat がセッションごとに作成する VoWiFi TUN を含む）からの新規接続を破棄します。そのため Vocat は、イメージ同梱の `nft` を使用して狭い範囲の `vocat-ims-*` 許可ルール（P-CSCF からネゴシエートされた内部アドレス宛て）を挿入します。これらがない場合、IMS 登録や発信 SMS は動作しますが、着信 SMS や通話は拒否されます。同梱の `nft` がホストのルールセットを読み取れない場合、Vocat は `nft could not inspect the OpenWrt fw4 input chain` をログ出力します。その場合は `VOCAT_NFT` をホスト自身の nft に指定してください:

```bash
cat > /opt/vocat-host-nft <<'EOF'
#!/bin/sh
exec /host/lib/ld-musl-$(uname -m).so.1 --library-path /host/usr/lib:/host/lib /host/usr/sbin/nft "$@"
EOF
chmod 755 /opt/vocat-host-nft
# 上記の docker run コマンドに追加:
#   -v /usr:/host/usr:ro -v /lib:/host/lib:ro \
#   -v /opt/vocat-host-nft:/usr/local/bin/host-nft:ro \
#   -e VOCAT_NFT=/usr/local/bin/host-nft
```

GHCR イメージは `linux/amd64` と `linux/arm64` 向けに公開されています。

> [!TIP]
> **NAS / QNAP Container Station へのデプロイに関する注意**：
> QNAP QTS / QuTS hero（Container Station）などの NAS OS では、root 以外のカスタム管理者アカウントとボリューム分離の仕組みにより、Docker の名前付きボリューム（例：`-v vocat-data:/opt/vocat/data`）が、1 回限りの `bootstrap-admin` 初期化時と常駐サービスコンテナで異なる隔離パスに解決され、Web ログイン時に「パスワードが正しくありません」というエラーが発生する場合があります。
> NAS 環境では、SQLite データベースの永続化の一貫性を確保するため、初期化時と実行時の両方で、名前付きボリュームをホストの絶対パスによるバインドマウント（例：QNAP では `-v /share/Container/vocat/data:/opt/vocat/data`）に置き換えることを強く推奨します。

### USB SIM リーダー

USB SIM リーダーは Linux の PC/SC サービスを使用します。ワンクリックインストーラーは、対応するパッケージマネージャーで `pcscd` と CCID ドライバーを自動インストールし、サービスを起動します。Debian/Ubuntu での同等の手動セットアップは `apt install pcscd libccid` です。USB が CCID リーダーを認識していても PC/SC が利用できない場合、VoCat はデバイス追加ダイアログでリーダーを非表示にせず、そのまま表示し、不足しているサービスまたはドライバーを報告します。

### QMI コマンドラインユーティリティ

VoCat は `qmicli` を使って QMI 制御チャネルの準備ができていることを確認し、`qmi-proxy` を使ってそのアクセスを多重化します。パケットデータセッションは、`qmi-network` の CID/PDH 状態ファイルではなく、組み込みの QMI WDS クライアントが管理します。ワンクリックインストーラーは対応するユーティリティをインストールして検証します。手動デプロイの場合、Debian/Ubuntu では `apt install libqmi-utils`、Arch Linux では `pacman -S libqmi`、Alpine では `apk add qmi-utils`、OpenWrt では `opkg install qmi-utils` を使用します。

`vocat doctor --repair-dji-qmi` は、USB ドライバーの関連付けを変更したり、DTR 信号をアサートしたりする前に、`qmicli` の有無を確認します。ユーティリティが利用できない場合、コマンドはインストール方法を案内して停止し、デバイスの現在の状態を変更しません。

### AT セルラーホストデータ

AT ベースの ECM/RNDIS データには Linux、iproute2、および `udhcpc` を含む BusyBox が必要です。
VoCat はモデムのネットワークインターフェースを排他的に管理する必要があります。そのインターフェースに対して競合する DHCP クライアントや NetworkManager プロファイルを無効化してください。VoCat はシステムのデフォルトルートやリゾルバ設定を変更することなく、IPv4 リースとマーク付きポリシールートを維持します。このパスは現在 IPv4 対応のサービスを必要とします。

## 設定

Vocat は `VOCAT_CONFIG` からオプションの JSON 設定ファイルを読み込み、次に `VOCAT_*` 環境変数を適用します。環境変数が優先されます。

| 環境変数 | デフォルト | 説明 |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | HTTP リッスンアドレス。 |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | SQLite データベースパス。 |
| `VOCAT_SESSION_TTL` | `24h` | 認証セッションの有効期間。 |
| `VOCAT_SECURE_COOKIES` | `false` | HTTPS 使用時にセッション Cookie をセキュアとしてマークします。 |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | グレースフルシャットダウンのタイムアウト。 |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | API リクエストボディの最大サイズ。 |
| `VOCAT_MCP_ENABLED` | `true` | AI エージェント向けに `/mcp` で MCP エンドポイントを提供します。無効にするには `false` を設定します。 |
| `VOCAT_PROBE_UNKNOWN_USB` | `false` | 未認識の USB シリアルデバイスを AT コマンドでプローブすることをオプトインします。プローブはシリアル設定を変更し入力をクリアするため、これらのポートがモデム専用である場合のみ有効にしてください。検証されたモデム ID は自動的に検出されます。JSON キー: `probe_unknown_usb`。 |
| `VOCAT_REPO` | `MengMengCode/VoCat` | 自己更新機能が使用する信頼された GitHub リポジトリ(`owner/name` 形式)。 |
| `GITHUB_TOKEN` | 空 | プライベートリポジトリやより高い API レート制限のためのオプションの GitHub トークン。 |

ユーザーが提供する Apple キャリアバンドルは、`vocat carrier import-ipcc` で、内容を確認可能で許可リストに準拠したキャリアプロファイルに変換できます。[docs/CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md) を参照してください。

管理者の認証情報は SQLite にのみ保存されます。空のデータベースを `vocat bootstrap-admin` で一度初期化してください。環境変数や JSON 設定では、管理者のユーザー名やパスワードを設定または上書きできません。

Telegram トークン、SMTP パスワード、Webhook シークレット、SIM 認証情報、その他のプライベートデータをリポジトリに保存しないでください。アプリケーション設定または保護された環境ファイルを通じて設定してください。

## シングルサインオン (SSO)

VoCat は OpenID Connect による Pocket ID、GitHub.com OAuth App、および Generic OAuth2 プロバイダーをサポートしています。**設定 → シングルサインオン (SSO)** でプロバイダーを 1 つ選択します。SSO は初期状態では無効になっており、パスワードログインは引き続き利用可能です。ローカル管理者としてログインし、プロバイダーを保存して、外部アカウントを明示的にバインドしてから SSO を有効にしてください。バインドされていない外部アカウントは登録や管理者昇格を行うことができず、バインドされたアカウントと同じメールアドレスを共有していても自動マージされることはありません。

### Pocket ID

1. `https://vocat.example.com` などの HTTPS オリジンを VoCat に使用し、`VOCAT_SECURE_COOKIES=true` を設定します。SSO 設定で **Pocket ID** を選択します。公開オリジンには最初現在のブラウザアドレスが入力されます。必要に応じて表示される **VoCat 公開オリジン** フィールドを編集してください。コンパクトなコールバック URL とコピーボタンがすぐに表示されるため、OIDC クライアントを作成したり認証情報を保存したりする前に URL をコピーできます: `https://vocat.example.com/api/auth/sso/callback`。
2. Pocket ID で VoCat 用に個別の **Confidential** OIDC クライアントを作成します。正確なコールバック URL を登録し、対象のユーザーまたはグループがこのクライアントの使用を許可されていることを確認します。VoCat と Komari は別個のクライアントとローカルセッションが必要です。
3. VoCat で Pocket ID の正確な公開 `APP_URL` を Issuer として入力し、ステップ 2 で作成したクライアントに発行された Client ID と Client Secret を入力します。Client ID フィールドにはこの値の確認場所のリマインダーが含まれています。スコープと PKCE S256 は VoCat が自動処理します。トークンクライアント認証は検証された Discovery メタデータから自動選択され、`client_secret_post` よりも `client_secret_basic` が優先されます。SSO を無効にしたまま保存します。オリジンを編集すると表示されるコールバックが即座に更新され、保存後に実際の認可に反映されます。
4. 保存された設定をチェックします。成功すると **設定チェックに合格しました** と表示されます。次にアカウントをバインドします。Pocket ID の Passkey ログインと同意を完了し、VoCat に返されたバインディングを確認します。設定チェックはメタデータと接続性のみを検証し、このログインを行わずにクライアントシークレットやユーザー認可を検証することはできません。
5. SSO を有効にして保存します。シークレットブラウザウィンドウを使用して、SSO ログイン、要求されたページへの復帰、およびログイン後最初の状態変更リクエストを検証します。

設定ページにはバインドされたアカウントとアンバインド操作が表示されます。外部アカウントを変更するには、まずアンバインドしてから対象のアカウントをバインドし、再度 SSO を有効にしてください。

Issuer はパスや末尾のスラッシュを含めて正確に照合されます。Pocket ID のログインは検証された ID トークンの `issuer + sub` を使用するため、UserInfo エンドポイントやメールクレームは不要です。Discovery およびその認可、Token、JWKS エンドポイントは設定された Issuer のオリジンと一致している必要があります。異なるバックエンドオリジンを持つ `INTERNAL_APP_URL` を使用したデプロイは現時点ではサポートされていません。公開 `APP_URL` を Issuer として保持してください。保存時には、明示的に設定された既存の認証方式やスコープが保持されます。

### GitHub および Generic OAuth2

GitHub の場合、GitHub.com でまったく同一のコールバック URL を持つ OAuth App を作成し、**GitHub** を選択して Client ID と Secret を入力し、上記の保存・チェック・バインド・有効化の手順に従います。エンドポイントは GitHub.com に固定され、デフォルトのスコープは `read:user` であり、PKCE S256 が必須です。バインディングは不変の GitHub 数値アカウント ID に基づくため、ユーザー名を変更してもアカウントは変わりません。GitHub Enterprise は本リリースの対象外です。

Generic OAuth2 の場合、クライアント認証情報に加えて認可、Token、および UserInfo の各 URL を指定します。Generic OAuth2 を選択すると、スコープ、トップレベルのユーザー ID フィールド名（デフォルトは `id`）、Token 認証および PKCE 互換性オプションが表示されます。UserInfo は Bearer アクセストークンでリクエストされます。ID は空でない文字列または整数である必要があり、大きな整数 ID でも正確な値が保持されます。デフォルトの Token 認証は `client_secret_post` で、PKCE は S256 です。PKCE をサポートしていないプロバイダーに対してのみ PKCE を無効にしてください。Generic OAuth2 は OIDC ID トークン検証を実行しません。

### ネットワーク、認証情報、セッション

VoCat の既存の送信元ネットワークポリシーは SSO ルートにも適用されます。SSO を設定しても、インスタンスが自動的に一般公開されることはありません。ブラウザは VoCat および認可エンドポイントに到達できる必要があり、VoCat プロセスは Discovery、Token、JWKS/UserInfo エンドポイントに信頼できる TLS 証明書で到達できる必要があります。正確な Pocket ID HTTPS Issuer または Generic エンドポイント URL を保存すると、RFC1918/IPv6 ULA プライベートアドレスを含むこれらのアドレスへの HTTPS 接続が承認されます。GitHub の固定エンドポイントは引き続きパブリックアドレスに制限されます。

Generic プロバイダーの内部 HTTP サービスの場合、Token/UserInfo フィールドに完全な HTTP URL を明示的に入力してください。これらの HTTP 送信先に対するすべての DNS 候補および実際の接続先は、許可されたプライベートアドレスである必要があります。Pocket ID の Discovery は別のオリジンを自動承認することはできません。そのエンドポイントは設定された Issuer のオリジンと一致している必要があります。Issuer、Discovery 取得 URL、ブラウザ認可エンドポイントは常に HTTPS を必要とします。ループバック、リンクローカル、クラウドメタデータ、その他の特殊用途のアドレスは IdP 接続に対して引き続きブロックされます。プライベート CA 証明書はホスト/コンテナの信頼ストアにインストールしてください。TLS 検証をバイパスするオプションはありません。ブラウザが VoCat にアクセスするための localhost または HTTPS オリジンは、これらの IdP 接続許可とは独立しています。

付属の Compose デプロイはホストネットワークを使用します。ブリッジネットワークのサービス名が使用可能であると想定せず、実際のコンテナ内から DNS、ルーティング、CA 信頼を検証してください。リバースプロキシは設定された公開オリジンを維持する必要があります。VoCat は転送された Host ヘッダーからコールバック URL を推測しません。ルートパスへのデプロイが必要です。ローカルの Vite 開発では、公開オリジンおよびコールバック登録用に `http://127.0.0.1:5173` などの一貫したループバックオリジンを使用してください。IdP トランスポートの例外はテストフィクスチャ専用です。

セッションおよび CSRF Cookie は `SameSite=Strict` を維持します。短期間の認可 Cookie はトップレベルの GET コールバックをサポートするために `SameSite=Lax` を使用します。SSO の無効化やシークレットのローテーションを含む SSO 設定の保存を行うと、すべての SSO セッションが失効します。アカウントをアンバインドすると、そのバインディングの SSO セッションが失効します。パスワードセッションおよび API トークンはこれらの操作後も保持されます。ローカル管理者のパスワードを変更すると、すべてのセッションと API トークンが失効し、進行中の認可はキャンセルされますが、外部バインディングは保持されます。操作が成功して現在の SSO セッションが失効した場合、UI に再ログインが要求されます。

既存のシークレットを保持するにはシークレット入力を空のままにし、置き換える場合は新しいシークレットを入力します。設定ページにはシークレットをクリアするコントロールはありません。プロバイダー、Client ID、Issuer、または Generic の身元エンドポイント/ID フィールドを変更すると、身元名前空間が切り替わり、新しい認証情報と新しいバインディングが必要になります。既存のバインディングがある場合は明示的なリセット確認が必要です。バインドされたアカウントを変更するには、まずアンバインドしてから新しいバインディングを作成してください。Client Secret はサーバー上の保護された SQLite データベースにのみ保持されます。機密設定フラグはレスポンスのマスキング用であり、データベースを暗号化するものではありません。サードパーティトークンは永続化されず、ブラウザにも返されません。

通常のサインアウトは現在の VoCat セッションのみを終了します。IdP でアカウントを無効化したり Pocket ID からサインアウトしたりしても、既存の VoCat セッションは即座には失効しません。セッションは `VOCAT_SESSION_TTL` に従って期限切れになるか、VoCat で SSO を無効にするかアンバインドすることで即座に失効させることができます。公開機能エンドポイントは `GET /api/auth/providers` です。SSO 設定およびバインディング API の書き込みにはブラウザセッションと CSRF が必要であり、Bearer トークンではこれらを管理できません。設定のリビジョン競合は `409 sso_config_changed` を返し、身元元の変更確認が必要な場合は `409 sso_rebind_required` を返します。

### アップグレードとロールバック

SSO により SQLite スキーマバージョン 29 が追加されます。アップグレードでは既存の管理者、セッション、API トークン、デバイスデータが保持され、SSO は設定されるまで無効のままです。アップグレードの前に、古いバイナリまたはイメージのダイジェストを記録し、VoCat の停止中に設定、CA データ、完全な永続データディレクトリをバックアップするか、SQLite の backup API または `.backup` を使用して一貫したライブスナップショットを取得してください。実行中の WAL モードのデータベースファイルのみをコピーすることは信頼できるバックアップではありません。

プロバイダーの問題から復旧しつつ、パスワードログインと現在の業務データを維持するには、新しいバージョンで SSO を無効にします。スキーマ 28 のバイナリに戻すには、VoCat を停止し、一致するアップグレード前のデータと設定スナップショットを復元する必要があります。単にコンテナイメージを元に戻したり、`PRAGMA user_version` を下げたりするだけでは、データベースをダウングレードできません。可能な限り別のディレクトリ/ボリュームに復元し、調査のために新しいデータベースを保持してください。スナップショットを復元すると、バックアップ以降に記録されたメッセージやタスクの変更が失われます。Docker の場合は、イメージをプルしてコンテナを再作成することで更新します。

本番リリース前に、実際の Pocket ID Passkey/アクセスポリシーのテストと、そのデプロイ環境でのアップグレード/ロールバックの予行演習を完了してください。

## API トークンと MCP

ブラウザ以外のクライアントは、ブラウザのセッション Cookie や CSRF フローの代わりに Bearer トークンを使用して同じ `/api/...` エンドポイントに対して認証を行います:

```bash
vocat token create --read-only --ttl 720h ci-runner   # トークンを一度だけ出力
curl -H "Authorization: Bearer vcat_..." http://127.0.0.1:7575/api/devices
```

`vocat token list` および `vocat token revoke <name>` でインベントリを管理します。トークンは管理者権限で動作します。`--read-only` トークンは `GET` リクエストのみを発行できます（文書化された例外が 1 つあります: SMS スレッドを読み取るとローカルで既読としてマークされます — ルールは「ハードウェアやネットワークの変更を行わない」です）。管理者のパスワードをローテーションすると、すべてのトークンが失効します。初回の `bootstrap-admin` 実行後にトークンを作成してください。リモートの Bearer クライアントは、その送信元アドレスを許可するネットワークアクセスポリシーも必要です。デフォルトの `internal` モードはプライベートネットワークのみを許可します。

デフォルトでは（無効にするには `VOCAT_MCP_ENABLED=false` を設定）、同じサービスが Streamable HTTP トランスポートと Bearer トークン認証を使用して、AI エージェント向けの MCP（Model Context Protocol）エンドポイントを `http://<host>:7575/mcp` で公開します。ツールはデバイスステータス、SMS スレッドおよび保護された送信、eSIM プロファイルの一覧表示と切り替え、WiFi Calling ステータス/再接続をカバーします。機密性の高い操作（生 AT、eSIM のダウンロード/削除、SIM PIN、ネットワーク選択、システム更新）は公開されておらず、ツールの出力に IMSI、SIM PIN、アクティベーションコードが含まれることはありません。読み取り専用トークンは、書き込みツールが除外されたツールリストを受け取ります。SMS 送信は Web UI と同じブロック対象宛先ポリシーとグローバルな 1 時間あたりの制限に従い、セルラー AT パス経由で送信されます（WiFi Calling 有効時の Web UI の IMS 優先ルーティングは Web 側のままであり、MCP 経由で送信されたメッセージはまだ Web の会話履歴には記録されません）。

クライアント設定例（Claude Code の場合）:

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

設計記録については [docs/API_MCP_DEVELOPMENT_PLAN.md](API_MCP_DEVELOPMENT_PLAN.md) を参照してください。

## Telegram ボット

Telegram 通知が有効で、Chat ID と Admin ID の両方が設定されている場合、ボットは以下をサポートします:

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

プロファイルの切り替えと SMS の送信には、ワンタイム確認ボタンが使用されます。ボットは eSIM のダウンロード、削除、名前変更コマンドを公開しません。

Web ターミナルと Telegram は USSD ルーティングを共有します。WiFi Calling が有効な場合、リクエストは IMS/USSI を使用し、セルラー AT+CUSD にフォールバックすることなく、IMS が登録されるまで準備エラーを返します。WiFi Calling が無効な場合、リクエストはセルラー USSD を使用します。メニューの返信とキャンセルは常に元のデバイスとダイアログにバインドされます。キャンセルの失敗はエラーとして報告され、再試行のためにセッションが保持されます。結果が不確実な送信が別のチャネル経由で自動的に再送信されることはありません。

USSI ダイアログの処理は [3GPP TS 24.390](https://www.etsi.org/deliver/etsi_ts/124300_124399/124390/18.00.00_60/ts_124390v180000p.pdf) に準拠します。BYE に失敗した場合でもキャンセル記録が保持されますが、キャンセルが開始された後は以降のメニュー入力がブロックされます（[RFC 3261 第 15.1.1 項](https://www.rfc-editor.org/rfc/rfc3261.html#section-15.1.1)）。

## 更新

より新しい GitHub Release を確認する:

```bash
vocat update --check --repo MengMengCode/VoCat
```

最新リリースをインストールする:

```bash
sudo vocat update --repo MengMengCode/VoCat
```

更新機能は現在の Linux アーキテクチャに一致するバイナリをダウンロードし、公開されている `SHA256SUMS` で検証し、実行ファイルをアトミックに置き換え、利用可能な場合は `vocat` systemd サービスを再起動します。

Docker インストールの場合の更新方法:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

新しいイメージをプルした後にコンテナを再作成します。

## 開発

要件:

- Go 1.25 以降
- Node.js 20 以降
- npm

フロントエンド開発サーバーを実行する:

```bash
cd web
npm install
npm run dev
```

組み込みフロントエンドをビルドしてバックエンドを起動する:

```bash
cd web
npm run build
cd ..
go run ./cmd/vocat
```

すべてのテストを実行する:

```bash
go test ./...
```

本番バイナリをビルドする:

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## リリース自動化

バージョンダグをプッシュすると、2 つの GitHub Actions ワークフローが開始されます:

- `release-binaries`: `amd64`、`386`、`arm64`、`aarch64`、`armv7` バイナリと `SHA256SUMS` をビルドして公開します。
- `docker`: マルチアーキテクチャイメージをビルドし、GitHub Container Registry に公開します。

```bash
git tag v0.2.0
git push origin v0.2.0
```

## プロジェクト構成

```text
cmd/vocat/                  アプリケーションのエントリーポイントと CLI
internal/device/            モデムの検出とデバイス制御
internal/modem/             AT セッションと応答処理
internal/server/            HTTP API、通知、組み込み Web サーバー
internal/store/             SQLite 永続化
internal/update/            GitHub Release による自己更新
internal/vowifi/            IKE、EAP-AKA、IMS、WiFi Calling ランタイム
scripts/install.sh          Linux インストーラーおよびアップデーター
web/src/                    React と TypeScript フロントエンド
.github/workflows/          バイナリおよび Docker のリリース自動化
```

## 責任ある使用

セルラーモデムおよび eSIM の操作は、加入者サービス、保存されているプロファイル、ネットワーク登録、ハードウェアの状態に影響を与える可能性があります。バックアップを保持し、破壊的な操作を慎重に確認し、接続されたハードウェアおよびネットワークリソースの運用が許可されている合法的な環境でのみ本ソフトウェアを使用してください。

Vocat は、キャリア認証、ネットワークポリシー、ハードウェアセキュリティ、または eSIM の信頼要件をバイパスしません。操作への対応は、Vocat がモデムまたは eUICC にそれを要求できることを意味します。デバイス、プロファイル、ネットワーク、またはキャリアによって拒否される場合があります。

## コントリビューション

Issue や Pull Request を歓迎します。変更点を絞り込み、可能な限りテストを含め、認証情報や加入者データのコミットを避け、ハードウェア固有の動作を明確に文書化してください。

変更を送信する前に:

```bash
go test ./...
cd web && npm run build
```

## 謝辞
- [Nodeseek.com](https://www.nodeseek.com) — サーバーに特化したコミュニティ
- [Linux.do](https://linux.do) — 刺激的なテックコミュニティ
- [iniwex5](https://github.com/iniwex5) — スタイルと機能のガイドライン

## コーヒーをおごってください

| ネットワーク | アドレス |
| ------- | ------- |
| USDT-TRON (TRC20) | `TWSAkvzVsFc7KqncDLmUfRxpPQbpV5CgTB` |
| USDT-BSC (BEP20) | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |
| USDT-Polygon | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |

## ライセンス

[LICENSE](../LICENSE) を参照してください。

## Star History

<a href="https://www.star-history.com/?repos=mengmengcode%2Fvocat&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
 </picture>
</a>\n
