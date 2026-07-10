# U2.1 verification evidence — BMC2 index + F1–F3 static ports

Captured 2026-07-10 by the coordinator against a locally built daemon
serving the embedded artifact (isolated scratch config) at
`http://127.0.0.1:15080/bmc2{,/f1,/f2,/f3}`, side-by-side with the
`design_handoff/bmc2/*.dc.html` references (`file://`, same viewport).

## Files

Port at 1440×900: `bmc2-{index,f1,f2,f3}-port-1440x900.png`;
index also at 1920×1080 (`bmc2-index-port-1920x1080.png`).
References at 1440×900: `bmc2-{index,f1,f2,f3}-reference-1440x900.png`.

## Pixel results

- **Index**: pixel-identical; the only diff is the intended `OPEN →` →
  `OPEN` strip on the LIVE CONSOLE card (no-arrow-glyph hard rule).
- **F1 Surveillance / F2 Track / F3 Sensors**: pixel-identical to their
  references — CSS-only globes (radial-gradient spheres, bordered-ellipse
  orbits, affiliation dots), marquee box (`oc-marq`), layers panel,
  selection portrait/bars, filter strip + catalog table, object-frame
  attitude diagram, elements grid, command card, sensor cone + FOV
  footprint, access timeline/table, action grids. The intended addition on
  each board is the DEMO tag next to the kicker (D3). One pixel fix found
  during this pass and applied: the DEBRIS layer marker is a SQUARE outline
  in the ground truth (span with border, no radius) — the port's shared
  circular dot class was overriding it; now `border-radius: 0`.
- No Cesium/WebGL anywhere in the four components (README rule).

## Interactions

- Index SURVEILLANCE card → `/bmc2/f1` (client-side); F1 top-bar TRACK tab
  → `/bmc2/f2`; direct `/bmc2/f3` load; LIVE CONSOLE card → `/orbital`.
  F4–F6 tabs land on their U2.2 scaffolds.
- Fixed viewport: document does not scroll at 1440×900.

## Audits

- Console: completely clean on a fresh `/bmc2` load.
- Network: exactly ONE http request (the page itself, same-origin); all
  fonts/wasm are inlined `data:` URIs. Zero external requests.

## Recorded (pre-existing, NOT from this task)

The embedded artifact is ~10.4 MB: the hd-wallet wasm is inlined TWICE
(~5 MB as `application/wasm` + ~5 MB as `application/octet-stream`) since
U1.2's real-auth bundling (commit a088bfd7). Inlining wasm is BY DESIGN
(packaging hard rule); the duplication is a bundle-size defect tracked as
a U1.2 residual — dedupe should roughly halve the artifact.
