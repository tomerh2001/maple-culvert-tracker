# Native image submission API, version 1

This API is a server-to-server integration. The native client authenticates to
Mania; the Mania server holds the Culvert credential. No bot token, global JWT
secret, integration token or arbitrary destination URL belongs in the client.
The existing Discord command remains `/submit-scores`. Both transports share
image parsing, validation, name canonicalization and transactional score writes.

`POST /api/v1/guild-score-submissions` accepts `multipart/form-data`: a first
part named `manifest` containing the JSON below, followed by exactly one PNG
part named `page-1`, `page-2`, etc. in ordinal order. `Authorization: Bearer
<scoped opaque token>` and `Idempotency-Key: <capture_run_id>` are required.

```json
{
  "schema_version": 1,
  "origin": "submit-images",
  "capture_run_id": "unique-stable-run-id",
  "native_operation_id": "native-operation-id",
  "collector_id": "configured-collector",
  "destination_id": "configured-destination",
  "maple_guild": "ExampleGuild",
  "schedule_revision": "revision-id",
  "slot_id": "stable-slot-id",
  "reset_at_utc": "2026-11-12T00:00:00Z",
  "captured_started_at_utc": "2026-11-11T23:40:00Z",
  "captured_ended_at_utc": "2026-11-11T23:40:10Z",
  "expected_week_key": "2026-11-04",
  "complete": true,
  "first_page_verified": true,
  "last_page_verified": true,
  "pages": [{
    "ordinal": 1,
    "acquisition_id": "original-native-acquisition-id",
    "captured_at_utc": "2026-11-11T23:40:05Z",
    "sha256": "64-lowercase-hex-digits",
    "byte_length": 12345,
    "width": 1280,
    "height": 720,
    "pixel_format": "gray8"
  }]
}
```

Bounds: 1–20 pages; 8 MiB per PNG; 64 MiB combined PNG bytes; manifest 32 KiB;
dimensions 1–8192 and at most 24 million pixels per image. PNG header dimensions
are checked before decoding. `gray8` and `rgb8` describe the original capture;
they do not certify visual readability, source freshness or guild identity.
The server checks encoding, lengths, dimensions and hashes. Completeness and
native observation IDs remain authenticated collector claims until the native
feature has real qualification evidence. No URL fetch, per-page score commit,
raw score JSON or image transformation is supported by this route.

The configured principal binds collector, destination, exact Maple guild,
Discord guild and tenant. An enabled configuration is an explicit image-submit
grant; it does not confer admin, legacy JWT or cross-tenant access. Configuration
is fail-closed when absent, malformed or expired. Tokens are random secrets;
only their SHA-256 digests are stored in Culvert configuration.

A successful POST durably stores the complete immutable batch and returns 202
with `submission_id`, `state`, `replayed` and `status_url`. Repeating the same
key and manifest/image digest returns the original receipt. Reusing a key with
different content, or reopening the same collector/slot/reset with another run,
returns 409. A schedule revision does not reopen a claimed slot.

`GET /api/v1/guild-score-submissions/{submission_id}` uses the same principal.
`GET /api/v1/guild-score-submissions/by-capture/{capture_run_id}` resolves the
same receipt without retransmitting images when the POST response was lost.
Both reads and POST replay require the complete stored principal scope to match
the current grant; reusing a principal ID for a different destination cannot
reveal older receipts.
States are `received`, `committed` or `rejected`; processing holds a database
claim while the visible state remains `received`. A committed result contains
week, capture provenance, counts, warnings and image hashes. Notification states
are separate: `pending`, `delivering`, `delivered`, `skipped`, `outcome_unknown`.
A network timeout cannot undo committed scores. Query the same ID, or repeat the
identical POST if its acceptance response was lost. Never generate a new ID to
work around uncertainty. No general image-read endpoint is exposed.

Every page must fall within the declared capture interval and the same Culvert
week. That week is derived from capture time, not HTTP arrival. A batch crossing
Thursday 00:00 UTC is rejected. Capture duration is at most three minutes; future
timestamps beyond two minutes are rejected. A delayed older batch cannot
overwrite a newer committed batch for the same tenant/guild/week. Capture times
remain provenance claims, not source-frame freshness proof. A newer manual
correction or week reset also rejects the batch with `newer_manual_change`.
The comparison uses capture start, so a correction during a multi-page capture
is protected. A database trigger records all existing score-writer routes;
its API-origin exemption is transaction-local. The processor checks again after
score locks resolve and rolls back every score and auto-track change before
recording a rejection. Migration 13 seeds prior score timestamps and runs in
normal startup even when the image integration is disabled.

All page validation finishes before writes. Character creation, changed scores,
the committed receipt and notification intent share one database transaction.
Pending delivery can resume after restart without rerunning score writes. A
crash or uncertain Discord result after delivery starts becomes
`outcome_unknown`; it is not automatically resent, because Discord message
creation is not idempotent. Existing real message IDs are retained when known.
No claim of exactly-once external Discord delivery is made. Automated REST
requests carry the delivery context and disable automatic REST retries.
Only an explicit Discord UnknownMessage permits replacing an existing message;
other edit failures preserve the identifier. Thread, archive and identifier
persistence failures propagate into the durable uncertain outcome.

Each principal may have at most three received batches and 512 MiB of retained
original image bytes. Terminal batches release their image bytes after seven
days once no delivery is pending/in flight; their receipts and original hashes
remain queryable. Capacity failures return 409 `integration_capacity`.

Server configuration is `CULVERT_IMAGE_INTEGRATIONS`, a JSON array of grants
with `id`, `token_sha256`, `collector_id`, `destination_id`, `maple_guild`,
`discord_guild_id`, `tenant_id`, `actor_id` and `expires_at_utc`. Discord guild
mapping must agree with the existing `data.TenantID` rules. No grants are enabled
or credentials generated by this source change.

HTTP failures use JSON `error.code`: 400 malformed input, 401 unavailable or
invalid credentials, 403 configured identity mismatch, 404 unknown/out-of-scope
receipt, 409 key/slot conflict, 413 byte/page/dimension bounds, 422 invalid or
cross-reset capture, and 503 storage unavailable. OCR rejection is durable in
the receipt. Server errors do not return private tokens, raw images or database
details.

The endpoint remains disabled until scoped credentials and the verified
ExampleGuild-to-Discord mapping are configured. Code tests and accepted PNG encoding
do not qualify grayscale guild-menu OCR, pagination or native autonomy.
