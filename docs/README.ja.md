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

Vocat は、Quectel EC20/EC25 クラスのセルラーモデム向けのオープンソース Web コントロールパネル兼エンジニアリングツールキットです。モデムの検出、ライブの無線ステータス、AT / USSD ターミナル、SMS、WiFi Calling、eSIM 管理、ネットワーク選択、プロキシルーティング、通知、監査ログ、リリース自動化を、自己完結型の単一サービスに統合しています。

バックエンドは Go で書かれ、インターフェースは React と TypeScript で構築され、本番フロントエンドは Go バイナリに埋め込まれています。単一の実行ファイルに Web アプリケーション全体が含まれ、永続的な状態には SQLite を使用します。

<p align="center">
  <img src="../img/image.png">
  <img src="../img/image-1.png">
</p>

## 機能

| 領域 | Vocat が提供する機能 |
| --- | --- |
| デバイス管理 | シリアル/USB の自動検出、複数モデムのサポート、わかりやすいデバイス名、概要のライブ更新、モジュールの再起動、機内モード、USB ネットワークモードの制御。 |
| 無線とネットワーク | 登録ステータス、オペレーター、信号指標、RSRP/RSRQ/SINR、ネットワークモード、バンド、チャネル、オペレータースキャン、自動または手動のネットワーク選択。 |
| AT と USSD | 対話型 AT ターミナル、コマンド履歴、モデムの生出力、USSD の開始/継続/キャンセルフロー、明確なモデムエラーレポート。 |
| SMS | セルラーおよび IMS SMS の直接送信、受信同期、マルチパート処理、配信レポート、会話履歴、未読状態、タイムスタンプ、メッセージごとの配信ステータス。 |
| WiFi Calling | IKEv2/ePDG トンネルの確立、EAP-AKA 認証、IMS 登録、IMS SMS、再接続制御、ステータス診断、デバイスごとのルーティング。 |
| eSIM と eUICC | eUICC の検出、EID と製造情報、証明書メタデータ、複数 eUICC のインベントリ、インストール済みプロファイルの一覧、有効化/無効化/切り替え操作、およびカードが対応している場合のダウンロード、名前変更、削除操作。 |
| カードポリシー | ICCID ベースの WiFi Calling および機内モードの動作で、ポリシーが即時に適用されます。 |
| プロキシルーティング | アップストリーム SOCKS ルーティング、デバイスバインディング、国別ルール、TCP 到達性チェック、WiFi Calling データパス向けの UDP Associate チェック。 |
| 通知 | Telegram、Bark、メール、Pushplus、署名付き Webhook を介した新着 SMS の転送。各 SMS は個別の通知として配信されます。 |
| Telegram ボット | デバイスステータス、インストール済みプロファイルの一覧と切り替え、WiFi Calling 制御、SMS 送信。機密性の高い操作には管理者の確認が必要です。 |
| 運用 | 認証、CSRF 保護、アクセスポリシー、監査イベント、ライブログ、ログ保持、ヘルスチェック、レスポンシブレイアウト、ダークモード、英語/中国語のアプリケーション UI。 |
| 配布 | 静的 Linux バイナリ、systemd インストールスクリプト、SHA-256 検証付きの自己更新、Docker イメージ、GHCR 公開、GitHub Actions リリースビルド。 |

## 対応ハードウェア

Vocat は、互換性のある AT、QMI、シリアル、USB ネットワークインターフェースを公開する Qualcomm ベースの Quectel モジュールを対象としています。対象には以下が含まれます:

- Quectel EC20
- Quectel EC25
- Quectel EG25 ファミリー
- 互換性のある EG600 および関連モジュール

利用可能な機能は、モジュールのファームウェア、USB 構成、SIM/eSIM の機能、ホストドライバー、無線ネットワーク、キャリア設定によって異なります。

## インストール

### ワンクリック Linux インストール

root として(`sudo` が通常存在しない OpenWrt/Kwrt を含む):

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash
```

sudo を持つディストリビューションの一般ユーザーから:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

VoCat をインストールせずに、ホストの VoWiFi/XFRM 前提条件を確認する:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash -s -- --check-env
```

特定のバージョンをインストールする:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

VoWiFi IMS には Linux XFRM/IPsec が必要です。OpenWrt/Kwrt では、インストーラーはファームウェア自身のフィードから、一致する `ip-full`、`kmod-ipsec`、`kmod-ipsec4/6`、`kmod-crypto-authenc`、AES-CBC、SHA1 パッケージのインストールを試みます。一致するカーネルモジュールが利用できない場合は、それらを含むファームウェアを使用してください。別のカーネル向けにビルドされた kmod を強制的にインストールしてはいけません。

カーネルが XFRM/IPsec を提供できず、セルラー SMS やデータ通信など VoWiFi 以外の機能のみが必要な場合は、`--skip-vowifi-check` を指定してインストールしてください：

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh --skip-vowifi-check
```

インストーラーは次を行います:

- `amd64`、`386`、`arm64`、`aarch64`、`armv7` を検出します;
- 一致する GitHub Release バイナリをダウンロードします;
- `SHA256SUMS` と照合して検証します;
- Vocat を `/opt/vocat` にインストールします;
- Vocat が必要とするハードウェアおよびネットワークアクセスを持つ強化された systemd サービスを作成します;
- 実行時設定を `/etc/vocat/env` に保存します;
- 初回インストール時にランダムな初期管理者パスワードを生成します。

インストール後、次を開きます:

```text
http://<server-address>:7575
```

### 手動バイナリインストール

一致するバイナリと `SHA256SUMS` を GitHub Releases からダウンロードします:

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

この手動コマンドは Vocat をフォアグラウンドで実行します。プロセスがサーバーを直接起動するように `vocat serve` を使用してください。TTY で root として引数なしで `vocat` を実行すると、代わりに対話型管理メニューが開きます。管理対象の systemd サービスと自動再起動が必要な場合は、ワンクリックインストーラーを使用してください。

### Docker

接続されているすべてのサポート対象 Quectel モデムを検出し、USB ホットプラグイベントを継続的に認識する必要がある Linux ホストでは、Vocat をハードウェアアクセスモードで実行します:

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

このモードは意図的に Vocat にホストのデバイスとネットワークスタックへの広範なアクセス権を与えます。信頼できる Linux ホストでのみ使用してください。自動検出は、サポート対象の Quectel USB モデム（USB ベンダー ID `2c7c`）と、Linux WWAN サブシステムを通じて公開される PCIe/MHI モデムを識別します。任意のモデム構成を識別するものではありません。`--device` で `/dev/ttyUSB2`、`/dev/cdc-wdm0`、`/dev/wwan0qmi0` などの個別ノードのみをマッピングすると、コンテナはそれらの固定ノードに限定され、複数デバイスやホットプラグを完全には検出できません。

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
| `VOCAT_REPO` | `MengMengCode/VoCat` | 自己更新機能が使用する信頼された GitHub リポジトリ(`owner/name` 形式)。 |
| `GITHUB_TOKEN` | 空 | プライベートリポジトリやより高い API レート制限のためのオプションの GitHub トークン。 |

ユーザーが提供する Apple キャリアバンドルは、`vocat carrier import-ipcc` で、内容を確認可能で許可リストに準拠したキャリアプロファイルに変換できます。[docs/CARRIER_IPCC_IMPORT.md](CARRIER_IPCC_IMPORT.md) を参照してください。

管理者の認証情報は SQLite にのみ保存されます。空のデータベースを `vocat bootstrap-admin` で一度初期化してください。環境変数や JSON 設定では、管理者のユーザー名やパスワードを設定または上書きできません。

Telegram トークン、SMTP パスワード、Webhook シークレット、SIM 認証情報、その他のプライベートデータをリポジトリに保存しないでください。アプリケーション設定または保護された環境ファイルを通じて設定してください。

## Telegram ボット

Telegram 通知が有効で、Chat ID と Admin ID の両方が設定されている場合、ボットは以下をサポートします:

```text
/status [device]
/esim <device>
/switch <device> <iccid>
/wfc <device> <status|on|off|reconnect>
/sms <device> <number> <message>
```

プロファイルの切り替えと SMS の送信には、ワンタイム確認ボタンが使用されます。ボットは eSIM のダウンロード、削除、名前変更コマンドを公開しません。

## 更新

より新しい GitHub Release を確認する:

```bash
vocat update --check --repo MengMengCode/VoCat
```

最新リリースをインストールする:

```bash
sudo vocat update --repo MengMengCode/VoCat
```

アップデーターは、現在の Linux アーキテクチャに一致するバイナリをダウンロードし、公開された `SHA256SUMS` で検証し、実行ファイルをアトミックに置き換え、利用可能な場合は `vocat` systemd サービスを再起動します。

Docker インストールの場合:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

新しいイメージをプルした後、コンテナを再作成します。

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

埋め込みフロントエンドをビルドしてバックエンドを起動する:

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

バージョンタグをプッシュすると、2 つの GitHub Actions ワークフローが開始されます:

- `release-binaries` は `amd64`、`386`、`arm64`、`aarch64`、`armv7` バイナリと `SHA256SUMS` をビルドして公開します。
- `docker` はマルチアーキテクチャイメージをビルドして GitHub Container Registry に公開します。

```bash
git tag v0.2.0
git push origin v0.2.0
```

## プロジェクト構成

```text
cmd/vocat/                  アプリケーションのエントリポイントと CLI
internal/device/            モデム検出とデバイス制御
internal/modem/             AT セッションと応答処理
internal/server/            HTTP API、通知、埋め込み Web サーバー
internal/store/             SQLite 永続化
internal/update/            GitHub Release 自己更新機能
internal/vowifi/            IKE、EAP-AKA、IMS、WiFi Calling ランタイム
scripts/install.sh          Linux インストーラーとアップデーター
web/src/                    React と TypeScript のフロントエンド
.github/workflows/          バイナリと Docker のリリース自動化
```

## 責任ある使用

セルラーモデムおよび eSIM の操作は、加入者サービス、保存されたプロファイル、ネットワーク登録、ハードウェア状態に影響を与える可能性があります。バックアップを保持し、破壊的な操作を慎重に確認し、接続されたハードウェアとネットワークリソースを操作することが許可されている合法的な環境でのみソフトウェアを使用してください。

Vocat は、キャリア認証、ネットワークポリシー、ハードウェアセキュリティ、eSIM の信頼要件をバイパスしません。操作のサポートは、Vocat がモデムまたは eUICC にそれを要求できることを意味します。デバイス、プロファイル、ネットワーク、またはキャリアがそれを拒否する場合があります。

## コントリビューション

Issue や Pull Request を歓迎します。変更は焦点を絞り、可能な場合はテストを含め、認証情報や加入者データをコミットしないようにし、ハードウェア固有の動作を明確に文書化してください。

変更を提出する前に:

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

<a href="https://star-history.dera.page/#MengMengCode/VoCat">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://star-history.dera.page/svg?repos=MengMengCode/VoCat&theme=dark" />
   <source media="(prefers-color-scheme: light)" srcset="https://star-history.dera.page/svg?repos=MengMengCode/VoCat" />
   <img alt="Star History Chart" src="https://star-history.dera.page/svg?repos=MengMengCode/VoCat" />
 </picture>
</a>
