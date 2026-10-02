# SMS export

The SMS page includes **Export Messages**. Export all stored messages or select a device and calendar date range. This is a read-only archive feature, not database backup or restore. It does not query the modem, import records, mark messages read, send messages, or run notification/automatic-task processing.

## Formats

- **JSON** preserves every stored SMS field, including exact `body` text, message and device identifiers, SIM identifiers, local/peer numbers, direction, timestamps, status, delivery state, read state, multipart count, source, and `extra` metadata. It uses snake_case field names and a versioned envelope: `format_version` (currently `1`), `exported_at`, `filters`, `messages`, and `message_count`. An empty export has `messages: []` and `message_count: 0`.
- **HTML** is a self-contained offline reader styled after VoCat's SMS screen: device list, searchable contacts, chronological incoming/outgoing bubbles, date separators, and a contact/detail switch on narrow screens. Conversations retain the existing hardware/subscription/peer identity; contacts are listed newest first, while messages are chronological with row ID as a tie-breaker. Device filtering and contact/body search operate only on already-exported records. Device labels and the reader's device filter use original stored device IDs, not current device names or running status. Message details and archive metadata remain available on demand. The file contains one compiled-in, CSP-hashed navigation script and embedded CSS; all dynamic text is escaped, no remote assets or API calls are included, and nothing is sent or deleted. Without JavaScript, all conversations remain readable in a linear document. Opening conversations does not change saved read flags. JSON remains the complete machine-readable archive.

The export includes only records already stored by VoCat. Deleted or never-ingested messages cannot be recovered. Stored text is preserved without decoding or fetching external multimedia content. No restore endpoint is provided.

## Scope and dates

The dialog starts with the SMS page's selected device, makes its scope explicit, and does not inherit the contact search or current page limit. Configured devices remain selectable even when offline or stopped; the export dialog does not reuse the sending-only running-device filter. All-device export also includes historical records for removed devices. Device-specific export uses the same stable IMEI resolution as the SMS page, so renaming a configured device does not lose its existing hardware history. Message fields retain their original stored identities.

Selected start and end calendar days are inclusive in the browser's local time zone. The browser converts them to an inclusive UTC `since` and an exclusive UTC `until` (the next local midnight). Calendar arithmetic handles daylight-saving transitions; it does not assume every local day is 24 hours. JSON timestamps remain UTC. HTML captures the browser's IANA time zone at export time and uses it for message timestamps, export time, and displayed filter boundaries. The archive labels that zone and includes each timestamp's UTC offset, applying historical daylight-saving rules rather than the browser's current fixed offset. Opening the HTML later in a different time zone does not change its displayed times.

## HTTP API

`GET /api/sms/export` uses the existing authenticated API middleware. Parameters:

| Parameter | Meaning |
| --- | --- |
| `format` | `json` (default) or `html` |
| `timezone` | HTML only: IANA time zone captured by the browser, such as `Asia/Shanghai`; omitted means UTC for direct API calls. Empty, invalid, server-local `Local`, or JSON timezone parameters are rejected. |
| `device_id` | A configured/historical device ID; omitted, empty, or `all` means all devices |
| `since` | Optional RFC3339 timestamp, inclusive |
| `until` | Optional RFC3339 timestamp, exclusive and later than `since` when both are supplied |

Timestamp precision is whole seconds; fractional digits representing zero are accepted. Unknown, repeated, malformed, and invalid parameters are rejected rather than silently ignored. There is no pagination parameter or ordinary-list row cap.

Successful downloads include an attachment filename, `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, and `X-SMS-Export-Count`. HTML also includes a restrictive Content Security Policy and an equivalent offline meta policy.

## Integrity and privacy

A single SQLite SELECT supplies one consistent read snapshot, including when messages arrive or change during export. Rows are written incrementally to a private temporary file. Only a fully generated archive is returned; query, encoding, and staging errors do not return an attachment or a successful partial archive. The file is closed and removed when the request finishes. SQLite is released before transferring the download to a slow client.

The server requires temporary disk space for the complete archive. The browser receives a Blob before triggering its download, so very large archives also require browser resources; use device/date filters where needed. Cancellation aborts the request. Normal cleanup removes the temporary file, but abrupt process or machine failure may leave temporary files for the operating system to clean up.

Exports are **not encrypted or redacted**. They contain private messages, phone numbers, hardware/SIM identifiers, and possibly verification codes. Store the downloaded files securely. The UI reports that an archive was generated and asks the user to confirm it in browser downloads; it does not claim that the browser saved the file to disk.

## Verification

```text
go test ./internal/store ./internal/server -count=1
cd web
npm ci
npm test
npm run build
```

Focused Go tests cover unlimited exports, exact preservation, stable ordering, filters, consistent snapshots under concurrent writes, cancellation, authentication, safely escaped offline HTML, invalid queries, and failed writes/staging. Frontend tests cover date boundaries (including DST), scope validation, raw file preservation, and rejecting invalid/incomplete download responses. No schema migration or new dependency is needed.
