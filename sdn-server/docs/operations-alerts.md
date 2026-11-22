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

The alert names each failed scope of the lane; for a failed batch, whether a
completed publication of the whole batch exported what it holds:

```
dataset publication OMM.fbs celestrak/celestrak-gp batch "9f2c…" failed at step "export" after 1.2s: …
(batch "9f2c…" is unpublished: no publication of the whole batch completed; it holds 30871 records; a publication of a failed batch whole (POST /api/v1/admin/dataset-updates/publish with its "batchId") publishes its records and clears it)
```

A completed publication is one whose every window was announced and whose
completion was recorded as a series of the lane. A publication that failed
partway (at a later window's announcement, at recording its completion, or
cut by a crash) completed nothing, whatever rows and announced windows it
left: each window's row is written together with an unfinished mark (one
transaction), the marked windows never count as a publication of the batch,
are not announced again by an announcement of existing publications
(`announceExisting`), and the lane's retention releases them like any row no
series names (the mark goes with the row). Node 1.1.11 and earlier adopted such windows
as a series of their batch and kept them; retention now unpins them.

The store decides when the alert clears. A failed publication of a whole
batch X stays failed until one of two things is true of X itself:

- a completed publication of the whole of X exported the set X holds now:
  the fingerprint of that publication's set equals the fingerprint of the
  set a publication of X would export now (on a lane whose publications
  export the publication journal's retained selection, that set is the
  selection); or
- X holds no records any more: a replace-current lane superseded them, they
  expired, or the lane was retracted. The log says so when the alert clears.

A publication of any other batch never clears X, whatever records it shares
with X, and neither does a publication of the whole lane. So if every
record of a failed batch also sits in other, published batches (a later
scheduled run's pull returned them again), the alert still stays until that
batch is published whole or its records go: a publication of the batch
whole (`POST /api/v1/admin/dataset-updates/publish` with its `batchId`)
clears it. A batch stored in parts that gained records after its
publication is not published until it is published whole again.

X is checked from the store at each failure of X, after each completed
publication of the whole of X, when a supersede or an expiry removes records
of X's lane, and at start, once the node answers ready (and an update's
health trial is over), so the checks delay neither. A completed publication
of another batch checks nothing. A check reads X only: one fingerprint of X
(the read a publication of X makes to fingerprint it) and the lane's series
rows of X; nothing in it grows with the lane or with the number of its other
batches, and nothing is kept from one check to the next. At most one check
per failed batch is waiting at a time. Checks run off the publication's path,
one at a time, on work the node's stop cancels (within the stop's 10 s bound
for publications), and hold no lock that ingest or a publication waits on.
The alert says `(it is being checked)` until the first check of a batch ends.

A completed publication of the whole of X that read the lane after the
failed one did removes X's scope when it is reported, before its caller is
answered, without a store read. The check that follows compares the
fingerprint of what that publication exported with X as it is now; when
records arrived in X while the publication ran, X's scope is recorded again
and the alert raised again, saying how many of X's records arrived after its
last completed publication and are in no publication of it. The scope's row
in the store stays until that check confirms the publication, so a node
stopped before the check raises the alert again at start, and its first
check after the node is ready clears it. A publication
of a head of X (`limit`), of X on a datastore lane, or an announcement of
existing publications (`announceExisting`) never removes X's scope.

A retraction of the lane (`POST /api/v1/admin/dataset-updates/retract` with
`apply=true`) removes every failed scope of the lane, batches and lane heads
alike, when it marks the lane retracted: a retracted lane is never published
again.

A batch the snapshot guard refused (step `snapshot guard`) is refused again
by a publication of it whole while it is under the guard's floor, so its
alert says what clears it instead: the batch holding no records any more (a
newer batch supersedes it on a replace-current lane), or a publication of
the whole batch once the guard no longer refuses it (`min_record_ratio` /
`min_records` lowered or removed for the lane).

Every other failed scope clears when the same operation on the same scope
completes after it, having read the lane after the failed one did:

- a publication of a head of a batch (`limit`) or of the lane's newest
  records (no batch, no chunk size): a publication of the same kind;
- a publication of the whole lane (no batch, `fullCatalog` or a
  `chunkSize`): a publication of the whole lane, which also clears a failed
  head of the lane;
- an announcement of existing publications (`announceExisting`): such an
  announcement of the same batch;
- a batch of a datastore lane, or of a store whose batches cannot be
  checked (format 2): a publication of the whole batch.

A publication of a head never clears a failed batch: it exported only some
of the batch's records.

The alert clears when no failed scope of the lane is left. Removing the
scope and clearing the alert are one step under the lock a failure's raise
takes, so a failure raised meanwhile is never cleared with them. At most 16
failed batches are tracked per lane (each is one check, one fingerprint of
the batch, at each of its events); a failure beyond them is not checked, the
alert says so, and it stays until a publication of the whole lane completes.

The failed scopes are written to the store (`sdn_dataset_publication_failed_scopes`)
before the alert is raised and removed after it clears, so a restart raises
the alert again at start, checks the batches once the node is ready, and
clears or keeps it by the same rules. The auto-publisher tries a failed
batch up to 6 times and then leaves it to the operator. A lane whose
batches are declared snapshots (`publishing.retention.lanes[].snapshot_batches`)
whose retention released a batch's only completed series counts that batch
as unpublished until it is published whole again or its records go.

Alerts are rebuilt at boot: the node seeds `lane_failing` from `app_attempts`
rows with `consecutive_failures >= 2` (`sourcemetrics.SeedLaneAlerts`), so a
restart does not hide a lane that has been down for days, and raises
`publication_failed` again from the failed scopes in the store (above).

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
