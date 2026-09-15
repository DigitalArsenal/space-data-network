# Production release plan — September 2026

Owner directive 2026-09-15: "get this production ready, have desktop and
docker builds, and update the website and onboarding page." This is the
working checklist; each line names the evidence that closes it.

## Gate

The node cut (records stream into FlatSQL, sdn `ca64d02c`) is verified on the
dev node and on both production hosts. The fleet is wiped and rolled. What is
still between that and a build a stranger can install and an operator can
run unattended:

| # | Item | Status | Evidence |
|---|------|--------|----------|
| 1 | $CAT on host-02 (authoritative ingest) | landed 2026-09-15 11:15Z: SATCAT fetched 200 after the throttle lifted | host-02 `fetch_events`, `/api/v1/stats` |
| 2 | Lane and publication alerting | in progress (lane `task/ops-alerts`) | `/health` degraded + counts, `/api/v1/status/alerts`, `sdn_alerts_active` metric, `OPS ALERT` log lines |
| 3 | Host flows rebuilt from current modules | satcat + spw deployed on both hosts; gp (Sep 10 build), mlab, cell-tower, satnogs, sigmf still July/August runtimes | `capability_policy.json` approvals per runtime sha256 |
| 4 | Search schemas regenerated for SDS 1.217.0 | landed `de913ed4` | `go test ./internal/sds/` |
| 5 | Docker image boots ready with AOT engine and a persistent identity | in progress | `docker run` smoke: `/api/v1/ready` 200, `engine mode: AOT`, PeerID stable across container recreation |
| 6 | Desktop app runs the bundled node (mac arm64 + x64, win x64, linux x64) | in progress (lane `task/desktop-node`) | DMG launches, node child, dashboard shown |
| 7 | Release v1.0.5-beta.1: CLI bundles, desktop, container tarball, checksums, notes | pending 5 + 6 | GitHub release + `spacedatanetwork-checksums.txt` |
| 8 | Website + onboarding: self-hosted assets, current install paths | in progress | spacedatanetwork.org loads with no third-party host; onboarding step 1 = desktop / docker / one-liner |
| 9 | Engine hot window cost at restart (Part B) | known cost, not a blocker: warm reconcile ≈ 12 s per 24k forgotten tombstones; mostly-dead arena rebuild ≈ 60 s | `flatsql-page-fsdata-not-slurp` |
| 10 | Restart rule for producers | documented: never restart a producer mid-fetch; the lane backoff blocks the re-fetch for hours | `celestrak-lanes-repair-2026-09-15` memory |

## Facts the release depends on

- Fleet binaries ship through the update feed (`publish-fleet-update.mjs`,
  build locally, no signal when the store layout changed). Desktop and Docker
  artifacts ship through the GitHub release (the documented beta pipeline in
  `docs/release-pipeline.md`); the Docker Hub token lives in the repo secrets.
- The engine artifact is unchanged since `flatsql-e1b8b120c2368a7b-we0.16.4`;
  no AOT prewarm is needed on the hosts. A container has no cache, so the image
  prewarms at build time.
- CelesTrak politeness: one request per file per three hours from one address.
  A forced re-fire inside that window earned host-02 a 403 for seven hours on
  2026-09-15.
