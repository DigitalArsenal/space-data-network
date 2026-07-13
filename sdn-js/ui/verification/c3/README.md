# C3 verification evidence — conjunction-ship close-out

Loop `SDN_SPACEAWARE_UI_LOOP.md` Phase C, task **C3**. Captured 2026-07-13
(local-only under the no-deploy hold). Verified against a locally built daemon
(isolated scratch home under an **overridden `$HOME`**, explicit `--config` —
never `~/.spacedatanetwork`; admin `127.0.0.1:15080`, p2p `14001`/`18080ws`,
`SDN_UI_MODE` unset ⇒ conjunction shipped default) in a real browser
(chrome-devtools MCP, isolated anonymous context `c3-anon`, 1440×900) + curl.

## What C3 delivers

1. **CSP header** on the served conjunction UI (`conjunction_ui.go`):

   ```
   default-src 'self'; base-uri 'none'; object-src 'none';
   frame-ancestors 'none'; form-action 'none';
   script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline';
   img-src 'self' data:; font-src 'self' data:; connect-src 'self'
   ```

   Rationale: the artifact is a single self-contained document (1 inline
   module script + serve-time `__SDN_CONFIG__` script, 1 inline style block,
   8 `data:font/woff2` faces, no workers/blob/wasm/forms — audited below), so
   the load-bearing directive is **`connect-src 'self'`** (blocks any
   exfiltration/third-party beacon at the browser layer, complementing the
   build-time forbidden-host audit). `'unsafe-inline'` for script/style is
   REQUIRED by the single-file packaging hard rule (inline module + Svelte
   inline `style=""` attributes); a nonce/hash pass is noted future hardening
   and does not weaken the connect-src exfiltration guard. No
   `wasm-unsafe-eval` (no wasm ships, C1), no `worker-src` (no workers).
   Enforced by `TestConjunctionCSP` + assertions in the existing serve tests.

2. **"OPEN IN 3D" disposition: HIDDEN in the standalone build.** The reused
   `ConjunctionView`'s "3D" button targets descoped `/orbital?group=` — an
   inert no-op since C1. New `show3dLink` prop (default `true`, so the full
   SpaceAware console codepath is UNCHANGED) threads
   `ConjunctionView → ConjunctionTaskPanel`; the standalone `ConjunctionApp`
   passes `conjunctionAppShows3dLink()` (`false`, pure + unit-tested in
   `lib/conjunction-app.ts`). The `navigate()` descoped no-op stays as a
   documented defensive path. Browser-verified: `.sdn-conj-3d-btn` count = 0
   at `/` and `/?group=iss-env`.

3. **Cutover-contract updates** (`sdn-js/src/ui/upstream-webui/
   cutover-contract.test.ts`): file manifest brought current (+4 C1 files:
   `ConjunctionApp.svelte`, `conjunction-main.ts`, `lib/conjunction-app.ts`
   + test — the manifest test was RED before C3); new
   "conjunction-only ship surface" describe block locks primary route `/`,
   the full descoped-route 404 set (derived from `SPACEAWARE_ROUTES`, so a
   route re-registration fails the test), `/console/conjunction` as the only
   in-app alias, and legacy `/login` as bootstrap-only (never in-app). The
   daemon-side half stays enforced in Go
   (`TestFrontendSurfaceHandlerConjunctionMode`,
   `TestConjunctionDataSourcesStayAnonymous`).

## Audit sweep results (shipped artifact + serving)

- **Console: clean** — zero messages on load, after popover/interactions, and
  on the `?group=` deep link. No CSP violation reports (the policy does not
  break the inline single-file build).
- **Network: zero non-same-origin requests** — document +
  `/api/v1/{peers,channels,stats}` + `/api/v1/data/health` + favicon, all
  `127.0.0.1:15080`; fonts are inline `data:` URIs.
- **No document-level scrollbars at 1440×900** — scrollWidth=clientWidth=1440,
  scrollHeight=clientHeight=900 on both `/` and `/?group=iss-env`.
- **Tooltips/popover** — 32 `title` tooltips (health chip, session chip,
  target pills, steppers, source arrows…); ONE-OFF RUN popover opens/closes
  with DEMO tag, LOOK-BACK stepper, RUN ONCE (screenshot).
- **Self-containment (C1-style re-audit of the rebuilt artifact)**: zero
  `<script src=`/stylesheet links/workers/importScripts/serviceWorker/blob:/
  wasm/eval/new Function/forms/CDN-font hosts; only inert string refs
  (`http://www.w3.org` SVG namespace ×1, `https://svelte.dev` error-doc URLs
  ×13); 1 inline script, 1 inline style, 8 data-URI fonts.
- **crossOriginIsolated = true**, `document.cookie` empty (anonymous).

## Artifact size (rebuilt for the show3dLink change)

- raw: 230,683 → **231,085 bytes** (+402 B, +0.17%); gzip 132,785 B
  (129.7 KiB). Served with injected config: 231,208 B.

## Files

- `c3-conjunction-standalone-1440x900.png` — `/` live daemon, real data,
  ONLINE + PUBLIC · ANONYMOUS chips, no 3D button.
- `c3-oneoff-popover-1440x900.png` — ONE-OFF SCREEN popover open (DEMO).
- `c3-curl-transcript.txt` — headers incl. CSP; descoped 404 set; anonymous
  200s; gated 401s; `/login` + `/webui/` 200.
- Deep-link screenshot: capture repeatedly timed out in the MCP rig
  (3 attempts) — the deep link is DOM-verified in this run (ISS Debris
  Envelope pill pre-selected, `9 OBJECTS · MY GROUP` strip, no scrollbars,
  clean console) and pixel-covered by C2's
  `c2-primary-route-group-deeplink-1440x900.png` (identical view, pre-C3
  the only visual delta is the now-hidden 3D button).

## Docs + desktop decision (C3 items 4–5)

- `README.md` "Current UI Surfaces": conjunction app at `/`,
  `SDN_UI_MODE=spaceaware` dev switch, legacy `/login` bootstrap role.
- `sdn-server/docs/gateway-api.md` §4: short "shipped UI consumer" context
  note (anonymous, same-origin-only consumer of the public allowlist).
- `docs/docs.html` checked — its only UI claim is about the Desktop app's own
  bundled UI (still true); NOT touched.
- Desktop packaging decision recorded in `desktop/DEVELOPER-NOTES.md`:
  **needs work, release-gated** — the dashboard loads its own stale bundled
  `assets/sdn-ui/` via a custom `sdn:` protocol, NOT the daemon-served UI;
  recommended future fix is to wrap the daemon's `/`. No desktop code changed.

## Residuals (honest)

- CSP uses `'unsafe-inline'` for script/style (packaging-forced); nonce/hash
  hardening is future work.
- Deep-link screenshot missing from this evidence set (rig timeout — DOM
  verification + C2 pixel evidence stand in).
- `SDN_UI_MODE=spaceaware` still verified by tests only (unchanged C2
  residual).
- Desktop conjunction delivery not implemented (decision recorded only).
- Deploy remains behind the no-deploy hold.
