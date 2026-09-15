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
| 1 | $CAT on host-02 (authoritative ingest) | done 2026-09-15 11:43Z: 69,999 CAT rows, lane failure counter 0 (the earlier "parse 400" was the parser refusing CelesTrak's 403) | host-02 `/api/v1/stats`, `app_attempts` |
| 2 | Lane and publication alerting | landed `14b9e09b` (2026-09-15): `/health` JSON `status` + counts, `/api/v1/status/alerts`, `sdn_alerts_active`, `OPS ALERT` log lines; unchanged (304) runs no longer count as failures | `/health` degraded + counts, `/api/v1/status/alerts`, `sdn_alerts_active` metric, `OPS ALERT` log lines |
| 3 | Host flows rebuilt from current modules | satcat + spw deployed on both hosts; host-02 mlab and cell-tower fail because their runtimes refuse HTTP 304 (lane `modules-304`: parsers treat 304 as unchanged, rebuild, redeploy); satnogs and sigmf run | `capability_policy.json` approvals per runtime sha256 |
| 4 | Search schemas regenerated for SDS 1.217.0 | landed `de913ed4` | `go test ./internal/sds/` |
| 5 | Docker image boots ready with AOT engine and a persistent identity | done 2026-09-15: ready in 10 s, `engine mode: AOT`, `/wallet-ui` + `/wallet-wasm` 200, same PeerID after recreation on the same volume with a fixed `--hostname`; without it the node fails closed (documented) | `deployment/docker/Dockerfile`, docs/INSTALL.md |
| 6 | Desktop app runs the bundled node (mac arm64 + x64, win x64, linux x64) | landed `95dec837`: mac arm64 DMG built and launched (node child, `/api/v1/ready` 200 in 24 s, dashboard shown, quit stops the child); x64 / win / linux packaging needs the workflow runners | `desktop/scripts/build-local.sh`, `desktop/dist/` |
| 7 | Release v1.0.5-beta.1: CLI bundles, desktop, container tarball, checksums, notes | pending 5 + 6 | GitHub release + `spacedatanetwork-checksums.txt` |
| 8 | Website + onboarding: self-hosted assets, current install paths | committed `ec53c147` (push with the release): no third-party host, downloads for v1.0.5-beta.1, onboarding step 1 = desktop / installers / docker | docs/index.html, docs/onboarding.html, docs/wallet-callback.html |
| 9 | Engine hot window cost at restart (Part B) | known cost, not a blocker: warm reconcile ≈ 12 s per 24k forgotten tombstones; mostly-dead arena rebuild ≈ 60 s | `flatsql-page-fsdata-not-slurp` |
| 11 | CI on main | red since 2026-09-09 (runner timeouts, the public-query bundle absent in CI, stale test fixtures): the fixture and product defects behind `internal/modulert/caps`, `internal/api` channel publish and the 304 lanes are fixed in this cut; the 20-minute package timeouts and the missing bundle are CI-environment work | `gh run list --workflow ci.yml` |
| 10 | Restart rule for producers | documented: never restart a producer mid-fetch; the lane backoff blocks the re-fetch for hours | `celestrak-lanes-repair-2026-09-15` memory |

## Facts the release depends on

- Codex is out of quota until 2026-09-20; the desktop, alerts and modules lanes
  run on Claude agents instead.
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
