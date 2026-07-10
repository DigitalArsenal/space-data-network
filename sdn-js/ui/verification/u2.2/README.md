# U2.2 verification evidence — BMC2 F4–F6 static ports

Captured 2026-07-10 by the coordinator against a locally built daemon
serving the embedded artifact at `http://127.0.0.1:15080/bmc2/{f4,f5,f6}`,
side-by-side with the `design_handoff/bmc2/*.dc.html` references
(`file://`, same 1440×900 viewport, fresh loads).

## Files

`bmc2-{f4,f5,f6}-port-1440x900.png` vs `bmc2-{f4,f5,f6}-reference-1440x900.png`.

## Pixel results

All three boards **pixel-identical** to their references; the only diff on
each is the intended DEMO tag (D3) in the shared top bar:
- **F4 Conjunction** (red): converging orbits, `oc-blink` conjunction ring
  + blinking top screening-row dot, TCA label, RIC relative-motion panel,
  3-event screening list, threat-actions grid, `3 WARNINGS` status.
- **F5 Maneuver** (amber): ghost dashed target orbit, burn point + thrust
  vector, ΔV budget bars, PLAN ✓ → REVIEW → AUTHORIZE → EXECUTE status
  strip (non-interactive spans — arrows kept verbatim per the U2.1
  descriptive-text precedent), 3 COA cards, maneuver-actions grid,
  `PLAN PENDING` status.
- **F6 Comms** (green): dashed ground track, downlink beam to the active
  station, per-state station markers, link-budget bars, contact-schedule
  timeline (`CONTENTION ×1`), pass list with row-driven dim states (TROLL),
  2D ground-track inset, comms-actions grid with red ABORT DL.

CSS-only globes throughout — no Cesium/WebGL. `oc-blink` added to the
global keyframes with the ground-truth definition; used only by F4.

## Audits

- Console: completely clean on the fresh F6 load (and F4/F5 loads during
  the pass raised nothing).
- Network posture unchanged from U2.1 (same artifact serving path: one
  same-origin document request; all assets inlined `data:` URIs).
- Top-bar F1–F6 nav round-trips exercised across the pass.
