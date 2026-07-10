# U1.1 verification evidence — Login screen pixel port (mock-staged)

Captured 2026-07-10 by the coordinator against a locally built daemon serving
the embedded single-file artifact (`spaceaware_app.html` via `go:embed`,
`serveSpaceAwareUI`) at `http://127.0.0.1:15080/login`, isolated scratch
config (auth routes disabled so `/login` reaches the SpaceAware surface — see
`spaceaware_ui.go` header: with HD-wallet auth enabled the legacy wallet login
mux pattern wins `/login` until U1.2 deliberately replaces it).

Reference ground truth: `design_handoff/login/Login.dc.html` from
`design/SpaceAware.io.zip` (sha1 `ba6f928d602ebcb63cd666dbaf8bcca554aa45c1`),
opened as `file://`, same viewport, fresh reload per size.

## Files

| file | what |
| --- | --- |
| `login-port-1440x900.png` | Svelte port served by the daemon, 1440×900 |
| `login-reference-1440x900.png` | `Login.dc.html` reference, 1440×900 |
| `login-port-1920x1080.png` | Svelte port served by the daemon, 1920×1080 |
| `login-reference-1920x1080.png` | `Login.dc.html` reference, 1920×1080 |

Expected diffs (intentional): the port omits the
`PROTOTYPE · ANY CREDENTIALS ACCEPTED` line (task requirement — the centered
stack sits ~12px higher as a result); port screenshots show a prefilled
operator ID + checked remember box because the remember-me interaction test
ran before capture (localStorage `sa_remembered_operator`).

## Interaction results (all pass, real browser)

- Empty operator submit → `⚠ OPERATOR ID AND PASSPHRASE REQUIRED`, both
  fields red; tab switch clears the error.
- Node key `not-a-valid-peer-key` → `UNRECOGNIZED PEER KEY FORMAT`, textarea
  red; `16Uiu…`/`12D3Koo`//ip4//dns prefixes accepted.
- Mock staged auth: 3 step rows (→ ✓ OK at 700/1450/2150ms), green
  `ACCESS GRANTED · LOADING SDN CONSOLE` banner, `navigate('/console')` at
  2900ms. The U0.3 session guard then bounces the unauthenticated mock back
  to `/login` — correct system behavior (verified `/console` direct nav
  bounces identically); the real session that satisfies the guard is U1.2.
- Remember-me: persists to `localStorage['sa_remembered_operator']`,
  prefills + prechecks on fresh load.
- REQUEST ACCESS toggles the provisioning info note on/off; EXPLORE PUBLIC
  CATALOG links `/orbital`.
- Starfield determinism: star alphas sampled at 10 algorithm-predicted
  coordinates match between port and reference (7/10 byte-identical, rest
  within antialiasing tolerance of the tiny-radius stars) — same seed-42
  Park-Miller LCG, 5 draws/star, at each page's own canvas size.
- Fixed viewport: document/body not scrollable at either size.

## Audits

- Network: 6 requests — `/login`, 3 inlined `data:font/woff2` URIs (vendored
  fonts), `/api/auth/me`, `/favicon.ico`. **Zero non-same-origin requests.**
- Console: clean except one `404` on `GET /api/auth/me` — an artifact of the
  verification daemon having auth routes disabled; U0.3's hydrate treats it
  as unauthenticated by design. The form-field id/name advisory found on the
  first pass was fixed (ids/names/autocomplete on all three fields) and the
  artifact rebuilt before final capture.
- COOP/COEP headers present on the served artifact (`same-origin` /
  `require-corp`).
