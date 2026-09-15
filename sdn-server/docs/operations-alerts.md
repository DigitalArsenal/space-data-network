# Operational alerts

On 2026-09-13..15 the fleet spent 31 hours rejecting every dataset publication
(2,122 signature-type rejections on host-01) while its CelesTrak lanes failed 48
times in a row. Every one of those failures was already in the logs and in the
retrieval ledger. Nothing surfaced them, so nobody noticed.

The node now keeps an in-memory registry of what is currently wrong with it
(`internal/ops`). An alert is keyed by `(kind, subject)`: the class of failure
and the thing that is failing. A repeat bumps `count`, `last_error` and
`last_at`; it never resets `since`, because how long a thing has been broken is
the number an operator acts on. A success clears it.

## Kinds

| Kind | Subject | Raised where | Cleared where |
| --- | --- | --- | --- |
| `lane_failing` | app id | `internal/sourcemetrics` `RecordAttemptOutcome`, at 2 consecutive failures (warning) and 4 (error) | a batch lands (`RecordIngest` → `clearAppFailuresLocked`) |
| `publication_rejected` | producer peer id, short form | `internal/node`: the pubsub handler (a wrapped `protocol.ErrPNMAnnouncement`), the TipQueue materializer, the stored-PNM catch-up | `Materialized trusted dataset update from …` |
| `flow_capability_denied` | plugin id | `internal/modulert` `checkCapabilityPolicy` refuses a module or flow bundle | the same plugin passes the gate |
| `engine_poisoned` | — | `internal/node`, around `store.RecoverPoisonedEngine()` after a trapped hot-window hydration | recovery returns without error |
| `engine_rebuilding` | — | the same window, in which the read gate answers `storage.ErrEngineRebuilding` | `RecoverPoisonedEngine` returns |
| `update_failed` | update id | `internal/node` update-signal lane, when the self-upgrade does not start | the swap is handed to the helper |

Severity is `warning` or `error`. Nothing else.

Alerts are rebuilt at boot: the node seeds `lane_failing` from `app_attempts`
rows with `consecutive_failures >= 2` (`sourcemetrics.SeedLaneAlerts`), so a
restart does not hide a lane that has been down for days.

## Surfaces

**`GET /health`** — anonymous, always HTTP 200 while the process serves. The
status code is deliberately unchanged by degradation: a failing CelesTrak lane
is not a reason for a load balancer to pull the node out of rotation. Counts
only — a probe never sees a subject.

```json
{"status":"degraded","alerts":{"error":1,"warning":1}}
```

**`GET /api/v1/status/alerts`** — operator session, the same gate as
`/metrics`. The full list, errors first, then kind, then subject.

```json
{"alerts":[{"kind":"publication_rejected","subject":"12*ab12cd","severity":"error",
  "since":"2026-09-13T04:11:02Z","count":2122,
  "last_error":"SIGNATURE_TYPE \"Ed25519\" does not match the provider's Secp256k1 key",
  "last_at":"2026-09-15T11:07:43Z"}]}
```

**`/metrics`** — `sdn_alerts_active{kind="…",severity="…"}`, one series per
kind and severity, read live at scrape time so a recovered lane's series
disappears instead of going stale at 1.

**Logs** — every transition is one line, ERROR on raise and INFO on clear,
prefixed `OPS ALERT`:

```
journalctl -u spacedatanetwork | grep "OPS ALERT"
```

A plain repeat does not log; an escalation from `warning` to `error` does.

Stored publications that no known key verifies are not `publication_rejected`:
the stored-PNM catch-up quarantines such a frame after one look (one `WARN`
line naming its CID and the peer it was stored from) and never re-verifies it
until the node restarts. The alert is for a producer refusing the node now — a
live announcement the pubsub loop rejected, or a catch-up that failed for a
reason that can change (the producer unreachable, a shard missing).
