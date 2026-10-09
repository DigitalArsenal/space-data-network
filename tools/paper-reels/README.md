# Whitepaper intro reels

One motion-graphics intro per whitepaper, built on the showreel kit (`../showreel/kit.js`) and rendered by its renderer (`../showreel/render.mjs`).

| Paper | Reel | Output |
| --- | --- | --- |
| `whitepapers/evidence-supported-aso-catalog.md` | `evidence-supported-aso-catalog.js` (56 s) | `docs/media/papers/evidence-supported-aso-catalog/` |
| `whitepapers/fast-conjunction-assessment.md` | `fast-conjunction-assessment.js` (56 s) | `docs/media/papers/fast-conjunction-assessment/` |
| `whitepapers/adversarial-security.md` | `adversarial-security.js` (47 s) | `docs/media/papers/adversarial-security/` |

Each output folder holds `reel.webm` (AV1), `reel.mp4` (H.264) and `poster.webp`.

## Render

```sh
PLAYWRIGHT_MODULE=<dir>/node_modules/playwright/index.mjs \
  node tools/paper-reels/render.mjs [paper ...] [--out <frames dir>]
```

Papers default to all three. `--stills 3.5,20` writes PNG stills to `<frames dir>/<paper>/` instead of encoding. Needs ffmpeg with libx264 and libsvtav1, and ImageMagick for the poster.

## Math

Equations are TeX in `math.mjs`, typeset to SVG paths in `math.gen.js`. After editing `math.mjs`:

```sh
MATHJAX_DIR=<dir with node_modules/mathjax-full@3.2.2> node tools/paper-reels/typeset.mjs
```

Every number and equation on screen is taken from its paper.
