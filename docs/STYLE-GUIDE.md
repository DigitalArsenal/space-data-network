# Space Data Network Style Guide

The standard for every Space Data Network surface: the website, documentation,
whitepapers, the dashboard, the desktop app, the project sites (Module SDK,
FlatSQL, FlatBuffers, Space Data Standards), decks and printed material. The
rendered version is `docs/style-guide.html` (spacedatanetwork.org/style-guide.html).

Copyright line: `© Edgesource Corporation`.

## 1. Logo

### The mark

Files: `docs/brand/sdn-mark-dark.svg` (for dark backgrounds) and
`docs/brand/sdn-mark-light.svg` (for light backgrounds).

| Part | Meaning |
| --- | --- |
| Faint ring | The Earth and the network around it |
| Amber arc | An orbit |
| Amber dot at the end of the arc | A satellite, the source of the data |
| Three linked nodes | Peers exchanging digitally signed data |

Rules:

- Use the SVG files. Never redraw, stretch, rotate or re-proportion the mark.
- The arc and its dot are always amber. Everything else is the ink color of the
  background variant.
- Minimum size: 16 px on screen, 6 mm in print.
- Clear space on every side: at least one quarter of the mark's width.
- No gradients, shadows, outlines, glows or badges. No other colors.
- Favicons use the dark variant as an inline SVG data URI.

### The lockup

Files: `docs/brand/sdn-lockup-dark.svg` and `docs/brand/sdn-lockup-light.svg`.

- Mark on the left, "Space Data Network" on the right, vertically centered.
- Wordmark: system sans, weight 600, the ink color. On web pages set the
  wordmark as live text beside the SVG mark, not as an image.
- Gap between mark and wordmark: 10 px at a 22 px mark (about half the mark's
  width).
- Write "Space Data Network" in full in headings and titles. "SDN" is fine in
  body copy after first use, in the product UI, and in code.

## 2. Color

The brand is dark first: near-black surfaces, off-white text, one amber accent.

| Token | Value | Use |
| --- | --- | --- |
| `--bg` | `#000000` | Page background |
| Diagram surface | `#0b0b0c` | Diagrams, charts, code blocks |
| `--panel-solid` | `#161617` | Opaque panels, menus |
| `--panel` | `rgba(28,28,30,.72)` with 12 px backdrop blur | Cards over imagery |
| `--border` | `rgba(255,255,255,.12)` | Hairlines, card borders |
| `--border-strong` | `rgba(255,255,255,.22)` | Buttons, emphasized borders |
| `--text` | `#f5f5f7` | Headings, primary text |
| `--text-2` | `rgba(245,245,247,.78)` | Body copy, ledes |
| `--muted` | `#8e8e93` | Captions, labels, metadata |
| `--accent` (Amber) | `#f5a524` | Links, eyebrows, highlights, the orbit |
| `--accent-soft` | `rgba(245,165,36,.14)` | Accent fills |

Supporting colors, each for one job only:

| Name | Value | Job |
| --- | --- | --- |
| Signal cyan | `#59d9ff` | A second series or object beside amber (the second orbit) |
| Alert red | `#ff3b30` | Warnings and conjunction alerts only. Never decoration |
| Satellite blue-white | `#b3e0ff` | Satellite dots over the globe |
| Amber on light | `#c77d00` | The accent on white, where `#f5a524` lacks contrast |
| Ink on light | `#1d1d1f` | Text and the mark on white |

Rules:

- One accent. Do not introduce other brand hues.
- Never set amber text on white or light backgrounds; use `#c77d00` or ink.
- Red means danger. Pair it with an icon or a word, never color alone.
- Text on imagery sits on a panel or gets the standard text shadow
  (`0 1px 18px rgba(0,0,0,.85)`). Buttons never get a text shadow.

## 3. Typography

System fonts only. Nothing is loaded from a font service (every SDN surface
loads zero third-party bytes).

- Sans: `-apple-system, BlinkMacSystemFont, 'Segoe UI', 'Helvetica Neue', Arial, sans-serif`
- Mono: `ui-monospace, 'SF Mono', Menlo, Consolas, 'Liberation Mono', monospace`

| Style | Size | Weight | Tracking | Notes |
| --- | --- | --- | --- | --- |
| Display (h1) | `clamp(40px, 7vw, 76px)` | 700 | -0.03em | Line height 1.04 |
| Section (h2) | `clamp(28px, 4vw, 40px)` | 700 | -0.02em | Title Case |
| Card title (h3) | 19 px | 600 | normal | Sentence case |
| Lede | `clamp(17px, 2vw, 21px)` | 400 | normal | `--text-2` |
| Body | 16–17 px | 400 | normal | Line height 1.6 |
| Eyebrow | 12 px | 600 | 0.12em | Uppercase, amber |
| Caption | 13–14 px | 400 | normal | `--muted` |
| Code | 13 px | 400 | normal | Mono |

Headings: section headings in Title Case ("What The Network Does"), card and
chapter titles in sentence case ("Digitally signed at the source").

## 4. Layout and components

- Content width 1120 px; reading columns 720–860 px; 20 px side gutters.
- Sections: 72 px vertical padding. Pages are centered.
- Radii: 20 px for cards, 12 px for small panels, images and tables, 999 px for
  buttons and pills, 18 px for diagram frames.
- Cards: `--panel` fill, `--border` hairline, backdrop blur.
- Buttons are pills. Primary: white fill, black text. Secondary: translucent
  dark fill, white text, `--border-strong` outline. On hover the hovered button
  turns white with black text and the primary gives up its white.
- Eyebrow above every section heading. A lede under it, one or two sentences.
- Icons: 24 px line icons, 1.7 px stroke, amber, rounded joins.
- No page-level horizontal scroll at any width. Test at 390 px.

## 5. Imagery

- The Earth: NASA Blue Marble (day) and Black Marble (night), rendered live by
  `earth.js`. Satellites are small blue-white dots; highlighted orbits are
  amber, a second orbit cyan; alerts are red triangles.
- Diagrams: SVG on `#0b0b0c` with an 18 px radius, system font, white
  hairlines at 12–30% opacity, amber for emphasis. Every diagram has a title and
  description for screen readers.
- Photos: public domain or clearly licensed, credited in the caption
  ("U.S. Space Force photo") or in the page notes.
- Product screenshots: real data from a real node, 1440×900 at 2× density,
  exported to WebP at quality 80, no personal names or network addresses.
- Charts: one axis, thin marks, direct labels, amber for a single series.
- Always include alt text. Honor reduced motion: no animation, static frames.

## 6. Voice

- Get to the point. Open with the fact. No preamble, no hype, no filler.
- Plain words over jargon; define a term the first time it appears.
- "Digitally signed", never just "signed", for records, listings and modules.
- "Catalog", never "catalogue".
- Every number has a source. Say what is measured and what is still open.
- Name organizations to state facts, never to disparage them.
- No development notes, task names or build status in public copy.
- Credit every image and every third-party source.

## 7. Technical rules

- Zero third-party origins: fonts, scripts, images and styles are same-origin.
- Prefer SVG for marks, icons and diagrams; WebP for photos and screenshots.
- Colors come from the tokens in `docs/site.css`; do not hard-code new values.
- Dark mode is the default. A light surface uses the "on light" colors above.
