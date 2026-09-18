# FlatSQL stream delivery log

History moved out of `graph/tasks/sdn-stream-flatbuffers-into-flatsql.md`, which
must stay under 8 KiB and hold only objective, scope, acceptance and
verification (AGENTS.md). Everything below is the dated record of what was
measured, rolled out and released for that task — kept because the numbers and
the owner rulings in it are not reproducible from the code.

## Measured (2026-09-14, dev node, 72 GB store wiped + re-ingested)

Store open 469 ms crash-style / 332 ms warm; `/api/v1/ready` 19.09 s / 11.31 s
(baselines 1 h 18 m cold, 37 s warm, 5 m 06 s on 2026-09-10); records served
at open, engine window hydrates in the background (8.5 s / 3.0 s); CAT 62,108
rows = one per object. Verification also found and fixed an engine
partition-map leak across arena discards and a cold-rebuild double ingest
(survey doc, "Measured"); the dev node's partitions equal the ledger after the
boot. Landed: sdn `dcb457e6` + `f07f5e1a` + `7f5ccb15`.

## Fleet rollout (2026-09-15, owner "yes do it")

Both hosts wiped with `store-wipe` (host-02 freed 51 GB, host-01 76 GB) and
rolled by hand through the update feed (`update install --direct` in the
stopped state on host-02; in place then restart on host-01, which serves the
feed). No self-upgrade signal was pushed: the new layout needs the wipe before
the first start. Boots: host-02 ready 2 min (cold auxiliary replay of 155,474
frames), host-01 ready 51 s; engine AOT on both; identities and EPMs intact
(host-02's EPM came back from auxiliary.flatsqlmeta). The wipe exposed a
consumer bug from c25e0fa1 — every HD publication rejected as
`SIGNATURE_TYPE "Ed25519" does not match the provider's Secp256k1 key`,
2,122 rejections on host-01 in the 31 h before — fixed in sdn `4571952c` +
`ca64d02c`; OMM/MPE/IQC/RFB flow again. $CAT and $SPW are absent fleet-wide:
`celestrak-ingest-lanes-broken-fleetwide` (pre-existing lane failures).

## Production release (2026-09-15, owner: "get this production ready, have desktop and docker builds, and update the website and onboarding page")

- sdn `ec53c147` site + Docker + install path; `14b9e09b` ops alerts
  (`/health` JSON status + counts, `/api/v1/status/alerts`, `sdn_alerts_active`,
  `OPS ALERT` lines); `95dec837` desktop app runs the bundled node (mac arm64
  DMG verified: node child, ready in 24 s, dashboard, quit stops it);
  `18d3715f` 304 = unchanged success (runner `ErrRetrievalUnchanged`, ledger
  `RecordAttemptUnchanged`), current-batch reconcile accounting, channel DPM
  key encoding, p2p test fixtures; `.github/workflows/docker-publish.yml`
  rewritten (v* tags → Docker Hub `<version>` + `beta`).
- modules `283026f`: mlab / cell-tower / satnogs / sigmf answer 304 with an
  unchanged notice; mlab + cellular bundles staged on host-02 with approvals
  (restart pending with the fleet roll).
- host-02 $CAT landed 11:43Z (69,999 rows; the "parse 400" was the parser
  refusing CelesTrak's 403/304). Docker smoke: ready 10 s, AOT, wallet 200,
  identity persists with a fixed `--hostname` (fails closed without one).
- Codex quota exhausted until 2026-09-20; desktop, alerts and modules lanes
  ran on Claude agents. CI on main red since 09-09 (runner timeouts, absent
  public-query bundle) — the fixture/product defects behind it are fixed here.
- Release v1.0.5-beta.1: `cut-release-local.sh --publish` (darwin-arm64,
  linux-amd64, linux-arm64), plus the desktop DMG and the container tarball
  uploaded by hand; Windows/Intel/Linux desktop = next beta.
- 12:53Z: host-01's own celestrak lanes removed (double-writer with host-02's publications under provider space-data-network-02; owner placement ruling 2026-07-28); host-01 consumes host-02's signed publications only.
