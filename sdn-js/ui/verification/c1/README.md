# C1 verification evidence — CONJUNCTION-only ship artifact

Loop `SDN_SPACEAWARE_UI_LOOP.md` Phase C, task **C1**. Captured 2026-07-13
(local-only under the no-deploy hold).

## What C1 delivers

A new build target `npm run build:conjunction` (sdn-js) with its own entry
(`ui/conjunction.html` → `src/spaceaware/conjunction-main.ts` →
`ConjunctionApp.svelte`) that mounts the **reused** `ConjunctionView`
standalone at `/` with only a header status strip for chrome — no console
rail, no routes to descoped screens, no login. The single-file inliner
(`scripts/build-conjunction-single-file.mjs`) writes the self-contained
artifact to `sdn-server/cmd/spacedatanetwork/embedded/conjunction_app.html`
(sibling of `spaceaware_app.html`; NO Go serving code touched — C2 owns the
serving swap). Per the Phase C banner this artifact becomes the inline
`CONTENT` of the conjunction APP record later.

## Size (honest)

| Artifact | Raw | Gzip |
| --- | --- | --- |
| `conjunction_app.html` (this ship) | **230,683 B (225.3 KiB)** | **132,558 B (129.5 KiB)** |
| `spaceaware_app.html` (full app, for reference) | 6,400,760 B (6.10 MiB) | — |

Composition: inlined JS 73.7 KiB + inlined CSS 151.2 KiB (the CSS is almost
entirely the 8 vendored woff2 font faces as `data:` URIs). ~28× smaller than
the full app. The build FAILS if a wasm blob is present (see audit below).

## No hd-wallet wasm (the key packaging win)

The old full app embedded ~5 MB of wallet wasm (as a `data:` URI) for its
host-side credential flow. Phase 1A deleted that production import chain, so
the conjunction build no longer relies on an alias or empty stub to exclude
the implementation. Two independent guards keep it out:

1. `ui/src/lib/public-wallet-boundary.test.ts` rejects protected credential or
   crypto modules in the active and dormant app graphs.
2. `build-conjunction-single-file.mjs` HARD-fails the build if a raw `\0asm`
   signature, a base64 `AGFzbQ` wasm data URI, or an `application/wasm`
   reference appears in the artifact.

Independent binary check of the written artifact:
`wasm magic (\x00asm) = 0`, `AGFzbQ = 0`, `application/wasm = 0`,
`hd-wallet / mnemonic references = 0`. The only `http(s)://` strings in the
bundle are Svelte's inert runtime error-message URLs (`svelte.dev/e/...`) and
the XML namespace constant — never fetched.

## Browser acceptance (chrome-devtools MCP, viewport 1440×900)

Served via a local static server (`scratchpad/conj-verify-server.mjs`) that
serves the artifact at `/` (with the daemon's `__SDN_CONFIG__` injection
mimicked) and stubs the four anonymous, same-origin data sources
(`/api/v1/peers`, `/api/v1/channels`, `/api/v1/stats`, `/api/v1/data/health`).
The daemon serving swap is C2 — C1 is permitted to serve via any local static
means.

- `conjunction-standalone-load-1440x900.png` — first load: minimal header
  strip (kicker + `CONJUNCTION · PRIVATE SCREENING`, ONLINE health chip,
  fixed honest `PUBLIC · ANONYMOUS` chip — no rail, no login, no peers
  link), real group pills from the shared `sdn_shared_groups` store, the
  honest single `Local SDN Catalog` DATA SOURCES row, SGP4 propagator with
  Numerical PAID-locked, and DEMO tags on SCREENING·LIVE / RESULTS /
  PROVENANCE per decision D4. Pixel-faithful to the U3.9 console conjunction
  view (same reused components), minus the console shell chrome.
- `conjunction-standalone-group-deeplink-1440x900.png` — `/?group=iss-env`:
  ISS Debris Envelope pill pre-selected, strip shows `9 OBJECTS · MY GROUP
  defined by THIS NODE`.

### Interactions verified live

- Group pill switch: LEO Constellation A → GEO Belt Watch updates the strip
  from real store data (`18 OBJECTS`).
- `?group=` deep link pre-selects the group on mount (ISS Debris Envelope).
- Results TABLE → JSON: schema-exact keys render
  (`object/tca/missDistanceKm/pc/state`).
- PAUSE STREAM → `STREAM PAUSED` / `no deltas while paused` → RESUME; live
  ticker animates while running.
- ONE-OFF RUN popover opens (LOOK-BACK stepper + RUN ONCE), DEMO-tagged.

### Audits

- **Console: clean** (zero messages) on both load and the deep-link
  navigation.
- **Network: zero non-same-origin requests.** All requests are same-origin
  (document + peers + channels + stats + health) plus inlined `data:` woff2
  font URIs. A same-origin `/favicon.ico` (204) is the browser's automatic
  request, not app-initiated.

## Residuals / deviations (honest)

- The reused ConjunctionView's "OPEN IN 3D" button targets the descoped
  `/orbital` route, which is not bundled in this ship. The standalone app's
  `navigate()` is a documented no-op for descoped targets
  (`classifyConjunctionAppNav`), so that one button is inert here. Not in
  C1's required interaction list; noted for C2/C3.
- DATA SOURCES rightly shows only `Local SDN Catalog` — the stub peers carry
  no EPM `dn` and no sealed MPE channel exists, matching the live U3.9
  finding. Provider/sealed rows are unit-proven in `conjunction-data.test.ts`.
- Browser acceptance used same-origin API stubs rather than a full scratch
  daemon (permitted for C1); the real-data wiring is unchanged from U3.9,
  which verified it against a live daemon.
