# ZXIC platform lab checklist

Repeatable acceptance list for the verified ZXIC platform (USB `19d2:0536`,
UZ901 firmware). The vendor ID `19d2` is shared by several platforms, so a
different product ID is not covered until it is verified individually. Run on
a Linux host with the modem attached (or USB/IP attached) and Vocat built from
this tree. Record each item with a sanitized transcript (no IMSI/IMEI/ICCID,
no APDU payloads, no credentials).

Prerequisites for the host-data items: `iproute2`, BusyBox `udhcpc`, and
`CAP_NET_ADMIN` for host networking. A non-root run of BusyBox `udhcpc` also
needs `CAP_NET_RAW` for the DHCP packet socket it opens on the link layer. The
provided systemd service runs as root and grants `CAP_NET_ADMIN` and
`CAP_NET_RAW`; root has serial access on its own, and the unit does not add
the `dialout` group, so a non-root run needs `dialout` membership as well.
AT-only items need serial access only.

Status key: `[x]` verified on the lab unit, `[ ]` pending privileged smoke.

## 1. Discovery and identity

- [x] Plug the modem in; the kernel binds `cdc_ether` (ECM netdev) and the
  `usbserial_generic` AT ports. Vocat discovers the device by the verified USB
  identity `19d2:0536`; other product IDs under the same vendor keep the
  standard 3GPP command set.
- [ ] Unplug/replug twice; each time Vocat maps the modem back to the already
  configured device entry (stable ID, no duplicate device).
- [x] AT probe succeeds on the AT port; IMEI snapshot is read (`+CGSN:`
  prefix form accepted). *(Smoke 2026-10-23.)*
- [x] `AT+COPS=?` network scan completes and operator selection works (can take
  over a minute; do not confuse with a hang). *(Hardware gate 2026-10-23:
  standard-format scan returned on the live unit.)*

## 2. SIM and AKA

- [x] SIM readiness: `+CPIN: READY`, registration reported (`+CEREG: 5`
  roaming LTE on the lab unit). *(Reconfirmed in smoke 2026-10-23:
  `+CEREG: 2,5,<PLMN>,<CI>,7,0` — the unit briefly dropped and re-registered
  on its own; `+CGATT: 1`.)*
- [x] AKA over the shared CSIM/CGLA executor: unquoted CSIM/CGLA forms are
  negotiated at runtime for this firmware and bare CCHO channel responses are
  accepted (covered by unit tests and live probe); timed-out APDUs are never
  replayed.

## 3. Cellular host data (ECM)

- [x] Modem-side PDP state readable: `AT+CGACT?` returns the context state.
  *(Smoke 2026-10-23: `+CGACT: 1,1` / `+CGACT: 2,1` after the modem
  auto-activated its default PDN — the same auto-activate behavior Vocat's
  enable probe deactivates before `CGDCONT`.)*
- [ ] Enable data in Vocat: transcript shows `CGACT?` probe, `CGDCONT`,
  `CGATT=1`, `CGACT=1,1`, then the host lease comes up (foreground BusyBox
  `udhcpc` reaches BOUND on the ECM netdev).
- [ ] `ip rule` shows the Vocat fwmark rule and a per-interface table
  (20000–29999); the main routing table default route and system DNS files are
  unchanged.
- [ ] `NetworkStatus` reports `backend=at`, `connected=true`.
- [ ] Disable data: protected routes/addresses/DNS file removed, DHCP process
  exited, `AT+CGACT=0,1` sent, no `AT+CGATT=0`.
- [ ] Unplug during an active session: the host session is torn down (no
  orphaned `udhcpc`, no stale routes).

## 4. VoWiFi

- [ ] With data enabled, enable VoWiFi: cellular PDP stops (`CGACT=0,<cid>`),
  radio enters RF-off (`CFUN=4`), IKEv2/EAP-AKA reaches tunnel ready and IMS
  registers. No `CFUN=1` and no `CGACT=1,1` is issued by the success path.
- [ ] While VoWiFi is enabled the data monitor does not reconnect cellular
  data.
- [ ] Fail or disable mid-way: tunnel/IMS resources are released; RF stays off
  until explicit re-enable; no residual routes/addresses/DHCP process.
- [ ] Failure text and status JSON contain no RAND/AUTN/APDU hex, IMSI, IMEI,
  or APN password.

## 5. Negative scope (expected failures, not bugs)

- PPP-only host data paths are out of scope for the first release.
- Non-ZXIC USB identities use the generic 3GPP AT profile; vendor-specific
  dial commands are not sent without hardware evidence (the RNDIS-era `ZGACT`
  dial is known to fail with vendor error 8003 on this ECM firmware).
- The `AT+CGPADDR` query is informational only: the AT data path uses
  `CGACT?` plus DHCP and does not depend on it.

## Smoke record (2026-10-23, lab unit, read-only)

Sanitized command outcomes from the Task 10 smoke pass; identities are
redacted. Host-side data and VoWiFi items remain `[ ]` — this environment
lacks BusyBox `udhcpc` and `CAP_NET_ADMIN`, so those need the deployment
environment.

| Command | Outcome |
| --- | --- |
| `AT` | `OK` |
| `AT+CGSN` | `+CGSN: <15-digit IMEI>` then `OK` — the `+CGSN:` prefix form Vocat's parser accepts |
| `AT+CPIN?` | `+CPIN: READY` |
| `AT+CEREG?` | `+CEREG: 2,5,<mcc/mnc>,<ci>,7,0` then `OK` — roaming LTE |
| `AT+CGATT?` | `+CGATT: 1` then `OK` |
| `AT+CGACT?` | `+CGACT: 1,1` / `+CGACT: 2,1` then `OK` — modem auto-activated its default PDN (also announced via `+CGEV: ME PDN ACT 1`) |
| `AT+CGACT?` (transient unregistered window) | `+CME ERROR: 148` — vendor error while not registered; Vocat's enable probe treats a failed query as non-fatal, so this state does not wedge the enable flow |
| `AT+CGPADDR` (same unregistered window) | `+CME ERROR: 6003` — informational; the AT data path does not rely on `CGPADDR` |
| `ATI` / `AT+CCID` / `AT+QCCID` / `AT+MCCID` / `AT+CLCC` | `+CME ERROR: 6003` — the firmware does not implement them at all (manual has no ATI; `+ZICCID` is documented as URC-only but the `AT+ZICCID?` query form works) |
| `AT+CGMI` / `AT+CGMM` / `AT+CGMR` / `AT+CIMI` / `AT+CGSN` | prefixed responses (`+CGMI: <model>_<variant>` style) — Vocat's identity parser strips the command prefix |
| `AT+ZICCID?` | `+ZICCID: <20-digit ICCID>` then `OK` — the profile-driven ICCID query for this modem family |

The transient unregistered window is the same session that showed
`+CGEV: ME PDN DEACT 1/2` followed by automatic re-attach — independent
evidence that this ZXIC firmware re-establishes its default PDN on its own,
which is exactly the state Vocat's deactivate-if-active probe is designed to
handle before reconfiguring context 1.
