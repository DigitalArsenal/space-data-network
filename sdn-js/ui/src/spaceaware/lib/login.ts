/**
 * Pure logic extracted from `login/Login.dc.html` (design ground truth, loop
 * task U1.1) so it is unit-testable independent of the Svelte component and
 * the canvas it draws into:
 *
 *  - the deterministic seed-42 Park-Miller ("minimal standard" Lehmer) PRNG
 *    that drives the starfield, plus the starfield/orbit-arc generators
 *  - operator-form / node-key form validation used by the mock auth gate
 *  - the staged mock-auth timing constants + per-step view-model builder
 *
 * `screens/LoginScreen.svelte` should only glue these to the DOM (canvas
 * drawing, `$state`, timers) — it must not re-implement the algorithms here.
 */

// ---------------------------------------------------------------------------
// Starfield PRNG
// ---------------------------------------------------------------------------

/** Star count drawn per `Login.dc.html`'s `drawStars()`. */
export const STAR_COUNT = 170;

/** Seed used by `Login.dc.html` so the field is identical across redraws. */
export const STAR_SEED = 42;

const LEHMER_MULTIPLIER = 16807;
const LEHMER_MODULUS = 2147483647; // 2^31 - 1

/**
 * Deterministic Park-Miller "minimal standard" LCG, exactly as implemented
 * inline in `Login.dc.html`:
 *
 * ```js
 * let seed = 42;
 * const rnd = () => { seed = (seed * 16807) % 2147483647; return seed / 2147483647; };
 * ```
 */
export function createSeededRandom(seed: number = STAR_SEED): () => number {
  let s = seed;
  return () => {
    s = (s * LEHMER_MULTIPLIER) % LEHMER_MODULUS;
    return s / LEHMER_MODULUS;
  };
}

export interface Star {
  x: number;
  y: number;
  r: number;
  /** `"r,g,b"` fragment, ready to interpolate into an `rgba(...)` string. */
  color: string;
  /** Already rounded to 2 decimals, matching the source's `.toFixed(2)`. */
  alpha: number;
}

/**
 * Regenerate the 170-star field for a `w`x`h` canvas. Deterministic for a
 * given `(w, h, seed)` — same inputs always produce the same stars, which is
 * what lets `Login.dc.html` redraw on resize without the field "twinkling".
 *
 * Per-star draw order matches the source exactly: x, y, r, color-threshold,
 * alpha (5 `rnd()` calls/star) — color/alpha distribution is 80%
 * `207,227,236` / 15% `127,215,226` / 5% `255,217,160`, r in [0.4, 1.3),
 * alpha in [0.12, 0.72).
 */
export function generateStarfield(w: number, h: number, seed: number = STAR_SEED): Star[] {
  const rnd = createSeededRandom(seed);
  const stars: Star[] = [];
  for (let i = 0; i < STAR_COUNT; i++) {
    const x = rnd() * w;
    const y = rnd() * h;
    const r = 0.4 + rnd() * 0.9;
    const t = rnd();
    const color = t < 0.8 ? '207,227,236' : t < 0.95 ? '127,215,226' : '255,217,160';
    const alpha = Number((0.12 + rnd() * 0.6).toFixed(2));
    stars.push({ x, y, r, color, alpha });
  }
  return stars;
}

export interface OrbitArc {
  cx: number;
  cy: number;
  rx: number;
  ry: number;
  startAngle: number;
  endAngle: number;
  strokeStyle: string;
  lineWidth: number;
  dash: number[];
}

/**
 * The 3 faint elliptical orbit arcs swept from below the viewport. No PRNG
 * involved — purely a function of canvas size, per `Login.dc.html`.
 */
export function generateOrbitArcs(w: number, h: number): OrbitArc[] {
  const cx = w * 0.5;
  const cy = h * 1.85;
  const radii = [h * 1.32, h * 1.5, h * 1.68];
  return radii.map((r, i) => ({
    cx,
    cy,
    rx: r * 1.25,
    ry: r,
    startAngle: Math.PI * 1.05,
    endAngle: Math.PI * 1.95,
    strokeStyle: i === 1 ? 'rgba(53,201,216,0.09)' : 'rgba(90,150,180,0.06)',
    lineWidth: 1,
    dash: i === 2 ? [2, 7] : [],
  }));
}

// ---------------------------------------------------------------------------
// Form validation
// ---------------------------------------------------------------------------

export const OPERATOR_REQUIRED_ERROR = 'OPERATOR ID AND PASSPHRASE REQUIRED';
export const NODE_KEY_REQUIRED_ERROR = 'PEER ID OR MULTIADDR REQUIRED';
export const NODE_KEY_FORMAT_ERROR = 'UNRECOGNIZED PEER KEY FORMAT';

/** Recognized node peer-key / multiaddr prefixes, per `Login.dc.html`. */
export const NODE_KEY_PREFIXES = ['16Uiu', '12D3Koo', '/ip4', '/dns'] as const;

/** Returns an error message, or `null` when the operator form is valid. */
export function validateOperatorForm(opId: string, pass: string): string | null {
  if (!opId.trim() || !pass) return OPERATOR_REQUIRED_ERROR;
  return null;
}

export function isRecognizedNodeKey(key: string): boolean {
  return NODE_KEY_PREFIXES.some((prefix) => key.startsWith(prefix));
}

/** Returns an error message, or `null` when the node-key form is valid. */
export function validateNodeKeyForm(nodeKey: string): string | null {
  const k = nodeKey.trim();
  if (!k) return NODE_KEY_REQUIRED_ERROR;
  if (!isRecognizedNodeKey(k)) return NODE_KEY_FORMAT_ERROR;
  return null;
}

// ---------------------------------------------------------------------------
// Mock staged-auth sequence
// ---------------------------------------------------------------------------

/** Timings (ms, relative to auth start) from `Login.dc.html`'s `startAuth()`. */
export const AUTH_STEP_TIMINGS_MS = {
  step1: 700,
  step2: 1450,
  complete: 2150,
  redirect: 2900,
} as const;

export type AuthPhase = 'idle' | 'auth' | 'ok';

export function operatorStepLabels(): readonly [string, string, string] {
  return ['CREDENTIAL CHECK', 'P2P HANDSHAKE', 'SESSION SEALED'];
}

export function nodeStepLabels(): readonly [string, string, string] {
  return ['PEER KEY VERIFY', 'P2P HANDSHAKE', 'SESSION SEALED'];
}

export interface AuthStepView {
  label: string;
  glyph: string;
  anim: string;
  color: string;
  labelColor: string;
  status: string;
}

/**
 * Build the 3 step-row view models for the current `step` index, matching
 * `Login.dc.html`'s `renderVals().authSteps` exactly: pending `·` (queued,
 * `#44586a`) / active `◌` (running, spinning, `#35c9d8`) / done `✓` (ok,
 * `#5ad6a0`).
 */
export function buildAuthSteps(labels: readonly string[], step: number): AuthStepView[] {
  return labels.map((label, i) => {
    const done = step > i;
    const active = step === i;
    return {
      label,
      glyph: done ? '✓' : active ? '◌' : '·',
      anim: active ? 'sa-spin 0.9s linear infinite' : 'none',
      color: done ? '#5ad6a0' : active ? '#35c9d8' : '#44586a',
      labelColor: done || active ? '#cfe3ec' : '#5a7a8a',
      status: done ? 'OK' : active ? 'RUNNING' : 'QUEUED',
    };
  });
}
