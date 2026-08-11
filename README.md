<p align="center">
  <img src="web/public/favicon.svg" width="96" alt="Vocat">
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

**English** | [简体中文](docs/README.zh-CN.md)

Vocat is an open-source web control panel and engineering toolkit for Quectel EC20/EC25-class cellular modems. It combines modem discovery, live radio status, AT and USSD terminals, SMS, WiFi Calling, eSIM management, network selection, proxy routing, notifications, audit logs, and release automation in one self-contained service.

The backend is written in Go, the interface is built with React and TypeScript, and the production frontend is embedded into the Go binary. A single executable contains the web application and uses SQLite for persistent state.

<p align="center">
  <img src="img\image.png">
  <img src="img\image-1.png">
</p>

## Features

| Area | What Vocat provides |
| --- | --- |
| Device management | Automatic serial/USB discovery, multiple modem support, friendly device names, live overview updates, module restart, flight mode, and USB networking mode controls. |
| Radio and network | Registration status, operator, signal metrics, RSRP/RSRQ/SINR, network mode, band, channel, operator scanning, and automatic or manual network selection. |
| AT and USSD | Interactive AT terminal, command history, raw modem responses, USSD start/continue/cancel flows, and clear modem error reporting. |
| SMS | Direct cellular and IMS SMS transmission, inbound synchronization, multipart handling, delivery reports, conversation history, unread state, timestamps, and per-message delivery status. |
| WiFi Calling | Engineering implementation of IKEv2/ePDG tunnel setup, EAP-AKA/EAP-AKA' authentication, IMS registration, IMS SMS/calls, reconnect controls, status diagnostics, and per-device routing. Carrier interoperability must be verified separately. |
| eSIM and eUICC | eUICC discovery, EID and production information, certificate metadata, multi-eUICC inventory, installed profile listing, enable/disable/switch operations, download, rename, and delete operations when supported by the card. |
| Card policy | ICCID-based WiFi Calling and flight-mode behavior with immediate policy application. |
| Proxy routing | Upstream SOCKS routing, device bindings, country rules, TCP reachability checks, and UDP Associate checks for WiFi Calling data paths. |
| Notifications | New inbound SMS forwarding through Telegram, Bark, email, Pushplus, and signed webhooks. Each SMS is delivered as an individual notification. |
| Telegram bot | Device status, installed-profile listing and switching, WiFi Calling controls, SMS sending, timed dialing with automatic hang-up, call status, answer, and hang-up commands. Sensitive actions require administrator confirmation. |
| Operations | Authentication, CSRF protection, access policies, audit events, live logs, log retention, health checks, responsive layout, dark mode, and English/Chinese application UI. |
| Distribution | Static Linux binaries, systemd installation script, self-update with SHA-256 verification, Docker image, GHCR publishing, and GitHub Actions release builds. |

## Supported hardware

Vocat targets Qualcomm-based Quectel modules that expose compatible AT, QMI, serial, and USB networking interfaces, including:

- Quectel EC20
- Quectel EC25
- Quectel EG25 family
- Compatible EG600 and related modules

Available features depend on the module firmware, USB composition, SIM/eSIM capabilities, host drivers, radio network, and carrier configuration.

Snapdragon 410/MSM8916 or an OpenStick form factor alone is not a supported
configuration claim. That path additionally requires a matching baseband,
working UIM/QMI control ports, host WWAN drivers, and carrier IMS/VoWiFi
entitlement. It must be accepted on the exact hardware and SIM combination.

## Capability and acceptance boundaries

Vocat does **not** implement a complete Qualcomm cellular IMS/VoLTE stack. Its
cellular side establishes packet-data sessions through QMI and sends call-control
commands such as dial, answer, hang up, and call-list queries to the module over
AT. Whether such a call uses VoLTE, circuit-switched fallback, or fails is decided
by the modem firmware, SIM, radio network, and carrier provisioning. Vocat does
not currently provide its own cellular IMS PDN/registration, dedicated voice
bearer, QMI IMSA/VOICE control, SRVCC, or LTE-to-WiFi call continuity.

The VoWiFi code is an engineering implementation, not carrier certification or
proof that an arbitrary Snapdragon 410 device supports WiFi Calling. Do not use
UI state alone as acceptance evidence. Validate each stage below in order and
retain modem, packet-capture, and call/media evidence from the target device:

| Stage | Required evidence | Current responsibility |
| --- | --- | --- |
| 0. Platform | MSM8916/baseband version, UIM access, QMI/AT ports, host drivers | Hardware image and device integration; verify per unit |
| 1. LTE data | LTE registration, default data session, address, DNS and routed traffic | Vocat QMI data control plus modem/network |
| 2. Cellular IMS | Separate IMS connectivity and P-CSCF discovery | Modem/carrier; not implemented as a Vocat cellular IMS stack |
| 3. VoLTE | IMS registered, MO/MT call, two-way media and emergency-policy checks | Modem/carrier; AT call success alone is insufficient |
| 4. ePDG discovery | Correct operator ePDG FQDN and reachable DNS result | Vocat plus carrier DNS/provisioning |
| 5. IKE | Agreed algorithms and validated ePDG certificate/AUTH | Vocat; confirm with an outer-tunnel capture |
| 6. EAP | The configured EAP-AKA or EAP-AKA' method succeeds using the target UICC | Vocat/UICC/carrier; a challenge alone is not success |
| 7. CHILD_SA | Inner IP, traffic selectors and reachable P-CSCF | Vocat; verify routes and encrypted traffic |
| 8. VoWiFi IMS | Registered IMS, MO/MT SMS and calls, two-way media | Vocat/carrier interop; test on every target profile |
| 9. Continuity | Successful LTE-to-WiFi and WiFi-to-LTE call handover | Not currently implemented |

Emergency calling and carrier-registered emergency-address workflows require
operator integration and jurisdiction-specific acceptance; Vocat must not be
treated as a certified emergency-calling solution. Vocat therefore rejects
emergency-number calls and does not claim to provision an emergency address.

Current implementation limits that matter during acceptance:

- The QMI packet-data backend is IPv4-only. An `IPV4V6` request is explicitly
  reported as an IPv4 downgrade; an IPv6-only request fails before starting the
  data session.
- VoWiFi media supports G.711 PCMA/PCMU, RTCP, and negotiated RFC 4733 DTMF.
  It does not transcode AMR-NB or AMR-WB, so an AMR-only carrier offer is
  rejected instead of advertising media that Vocat cannot carry.
- IMS APN, private/public identities, transport, SMSC, and EAP method are
  configured per device and are separate from the cellular data APN. Saving
  these settings requires a Vocat process restart before the in-memory VoWiFi
  runtime is rebuilt.
- IKE defaults to MODP2048 with SHA-256. SHA-1 compatibility and MODP1024 are
  separate per-device switches, both disabled by default; enable only the exact
  legacy capability required by a verified carrier profile. No PLMN silently
  enables weak algorithms during an upgrade.
- If the IMS SMSC field is blank, Vocat first uses the SIM/modem `AT+CSCA?`
  value. There is no operator-specific global SMSC default; upgrades
  intentionally do not preserve the former Vodafone-only fallback, so configure
  the carrier SMSC explicitly when the SIM returns an empty value.

## Installation

### One-click Linux installation

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

Install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

The installer:

- detects `amd64`, `386`, `arm64`, `aarch64`, or `armv7`;
- downloads the matching GitHub Release binary;
- verifies it against `SHA256SUMS`;
- installs Vocat under `/opt/vocat`;
- creates a hardened systemd service with the hardware and network access required by Vocat;
- stores runtime configuration in `/etc/vocat/env`;
- generates a random initial administrator password on first installation.

It also installs and verifies the external networking tools used at runtime:
`iproute2`, BusyBox with the `udhcpc` applet, and libqmi's `qmi-network`,
`qmicli`, and `qmi-proxy`. Debian/Ubuntu installations use the `iproute2`,
`busybox`, and `libqmi-utils` packages; Alpine installations use `iproute2`,
`busybox`, and `qmi-utils`. On other distributions, install equivalent packages
before running the installer.

After installation, open:

```text
http://<server-address>:7575
```

### Manual binary installation

Download the matching binary and `SHA256SUMS` from GitHub Releases:

| Platform | Release file |
| --- | --- |
| Linux x86-64 | `vocat-linux-amd64` |
| Linux x86 32-bit | `vocat-linux-386` |
| Linux ARM64 | `vocat-linux-arm64` |
| Linux AArch64 | `vocat-linux-aarch64` |
| Linux ARMv7 | `vocat-linux-armv7` |

Install the runtime packages listed above, then confirm the host exposes the
required helpers before starting Vocat:

```bash
command -v ip busybox qmi-network qmicli
busybox udhcpc --help >/dev/null
command -v qmi-proxy >/dev/null 2>&1 || test -x /usr/libexec/qmi-proxy
```

Verify and install it:

```bash
sha256sum -c SHA256SUMS --ignore-missing
sudo install -d -m 0755 /opt/vocat/bin /opt/vocat/data
sudo install -m 0755 vocat-linux-amd64 /opt/vocat/bin/vocat
sudo env \
  VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db \
  VOCAT_ADMIN_PASSWORD=change-this-password \
  /opt/vocat/bin/vocat serve
```

This manual command runs Vocat in the foreground. Use `vocat serve` so the
process starts the server directly; running `vocat` without arguments as root
on a TTY opens the interactive management menu instead. Use the one-click
installer when a managed systemd service and automatic restart are required.

### Docker

The default Compose file intentionally starts a normal bridged container with
no host device access. It is suitable for inspecting the UI and configuration,
but it cannot discover/control modems and is **not** a Snapdragon 410 test or
deployment:

```bash
cp .env.example .env
# Set VOCAT_ADMIN_PASSWORD in .env.
docker compose up -d
```

For a trusted Linux modem host, apply the explicit hardware override:

```bash
docker compose \
  -f docker-compose.yml \
  -f docker-compose.hardware.yml \
  up -d
```

The hardware override uses the Compose `!reset` tag and requires Docker Compose
2.24 or newer.

The override switches to host networking, runs as privileged root, and mounts
the host's `/dev` and `/sys`. This is required for QMI-created
WWAN interfaces, serial/QMI hot-plug, TUN/XFRM and policy routing. It gives the
container broad control of the host and should only be used after reviewing
`docker-compose.hardware.yml`.

The official image contains `iproute2`, BusyBox `udhcpc`, `qmi-network`,
`qmicli`, and `qmi-proxy`, and verifies them while the image is built. For an
image-only hardware deployment, the equivalent explicit command is:

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

Open `http://<server-address>:7575` after the container starts. The `/dev` bind
mount makes new `ttyUSB*`, `ttyACM*`, and `cdc-wdm*` nodes visible without
recreating the container.

This mode intentionally gives Vocat broad access to the host's devices and
network stack. Use it only on a trusted Linux host. The automatic discovery
currently identifies supported Quectel USB modems (USB vendor ID `2c7c`), not
arbitrary modem brands. Mapping only individual nodes with `--device`, such as
`/dev/ttyUSB2` and `/dev/cdc-wdm0`, limits the container to those fixed nodes
and does not provide complete multi-device or hot-plug discovery.

The GHCR image is published for `linux/amd64` and `linux/arm64`.

## Configuration

Vocat reads an optional JSON configuration file from `VOCAT_CONFIG`, then applies `VOCAT_*` environment variables. Environment variables take precedence.

| Environment variable | Default | Description |
| --- | --- | --- |
| `VOCAT_ADDR` | `0.0.0.0:7575` | HTTP listen address. |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | SQLite database path. |
| `VOCAT_ADMIN_USERNAME` | `admin` | Initial administrator username. |
| `VOCAT_ADMIN_PASSWORD` | `admin` | Initial administrator password. Change it before exposing the service. |
| `VOCAT_SESSION_TTL` | `24h` | Authentication session lifetime. |
| `VOCAT_SECURE_COOKIES` | `false` | Marks session cookies as secure when HTTPS is used. |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown timeout. |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | Maximum API request body size. |
| `VOCAT_REPO` | `MengMengCode/VoCat` | Trusted GitHub repository used by the self-updater, in `owner/name` form. |
| `GITHUB_TOKEN` | empty | Optional GitHub token for private repositories or higher API limits. |

Do not store Telegram tokens, SMTP passwords, webhook secrets, SIM credentials, or other private data in the repository. Configure them through the application settings or protected environment files.

## Telegram bot

When Telegram notifications are enabled and both Chat ID and Admin ID are configured, the bot supports:

```text
/status [device]
/esim <device>
/switch <device> <iccid>
/wfc <device> <status|on|off|reconnect>
/sms <device> <number> <message>
/call <device> <number> <seconds>
/calls <device>
/answer <device>
/hangup <device>
```

Profile switching, SMS submission, and dialing use one-time confirmation buttons. Timed dialing performs the modem call action and automatically hangs up after 1–600 seconds; it does not capture or process call audio. The bot does not expose eSIM download, delete, or rename commands.

## Updating

Check for a newer GitHub Release:

```bash
vocat update --check --repo MengMengCode/VoCat
```

Install the latest release:

```bash
sudo vocat update --repo MengMengCode/VoCat
```

The updater downloads the binary matching the current Linux architecture, verifies it with the published `SHA256SUMS`, replaces the executable atomically, and restarts the `vocat` systemd service when available.

For Docker installations:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

Recreate the container after pulling the new image.

## Development

Requirements:

- Go 1.25 or newer
- Node.js 20 or newer
- npm

Run the frontend development server:

```bash
cd web
npm install
npm run dev
```

Build the embedded frontend and start the backend:

```bash
cd web
npm run build
cd ..
go run ./cmd/vocat
```

Run all tests:

```bash
go test ./...
```

Build a production binary:

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## Release automation

Pushing a version tag starts two GitHub Actions workflows:

- `release-binaries` builds and publishes `amd64`, `386`, `arm64`, `aarch64`, and `armv7` binaries plus `SHA256SUMS`.
- `docker` builds and publishes a multi-architecture image to GitHub Container Registry.

```bash
git tag v0.2.0
git push origin v0.2.0
```

## Project layout

```text
cmd/vocat/                  Application entry point and CLI
internal/device/            Modem discovery and device control
internal/modem/             AT session and response handling
internal/server/            HTTP API, notifications, and embedded web server
internal/store/             SQLite persistence
internal/update/            GitHub Release self-updater
internal/vowifi/            IKE, EAP-AKA/EAP-AKA', IMS, and WiFi Calling runtime
scripts/install.sh          Linux installer and updater
web/src/                    React and TypeScript frontend
.github/workflows/          Binary and Docker release automation
```

## Responsible use

Cellular modem and eSIM operations can affect subscriber service, stored profiles, network registration, and hardware state. Keep backups, review destructive actions carefully, and use the software only in lawful environments where you are permitted to operate the connected hardware and network resources.

Vocat does not bypass carrier authentication, network policy, hardware security, or eSIM trust requirements. Support for an operation means that Vocat can request it from the modem or eUICC; the device, profile, network, or carrier may still reject it.

## Contributing

Issues and pull requests are welcome. Keep changes focused, include tests where practical, avoid committing credentials or subscriber data, and document hardware-specific behavior clearly.

Before submitting a change:

```bash
go test ./...
cd web && npm run build
```

## Thanks
- [Nodeseek.com](https://www.nodeseek.com) — A community dedicated to servers
- [Linux.do](https://linux.do) — An inspiring tech community
- [iniwex5](https://github.com/iniwex5) - Style and Functionality Guidelines

## License

See [LICENSE](LICENSE).
