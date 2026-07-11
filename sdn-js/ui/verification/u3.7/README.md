# U3.7 verification evidence — CHANNELS view wired

Captured 2026-07-11 by the coordinator against a locally built daemon
(isolated scratch home) at `http://127.0.0.1:15080/console/channels`,
live admin session.

## Files

- `console-channels-reference-1440x900.png` — the mock's CHANNELS route
  (four fixture channels with fabricated GRANTED/SEALED states).
- `console-channels-port-real-1440x900.png` — the port: all 173 real
  standard-topic channels (`ACL BROADCAST → Broadcast · NOT REQUIRED ·
  PLAINTEXT`), monitor panel with real fields (PUBLIC / — / NOT
  REQUIRED / ACL), honest "NO KEY ENVELOPE · public channel — payloads
  are not sealed to a recipient" box, OPEN SEALED STREAM + ENVELOPE
  disabled with honest tooltips.
- `console-channels-grant-issued-1440x900.png` — live grant issuance:
  `GRANT ISSUED · grant-25bf9f72f2811e58fc49ddf3f67a8494`.

## Ground truth recorded (per the tracker's scout requirement)

Channel routes (`internal/api/channels.go:140-141`, dispatch at 282):
`GET /api/v1/channels` (public collection; one row per supported
standard when no verified dataset publications exist — the live case);
`/api/v1/channels/{channelID}/{action}` with channelID =
`{sourceId}-{STANDARD}` (bare codes 400 in `ParseChannelID`), actions:
detail(""), monitor, pnm, subscribe, unsubscribe, publish, stream,
bytes, key-unwrap, shard-import, module-feed, cache, grants.
Grants: `POST /api/v1/channels/{id}/grants` `{to|subject, scopes,
expiresAt RFC3339}` → 201 `{grantId, channelId, subject, scopes,
grantState:"verified", issuedAt, expiresAt}`.

## Honesty mapping (intended deviations from the mock)

- GRANT column renders the server's real vocabulary (`NOT REQUIRED` /
  `REQUIRED` / `VERIFIED`), not the mock's fabricated GRANTED/OPEN.
- `encryptionState:"none"` renders dim `PLAINTEXT` — never the mock's
  "✓ SIGNED" (that claim isn't backed by this field).
- Channel names are the real `channelId` when one exists, else
  `{CODE} BROADCAST` — no fabricated provider slugs.
- Sealed-stream open is `[!]`-annotated: NO channel on this build has a
  sealed stream or key envelope (no private/verified publications), so
  OPEN SEALED STREAM/ENVELOPE are honestly disabled; the sealed path
  can only be exercised once a provider publishes a private channel.

## Live checks

- Grant round-trip: ISSUE GRANT form (sourceId defaults `local` →
  `local-ACL`, subject, scopes, optional RFC3339 expiry) → 201 verified
  grant rendered inline.
- Row selection updates the monitor panel; collection re-query with
  `?standardCode=` on selection.
- Console clean; requests all same-origin (channels collection +
  filtered detail + health + auth/me). Anonymous degradation and sealed
  fixtures unit-tested (82 tests).

## Residuals

- Server error envelope `{"error":{"message"}}` isn't parsed by
  SdnApiClient (expects `{code,message}`) — grant errors surface as
  generic HTTP codes; client-side parser alignment is a follow-up.
- No SUBSCRIBE control (read-only subscription state per spec).
