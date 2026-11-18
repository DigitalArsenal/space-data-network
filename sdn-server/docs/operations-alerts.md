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
| `publication_failed` | lane: `<schema> <provider>/<source>` (or `<schema> datastore <key>`) | `internal/api` `reportDatasetPublication`, when one of the node's own dataset publications fails or is cut | no failed scope of the lane is unpublished (below) |

Severity is `warning` or `error`. Nothing else.

### When `publication_failed` clears

The alert names each failed batch of the lane and how many of its records no
completed publication of the lane carries:

```
dataset publication OMM.fbs celestrak/celestrak-gp batch "9f2c…" failed at step "export" after 1.2s: …
(batch "9f2c…" is unpublished: 2104 of its 30871 records are in no completed publication of the lane)
```

A completed publication is one whose every window was announced and whose
completion was recorded as a series of the lane. A publication that failed
partway (at a later window's announcement, at recording its completion, or
cut by a crash) completed nothing, whatever rows and announced windows it
left: its windows are marked unfinished when written, never count as a
publication of the batch, and the lane's retention releases them like any
row no series names.

It clears when nothing that failed is still unpublished:

- the failed batch itself, or the whole lane (a publication with no batch),
  is published with at least one record; or
- every record of the failed batch is in a completed publication of the lane.
  This is how a later scheduled run clears it: a pull that returns a record
  again tags it to the later batch, and that batch's publication carries it.

Which records are published is worked out after the publication was reported
done, off the publication's path, one job at a time, on work the node's stop
cancels. When a batch fails, its records are counted once: one read of the
batch's record list and of their tags, a page of 2,048 at a time. A record
counts as published when it is tagged to a batch with a completed
publication of its whole batch. The records found unpublished (at most
200,000 per batch; a batch with more keeps its alert until the batch or the
lane is published) are kept in memory, and after each completed publication
of a whole batch of the lane the published batch's record list (no tags) is
read once and its records are taken off them. The alert says
`(its records are being counted)` until the first count ends.

A record of the failed batch that no later pull returned (an element set its
publisher has since replaced) is in no publication, so the alert stays; after
each publication of the lane its text says how many records are still
unpublished. A publication of the failed batch
(`POST /api/v1/admin/dataset-updates/publish` with its `batchId`) clears it;
that publication is then the lane's newest. A publication of an empty batch,
or an announcement of existing publications, clears nothing. A lane whose
batches are declared snapshots (`publishing.retention.lanes[].snapshot_batches`)
counts a record whose only completed publication is a series its retention
released as unpublished when the batch fails, so its alert may stay where a
lane of parts would clear. The failed scopes and the records followed are
kept in memory: a restart drops the alert, and the failed batch's records
stay as they were.

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
