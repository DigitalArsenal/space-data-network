# U1.2 verification evidence — Login wired real

Captured 2026-07-10 by the coordinator against a locally built daemon
(embedded artifact, **auth ENABLED**, isolated scratch config) at
`http://127.0.0.1:15080/login`. The screenshot shows the login rendering
REAL data: TRACKED/PEERS/FEEDS from `/api/v1/stats`, network chip from
`/api/v1/data/health`, footer `NODE v1.0.4` + this node's real peer ID from
`/api/node/info` (city dropped — no data source), SIM LINK row dropped.

## Route flip (Go half)

`auth.Handler.SetExternalLoginUI(true)` (wired in main.go): the exact
`/login` mux pattern is left to the "/" frontend surface → the embedded
SpaceAware login now wins on auth-enabled nodes; the legacy wallet-gated
page (still the wallet-creation surface, incl. its unpkg-CDN fallback) moved
to `/login/legacy`. Verified live: `/login` → SpaceAware title,
`/login/legacy` → legacy page + `createWalletUI`, `/api/auth/me` → 401 JSON
anonymously.

## Real-auth acceptance (all pass, real browser)

- **Real operator login end-to-end**: a test wallet (created by the real
  `createLocalWallet` in Node, injected as the passphrase-encrypted
  `sdn_spaceaware_wallets_v1` localStorage blob) unlocked with its
  passphrase → real challenge → Ed25519 sign → verify → **first-admin
  TOFU bootstrap** → `/api/auth/me` = `{"name":"Initial Admin",
  "trust_level":"admin"}` → `navigate('/console')` **passes the U0.3
  guard** → lands on `/console/node`. Step rows advanced on real
  authState stages (challenge/verify/confirmed) with min-dwell smoothing.
- **Wrong passphrase** → real `INCORRECT PASSPHRASE` banner (AES-GCM
  unlock failure), sequence stopped, back on form.
- **No wallet for operator id** → explicit `NO LOCAL WALLET…` banner
  (wallet creation stays on `/login/legacy` until a SpaceAware creation
  flow exists — recorded gap).
- **Node-key tab**: unknown/disconnected peer → real `HTTP 404` banner
  (residual polish: friendlier message text); a live connected peer
  resolved via `GET /api/v1/peers/{id}` → `/orbital`. Note: this scratch
  daemon serves the NATIVE peers route (auth-gated detail, legacy shape,
  no EPM fields — graceful nulls); prod's G.2 peers-discovery FLOW serves
  the anonymous EPM-shaped detail — re-probe at U3.3.
- **Telemetry cross-check vs curl**: TRACKED 18 = `total_records`,
  FEEDS 3 = `schemas[]` length, footer peer id/version exact; PEERS is a
  live-churning DHT count on a 30s refresh (sampled 166/215/200 within a
  minute — wiring correct).
- **Audits**: console completely clean (authed `me` = 200); network 100%
  same-origin (page + 3 inlined data: fonts + 4 API calls). Zero external.

Test wallet + session live only in the coordinator's browser profile and
scratch daemon; nothing sensitive committed (the blob is a throwaway test
wallet, and it is NOT in this directory).
