# C2 verification evidence — daemon serving swap (conjunction-only ship)

Loop `SDN_SPACEAWARE_UI_LOOP.md` Phase C, task **C2**. Captured 2026-07-13
(local-only under the no-deploy hold). Verified against a locally built daemon
serving the C1 embedded artifact, anonymously, in a real browser + curl.

## What C2 delivers

The daemon now ships the **conjunction app as THE UI**, served from the
embedded single-file artifact at the primary route `/`.

- `conjunction_ui.go`: `//go:embed embedded/conjunction_app.html` +
  `serveConjunctionUI` (config injection + COOP/COEP + no-store, mirroring the
  full-app handler) + a `uiMode` switch (`resolveUIMode()` reads `SDN_UI_MODE`).
- `makeUISurfaceHandler` (new dispatcher wired at `/` in main.go) selects by
  mode:
  - **conjunction (SHIPPED default)** — `makeConjunctionSurfaceHandler`: `/`
    and `/index.html` serve the conjunction bytes; every descoped SpaceAware
    screen (`/console/*`, `/orbital`, `/gantt`, `/bmc2/*`, the SpaceAware
    `/login` screen) returns 404 (code stays committed & dormant, serving
    stops); other paths fall through to the disk frontend handler.
  - **spaceaware (dev / re-enablement, `SDN_UI_MODE=spaceaware`)** — delegates
    to the **unchanged** `makeFrontendSurfaceHandler`, so the full SpaceAware
    route skeleton is served exactly as before Phase C. (Keeping that function
    untouched also means `main_test.go` — a mnemonic-guard trap file — is not
    re-staged.)
- `SetExternalLoginUI(frontendUIMode == uiModeSpaceAware)` — see the legacy
  surfaces note below.

## Route decision (and why)

- **Primary route = `/`.** The conjunction app mounts standalone at root and
  reads its only deep link (`?group=`) from the query string, which is
  preserved on `/`. No path-based routes are needed.
- **No `/console/conjunction` alias.** `/console/*` is the descoped console
  namespace; keeping one member alive while 404-ing the rest is inconsistent.
  `/console/conjunction` therefore 404s like every other descoped screen, and
  the conjunction experience lives only at `/`. This gives a uniform
  "conjunction is the whole UI, mounted at root; the console is gone" story.

## Legacy `/login` + `/webui/` decision (and why)

- **`/login` in conjunction mode reverts to the legacy wallet page**
  (`SetExternalLoginUI(false)`). In spaceaware mode U1.2's external-login-UI
  behaviour (the SpaceAware login screen owns `/login`) is preserved. Since the
  SpaceAware login screen is descoped, conjunction mode gives `/login` back to
  the legacy wallet-gated page, which stays mounted as the **wallet-creation /
  first-admin bootstrap** surface for operators. Verified live: `GET /login` →
  200 `text/html`. The conjunction app itself never authenticates (anonymous),
  so it does not link to `/login`.
- **`/webui/` unchanged** — the kubo IPFS dashboard (4.12.0) is a separate
  surface, still mounted. Verified live: `GET /webui/` → 200.

## Live scratch-daemon acceptance

Isolated scratch home (`--config <scratch>/sdn-c2-home/config.yaml`, never
`~/.spacedatanetwork`), admin `127.0.0.1:15080`, p2p `14001`/`18080ws`, storage
+ keys under the scratch home, data-retrieval flow installed + mounted at
`/api/v1/data/` (so `/api/v1/data/health` → ONLINE). Daemon boot log confirms
`SDN UI at http://127.0.0.1:15080/ ... (mode: conjunction ...)`.

### Real browser (chrome-devtools MCP, 1440×900, isolated anonymous context)

- `c2-primary-route-live-daemon-1440x900.png` — `/` serves the conjunction app
  with **real daemon data**: ONLINE health chip (live `/api/v1/data/health`),
  `PUBLIC · ANONYMOUS` session chip, real group pills from the shared groups
  store (LEO Constellation A pre-selected · 42 OBJECTS), honest single
  `Local SDN Catalog` DATA SOURCES row, SGP4 propagator with Numerical
  PAID-locked, DEMO tags on SCREENING·LIVE / RESULTS / PROVENANCE (D4).
- `c2-primary-route-group-deeplink-1440x900.png` — `/?group=iss-env`: ISS
  Debris Envelope pill pre-selected, strip `9 OBJECTS · MY GROUP`.
- **Console: clean** (zero messages) on both load and the deep-link navigation.
- **Network: zero non-same-origin requests.** All requests are same-origin
  (`127.0.0.1:15080`): document + `/api/v1/{peers,channels,stats}` +
  `/api/v1/data/health` (×2 poll) + browser-automatic `/favicon.ico` — plus
  inlined `data:` woff2 fonts. No script/stylesheet/websocket to any other host.
- **Anonymous, in-browser cross-check** (`fetch(..., {credentials:'include'})`
  from the page): `document.cookie` = `(none)`, `crossOriginIsolated` = `true`,
  and statuses `/ 200`, `/console/node 404`, `/orbital 404`,
  `/console/conjunction 404`, `/api/v1/{peers,channels,stats,data/health} 200`,
  `/api/v1/data/summary 401`, `/api/auth/me 401`. This also closes C1's
  live-daemon residual (conjunction app now verified against a real daemon).

### Curl cross-checks

Full transcript in `c2-curl-transcript.txt`. Summary:

- `GET /` → 200, `text/html`, COOP `same-origin` + COEP `require-corp`,
  `Cache-Control: no-store`, 230,806 bytes (230,683 artifact + injected
  `__SDN_CONFIG__`), contains `conj-root`.
- Descoped `/console/*`, `/orbital`, `/gantt`, `/bmc2*` → all **404**.
- `/api/v1/{peers,channels,stats,data/health}` anonymous → all **200**.
- `/api/v1/data/{summary,query}`, `/api/auth/me` anonymous → all **401**
  (RequireAuth wall from B10 unchanged — auth middleware untouched).
- `/login` → 200 (legacy wallet page), `/webui/` → 200.

## Go tests

`go build ./...` clean; full `cmd/spacedatanetwork` suite green. New focused
tests in `conjunction_ui_test.go`:

- `TestResolveUIModeDefaultsToConjunction` — env-var mapping, shipped default.
- `TestConjunctionAppEmbeddedArtifact` — embedded, self-contained (no external
  refs, no wasm), has `</head>` + `conj-root`.
- `TestServeConjunctionUI` — 200 + COOP/COEP + no-store + injected config;
  HEAD no-body; POST 405.
- `TestFrontendSurfaceHandlerConjunctionMode` — via `makeUISurfaceHandler`: `/`
  and `/index.html` serve the conjunction bytes; `?group=` preserved; all
  descoped screens 404; non-UI paths fall through to the disk frontend.
- `TestFrontendSurfaceHandlerSpaceAwareMode` — dev/full mode unchanged
  (dispatcher delegates to `makeFrontendSurfaceHandler`; SpaceAware routes serve
  `sa-root`; `/` falls through).
- `TestConjunctionDataSourcesStayAnonymous` — peers/channels/stats/health
  anonymous; summary/query/me gated.

`makeFrontendSurfaceHandler` and its existing `TestMakeFrontendSurfaceHandler*`
tests are left byte-for-byte unchanged (the new mode dispatch lives in
`makeUISurfaceHandler`), so `main_test.go` is not touched.

## Residuals (honest)

- Live verification of `SDN_UI_MODE=spaceaware` was **not** re-run against a
  daemon (would require a restart cycle); it is covered by
  `TestFrontendSurfaceHandlerSpaceAwareMode` + the original U1.x/U3.x live
  evidence for the full app. The conjunction shipped default is fully
  live-verified.
- The reused ConjunctionView's "OPEN IN 3D" button still targets the descoped
  `/orbital` (a documented no-op in the standalone app — C1 residual, unchanged).
- Deploy remains behind the no-deploy hold (owner lifts explicitly). No config
  mount changes are shipped by C2; the UI-mode switch is code-only + env-gated.
