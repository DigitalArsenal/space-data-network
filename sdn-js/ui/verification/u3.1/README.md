# U3.1 verification evidence — console shell + NODE dashboard

Captured 2026-07-10 by the coordinator against a locally built daemon
serving the embedded artifact (isolated scratch config) at
`http://127.0.0.1:15080/console/node`, side-by-side with
`design_handoff/sdn_console/SDN Console.dc.html` (`file://`, same
1440×900 viewport, fresh loads). Session: real test wallet, first-admin
TOFU-bound, live session cookie (`/api/auth/me` 200 on every load).

## Files

- `console-node-{reference,port}-1440x900.png` — pixel comparison pair.
- `console-node-port-1920x1080.png` — port at the second viewport.
- `console-node-editmode-1440x900.png` — edit mode (dashed outlines,
  ⠿/⤢/✕ edit bars, RESET/DONE, ADD WIDGET tray).
- `console-node-layout-persisted-1440x900.png` — custom layout after a
  full reload.
- `console-node-qr-overlay-1440x900.png` — QR export overlay.
- `console-rail-hover-expanded-1440x900.png` — rail hover-expanded over
  the PEERS view.
- `console-peers-placeholder-deeplink-1440x900.png` — landed via
  `?route=peers` deep link.

## Pixel results

ImageMagick `compare -fuzz 8%`: 13 214 differing pixels (1.02% of the
frame), every one in an intended/recorded region:

- **PEER MAP interior** (the bulk): the reference globe + arcs are a
  script-driven canvas animation; the U3.1 widget ships the header,
  tabs, stats, legend and frame with an inert interior — wiring is
  U3.4. The canvas-rendered legend row also anti-aliases differently
  than the port's DOM legend, and the header `16 LINKS` chip sits
  1.1 px left of the reference (sub-pixel width difference in the same
  right-anchored flex row) — both recorded as U3.4 pickups.
- **RESTART / STOP / CHECK** in SERVICE: rendered disabled (opacity
  0.45) per D6 — daemon lifecycle is owned by systemd/desktop, so the
  verbs are present but inert, with explanatory tooltips.

Everything else is pixel-clean. Coordinator fixes applied during this
pass (all verified against ground-truth computed styles):

- IDENTITY card sat 8 px low / CONFIRMED chip 4 px off: the shared
  widget-title class carries `margin-bottom: 12px`, which inflates the
  flex header row's box; ground truth has no margin on the in-row
  title. Scoped override added.
- SERVICE card content sat 10 px low: RUNNING reused the 31 px/0.06em
  ONLINE status class; ground truth renders it 24 px/0.05em.
- SERVICE version sub-line had letter-spacing 0.04em; ground truth has
  none (NODE HEALTH's sub-line keeps 0.04em).
- Throughput `0.88` up-value rendered 600 weight; ground truth is 400.
- Rail glyph icons (◉ ◍ ⬡ ▤ ⧉ ⊘) rendered via IBM Plex Mono, drawing
  visibly smaller than the reference's font-unstyled spans (system
  Arial). Icon spans now use the Arial stack (system font — nothing
  fetched).

## Interactions

- **Edit mode**: span cycle W4→W6 (IDENTITY), remove (THROUGHPUT), add
  (PEER SUMMARY), drag-reorder (SERVICE ahead of IDENTITY) — all
  reflected in `localStorage['sdn_node_layout_v1']` on DONE, layout
  identical after a full reload, RESET restores the default
  composition and the tray re-offers removed widgets.
- **Deep link**: `/console/node?route=peers` lands on `/console/peers`
  (History path). This was dead on arrival — ConsoleShell resolves the
  deep link in its onMount, which runs before the parent app's onMount
  had created the router, so the first `navigate` was a no-op.
  SpaceAwareApp now creates the router at component init.
- **Rail**: hover expands (66→218 px, overlays content), pin toggle
  persists via `localStorage['sdn_console_rail_pinned']` ('1'/'0'),
  unpin collapses.
- **QR overlay**: opens from IDENTITY's QR button, backdrop click
  closes.
- Fixed viewport: document does not scroll at 1440×900.

## Audits

- Console: completely clean on a fresh load.
- Network: exactly 4 requests, all same-origin — the document,
  favicon, `/api/v1/data/health` (header health chip) and
  `/api/auth/me` (session chip). Zero external requests.
