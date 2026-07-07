/**
 * SpaceAware.io design tokens — single tokens module for the SpaceAware UI port.
 *
 * Source of truth: design_handoff/DESIGN_TOKENS.md (design/SpaceAware.io.zip,
 * sha1 05a065996a5577cc5a7f95afb6ce4915e59adb6b). Pixel ground truth is always
 * the inline styles in the reference `.dc.html` files; these tokens are the
 * shared vocabulary those styles draw from.
 *
 * Conventions carried by these tokens (hard rules):
 * - border-radius 0 everywhere; only status dots / spinners are round
 * - uppercase labels/headings; kickers use wide letter-spacing
 * - no directional arrow glyphs appended to labels
 * - every actionable control carries a `title` tooltip
 */

/** Canvas & surface colors. */
export const surface = {
  appBackground: '#04060a',
  radialPageGlow: 'radial-gradient(circle at 50% -8%, #0a1722, #04060a 55%)',
  /** Panel fill alpha range 0.72–0.94 over rgb(7,12,18). */
  panelFill: 'rgba(7,12,18,0.85)',
  panelFillLight: 'rgba(7,12,18,0.72)',
  panelFillDense: 'rgba(7,12,18,0.94)',
  panelBackdropBlur: 'blur(7px)',
  panelGradientRaised: 'linear-gradient(178deg,#16252f,#0a141b)',
  panelGradientWell: 'linear-gradient(178deg,#0b151c,#060d12)',
  menuPopover: 'rgba(6,11,17,0.94)',
  inputWell: 'rgba(4,8,12,0.65)',
} as const;

/** Borders & hairlines. */
export const border = {
  hairline: '1px solid rgba(110,170,190,0.22)',
  hairlineFaint: '1px solid rgba(110,170,190,0.16)',
  hairlineStrong: '1px solid rgba(110,170,190,0.28)',
  raisedPanel: '1px solid rgba(90,150,180,0.24)',
  divider: 'rgba(110,170,190,0.13)',
} as const;

/** Text colors, brightest to faintest. */
export const text = {
  bright: '#eaf6f8',
  body: '#c7d6dd',
  bodyAlt: '#cfe3ec',
  secondary: '#9fb3bc',
  secondaryDim: '#7d929b',
  kicker: '#5a7a8a',
  kickerAlt: '#5d7681',
  faint: '#44586a',
  faintAlt: '#3d515c',
  faintCool: '#3f5660',
} as const;

/** Semantic accent colors. */
export const accent = {
  cyan: '#35c9d8',
  cyanLight: '#9fe9f2',
  cyanLightAlt: '#7fe3ec',
  ice: '#9fd4f5',
  iceMid: '#7fb4d6',
  iceDeep: '#4aa6e0',
  green: '#5ad6a0',
  greenLight: '#cdeede',
  amber: '#ffb24d',
  amberLight: '#ffd089',
  amberPale: '#ffe6bf',
  amberDim: '#c9a07a',
  amberFaint: '#a08a5f',
  red: '#ff5b5b',
  redAlt: '#ff6b6b',
  redLight: '#ff8d8d',
  redLightAlt: '#ff9b9b',
  redPale: '#ffd3d3',
  redDim: '#c98a8a',
  purple: '#c77dff',
  orange: '#ff9e64',
  gold: '#f0b54a',
} as const;

/** Orbit-regime palette. */
export const regime = {
  LEO: '#35c9d8',
  MEO: '#5ad6a0',
  GEO: '#f0b54a',
  HEO: '#c77dff',
} as const;
export type OrbitRegime = keyof typeof regime;

/** Status dot colors — dots are the ONLY rounded elements in the system. */
export const status = {
  nominal: '#5ad6a0',
  degraded: '#ffb24d',
  alert: '#ff5b5b',
} as const;
export type StatusLevel = keyof typeof status;

/** Status dot geometry: 6–10px circle + matching glow. */
export function statusDotGlow(color: string, radiusPx = 7): string {
  return `0 0 ${radiusPx}px ${color}`;
}

/** Typography stacks. */
export const font = {
  /** Display, titles, buttons, tab labels — usually uppercase + letterspaced. */
  display: "'Chakra Petch', 'IBM Plex Mono', ui-monospace, monospace",
  /** Body, data values, inputs — the default stack. */
  mono: "'IBM Plex Mono', ui-monospace, monospace",
  /** Dense numeric readouts (Orbital Console). */
  numeric: "'JetBrains Mono', 'IBM Plex Mono', ui-monospace, monospace",
} as const;

/** Type scale in px, as used by the design (smallest to largest). */
export const typeScale = [
  8, 8.5, 9, 9.5, 10, 10.5, 11, 11.5, 12, 12.5, 13, 14, 15.5, 17, 18, 19, 25, 26,
] as const;

/** Letter-spacing conventions (em). */
export const tracking = {
  kickerMin: '0.16em',
  kicker: '0.22em',
  kickerMax: '0.28em',
  button: '0.12em',
  buttonTight: '0.08em',
  buttonWide: '0.16em',
  displayTight: '0.04em',
} as const;

/** Kicker label spec: 8.5–9.5px, wide tracking, muted color, uppercase. */
export const kicker = {
  fontSizePx: 9,
  letterSpacing: tracking.kicker,
  color: text.kicker,
  fontFamily: font.display,
  textTransform: 'uppercase',
} as const;

/** Shape & effects. */
export const effects = {
  /** Square, instrument-cluster look. Radius only on status dots/spinners. */
  borderRadius: '0',
  borderRadiusDot: '50%',
  floatingPanelShadow: '0 14px 40px rgba(0,0,0,0.6)',
  fullscreenVignette: 'inset 0 0 220px 55px rgba(0,0,0,0.78)',
  accentGlowAmber: '0 0 8px rgba(255,178,77,0.85)',
  scrollbarSizePx: 8,
  scrollbarThumb: 'rgba(110,170,190,0.25)',
} as const;

/** Keyframe names shared with src/spaceaware/styles/spaceaware.css. */
export const keyframes = {
  /** opacity .35↔1, ~1.5s */
  pulse: 'sa-pulse',
  /** 0.9s linear rotation */
  spin: 'sa-spin',
  /** caret blink, 1.2s step-end */
  blink: 'sa-blink',
  scan: 'sa-scan',
  marq: 'sa-marq',
} as const;

/** Button variants per the interaction conventions. */
export const button = {
  primary: {
    background: 'rgba(53,201,216,0.13)',
    border: '1px solid rgba(53,201,216,0.48)',
    color: accent.cyanLight,
    fontFamily: font.display,
    fontWeight: 600,
    letterSpacing: tracking.button,
  },
  neutral: {
    background: 'rgba(7,12,18,0.78)',
    border: '1px solid rgba(110,170,190,0.22)',
    color: text.secondary,
    fontFamily: font.display,
    fontWeight: 600,
    letterSpacing: tracking.button,
  },
  destructive: {
    background: 'rgba(255,91,91,0.13)',
    border: '1px solid rgba(255,91,91,0.48)',
    color: accent.redLight,
    fontFamily: font.display,
    fontWeight: 600,
    letterSpacing: tracking.button,
  },
} as const;
export type ButtonVariant = keyof typeof button;

/** Hover conventions: raise fill alpha, brighten text. */
export const hover = {
  primaryBackground: 'rgba(53,201,216,0.24)',
  neutralBackground: 'rgba(110,170,190,0.14)',
  destructiveBackground: 'rgba(255,91,91,0.24)',
  textBright: text.bright,
  barBrightness: 'brightness(1.3)',
} as const;

/** Selected tab/segment: accent 2px border-bottom or accent-tinted fill. */
export const tabs = {
  selectedBorderBottom: `2px solid ${accent.cyan}`,
  selectedColor: accent.cyanLight,
  inactiveColor: text.secondary,
} as const;

/**
 * The established glyph set — no other emoji/glyph decoration is permitted,
 * and no directional arrow glyphs on labels.
 */
export const glyphs = ['◈', '◯', '⬚', '⊘', '◎', '⬢', '⬡', '⚠', '✓', '✕', '▍', '☠', '🔒'] as const;

export const tokens = {
  surface,
  border,
  text,
  accent,
  regime,
  status,
  font,
  typeScale,
  tracking,
  kicker,
  effects,
  keyframes,
  button,
  hover,
  tabs,
  glyphs,
} as const;

export default tokens;
