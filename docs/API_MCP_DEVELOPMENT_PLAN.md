# API Tokens and MCP — Development Plan

This plan adds two capabilities to Vocat without changing the existing web
experience: programmatic remote access to the existing JSON API with bearer
tokens, and an MCP (Model Context Protocol) server for AI agents.

## Goals

1. `Authorization: Bearer vcat_...` authenticates non-browser clients against
   the existing `/api/...` endpoints, with no cookies and no CSRF dance.
2. A `/mcp` endpoint exposes Vocat operations as MCP tools over the
   Streamable HTTP transport for local and remote agents, enabled by
   default and authenticated with bearer tokens.
3. Token lifecycle is managed from the CLI (`vocat token ...`).

## Non-goals (deliberately out of scope)

- No `/api/v1` facade and no OpenAPI document. The bearer token works against
  the existing, already-consistent `/api/...` surface. Versioning is worth it
  only when a second consumer demands stability guarantees.
- No per-token scopes beyond a single `read_only` flag, no per-token rate
  limits. Both can be added later without schema changes that matter.
- No multi-user accounts. Vocat has one administrator; tokens act with
  administrator privileges, constrained only by `read_only`.
- (Superseded during development) The plan originally kept token management
  CLI-only with no web UI. A settings-page card — list, create with a
  one-time reveal, revoke — landed later because day-to-day operation from
  the browser is what the deployment actually needed; the CLI remains the
  automation path and the only way a bearer credential itself can never mint
  another credential.

## Design principles

- Reuse the seams the Telegram bot already uses: `internal/device` manager,
   `internal/vowifi` controllers, `internal/store`. A new transport is a new
  consumer of the same services, never a loopback HTTP client.
- Bearer requests skip CSRF entirely: CSRF defends cookie-based browsers;
  a token in an `Authorization` header is not ambient credentials and needs
  no such defense.
- The web (cookie) flow is untouched: no header present → exactly the current
  behavior.
- Follow the existing code style: stdlib `net/http` routing, manual switch
  dispatch, `writeJSON`/`writeError` envelopes, numbered SQL migrations,
  SHA-256-hashed secrets at rest, audit events for security-relevant actions.

## Step A — API token storage

**Files:** `internal/store/migrations.go` (new case 28), `internal/store/store.go`
(or a new `internal/store/apitokens.go`), `internal/auth/service.go`, tests.

Migration 28:

```sql
CREATE TABLE IF NOT EXISTS api_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    token_hash BLOB NOT NULL UNIQUE,
    read_only INTEGER NOT NULL DEFAULT 0 CHECK (read_only IN (0, 1)),
    expires_at INTEGER NOT NULL DEFAULT 0, -- unix seconds; 0 = never
    created_at INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
)
```

- Token format: `vcat_` + base64url of 32 random bytes (reuses
  `auth.randomToken`). The prefix makes tokens identifiable in logs and
  supports future secret scanning. Only the SHA-256 digest is stored, exactly
  like session tokens (`auth.hashToken`).
- `auth.Service` methods: `CreateAPIToken(ctx, name, readOnly, ttl)`,
  `AuthenticateAPIToken(ctx, token)` (validates expiry, opportunistically
  deletes expired tokens — mirroring `DeleteExpiredSessions` in the login
  path — and throttles the `last_used_at` write to one UPDATE per ~5 minutes
  per token), `ListAPITokens(ctx)`, `RevokeAPIToken(ctx, name)`.
- Name constraints: 1–64 chars, `[A-Za-z0-9._-]+`, unique — mirrors the
  device-ID rules already in the codebase.
- `Store.SetAdmin` also deletes all API tokens in the same transaction that
  revokes sessions: rotating the administrator credential must invalidate
  every bearer credential issued under the old one. The update-apply handler
  (`system/update/apply`, which already revokes sessions with a
  "revoke every durable token" comment) deletes API tokens for the same
  reason.
- Consequence worth documenting: `bootstrap-admin` on a fresh database calls
  `SetAdmin` — create tokens after the first bootstrap, not before.

## Step B — Bearer authentication on the HTTP API

**Files:** `internal/server/server.go`, new `internal/server/bearer_auth.go`, tests.

In `handleAPI`, before the session-cookie check:

1. `Authorization: Bearer vcat_...` present and valid → the request proceeds
   with a token principal; CSRF is skipped; mutating methods require a
   non-`read_only` token (else `403 token_read_only`).
2. Header present but invalid/expired → `401 invalid_token` written directly
   with `writeError` (never `authenticationRequired`, which would clear
   session cookies and redirect HTML GETs to `/login`), plus a logged audit
   event (`auth.token` failure; audit rows only for well-formed `vcat_`
   tokens so junk probes stay in the log, not the audit table). No
   fall-through to cookies: a client that sent a header meant to use it.
3. Header absent → existing cookie + CSRF flow, byte for byte.

`read_only` is enforced on the HTTP method (GET/HEAD/OPTIONS only), one
documented exception: `GET /api/sms/thread` marks messages read as a side
effect, which a read-only token is permitted to do to the local database —
the rule is "no hardware or network mutations", stated in the README.
`/api/auth/*` endpoints stay cookie-only (login/logout/session manage the
browser session; `settings/password` likewise re-reads the session cookie and
is browser-only by design). `audit()` resolves the actor `token:<name>` by
stashing the token principal in the request context at interception time.
Sensitive paths already protected by the existing access-control middleware
and request-size limits apply unchanged.

## Step C — `vocat token` CLI

**Files:** `cmd/vocat/main.go` (dispatch), new `cmd/vocat/token.go`, tests.

```
vocat token create [--read-only] [--ttl 720h] <name>   # prints the token once
vocat token list                                        # name, read_only, created, last_used, expires
vocat token revoke <name>
```

Follows the `bootstrap-admin` precedent: opens the store directly, no server
required. `create` and `revoke` append audit events (actor `cli`).

## Step D — `internal/mcp` package

**Files:** new `internal/mcp/` (`server.go`, `tools.go`, `options.go`), tests.

Uses the official SDK `github.com/modelcontextprotocol/go-sdk/mcp` (v1.x,
current spec), pinned to the latest tag. `internal/mcp` must not import
`internal/server` (cycle); it declares narrow local interfaces for the device
and VoWiFi seams it consumes.

`sms_send` enforces the same guardrails as the HTTP path, or agents get an
unlimited, policy-free send channel: the blocked-destination policy
(moved out of `internal/server` into a small shared helper so both transports
call one implementation) and the global hourly limit via
`store.ReserveSMSSend`. v1 sends over the AT path only (the HTTP path's
IMS-preferred routing when VoWiFi is enabled stays HTTP-side); the README
states this. Thread listings come from the store (`ListSMSContacts` /
`ListSMSMessages`), and config→physical device mapping reuses
`internal/vowifi/integration.ATMapper` rather than reimplementing the
server's filter helpers.

Constructor: `mcp` package `New(Options) (*Server, error)` with
`Options{Store, Devices, VoWiFi, Logger, Version, ReadOnly}` and method
`Handler() http.Handler` (Streamable HTTP).

Tools — read-heavy, one write per domain, all named and documented for agent
use:

| Tool | Kind |
| --- | --- |
| `list_devices` | read |
| `device_overview` | read |
| `sms_list_threads` | read |
| `sms_read_thread` | read |
| `sms_send` | write |
| `esim_list_profiles` | read |
| `esim_switch_profile` | write |
| `vowifi_status` | read |
| `vowifi_reconnect` | write |

Excluded on purpose (matching the Telegram bot's boundary): raw AT terminal,
eSIM download/delete/rename, SIM PIN, flight mode, operator selection, cell
lock, system update, USSD dialogs (stateful; poor agent fit for v1).
Tool output never includes IMSI, PIN, or eSIM activation codes.

## Step E — `/mcp` endpoint and the stdio decision

**Files:** `cmd/vocat/main.go`, `internal/server/server.go` + new
`internal/server/mcp_mount.go`, `internal/config/config.go`, tests.

The plan originally proposed a `vocat mcp` stdio subcommand with a loopback
fallback "decided by evidence in this step". The evidence decided against
stdio: device leases are process-local, so a second Vocat process speaking
stdio MCP on the same host would contend with the running service for the
same serial ports, and MCP clients speak Streamable HTTP natively — the
`/mcp` endpoint serves both local and remote agents without a second
process. The subcommand is dropped; the endpoint is the delivery.

- `/mcp` is mounted on the existing listener by default; `VOCAT_MCP_ENABLED=false`
  (or `mcp_enabled: false` in the config file) removes it. It requires a
  valid bearer token; a read-only token receives a server variant whose
  tool list omits the write tools entirely. `cmd/vocat` constructs both
  variants and passes them to `server.Options`, so neither package imports
  the other's implementation.

## Testing

Every step lands with tests in the existing style and `go test ./...` green:

- A: token CRUD, uniqueness, expiry, `read_only` flag, throttled
  `last_used_at`.
- B: valid bearer GET; bearer write with read-only token → 403; invalid
  token → 401; absent header → cookie flow unchanged; CSRF not required for
  bearer; `/api/auth/*` rejects bearer.
- C: create/list/revoke happy paths, name validation, duplicate names,
  audit rows written.
- D: tool handlers against fake device/VoWiFi implementations (the Telegram
  bot tests already fake these); redaction assertions.
- E: `/mcp` requires a bearer token, the JSON-RPC handshake over
  Streamable HTTP works, read-only tokens receive the reduced tool list,
  and the endpoint is absent when MCP is disabled.

## Documentation

README: `VOCAT_MCP_ENABLED` row, a short "API tokens" section (curl example,
the `read_only` rule and its `sms/thread` exception, that remote bearer
clients need an access-control mode that admits their source address — the
default `internal` mode only serves private networks, and that tokens are
created after the first bootstrap) and an "MCP" section with an HTTP client config snippet. Keep both sections brief; this
document stays as the design record.

## Delivery

One commit per reviewed step on `feat/api-mcp`, each passing `go vet` and
`go test ./...` before the next step starts.
