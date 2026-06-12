/**
 * Conjunction screening over OMM ephemerides and 2-D probability of
 * collision (Foster method) from CDM covariance data.
 */
import { ommToSatrec, type Vector3 } from './propagation';
import { propagate as satPropagate } from 'satellite.js';
import type { OmmRecord } from './tle';

export interface ConjunctionWindow {
  start: Date | string;
  end: Date | string;
}

export interface ScreenOptions {
  /** Report approaches closer than this many kilometers. Default 5. */
  threshold?: number;
  /** Screening window. Defaults to 24h from the primary's EPOCH. */
  timeWindow?: ConjunctionWindow;
  /** Coarse sampling step in seconds. Default 60. */
  step?: number;
}

export interface ConjunctionEvent {
  /** The secondary OMM record that produced this event. */
  secondary: Partial<OmmRecord>;
  /** Time of closest approach, ISO 8601 UTC. */
  tca: string;
  /** Miss distance at TCA in kilometers. */
  missDistance: number;
  /** Relative speed at TCA in kilometers/second. */
  relativeVelocity: number;
}

interface SampledState {
  position: Vector3;
  velocity: Vector3;
}

function sub(a: Vector3, b: Vector3): Vector3 {
  return [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
}

function norm(a: Vector3): number {
  return Math.hypot(a[0], a[1], a[2]);
}

function stateAt(satrec: ReturnType<typeof ommToSatrec>, when: Date): SampledState | null {
  const result = satPropagate(satrec, when);
  if (!result || typeof result.position === 'boolean' || typeof result.velocity === 'boolean') {
    return null;
  }
  return {
    position: [result.position.x, result.position.y, result.position.z],
    velocity: [result.velocity.x, result.velocity.y, result.velocity.z],
  };
}

function separationAt(
  primary: ReturnType<typeof ommToSatrec>,
  secondary: ReturnType<typeof ommToSatrec>,
  when: Date,
): { distance: number; relativeSpeed: number } | null {
  const a = stateAt(primary, when);
  const b = stateAt(secondary, when);
  if (!a || !b) return null;
  return {
    distance: norm(sub(a.position, b.position)),
    relativeSpeed: norm(sub(a.velocity, b.velocity)),
  };
}

/** Golden-section minimization of the separation distance on [lo, hi]. */
function refineTca(
  primary: ReturnType<typeof ommToSatrec>,
  secondary: ReturnType<typeof ommToSatrec>,
  lo: number,
  hi: number,
): { tca: Date; distance: number; relativeSpeed: number } | null {
  const phi = (Math.sqrt(5) - 1) / 2;
  let a = lo;
  let b = hi;
  let c = b - phi * (b - a);
  let d = a + phi * (b - a);
  let fc = separationAt(primary, secondary, new Date(c))?.distance;
  let fd = separationAt(primary, secondary, new Date(d))?.distance;
  if (fc === undefined || fd === undefined) return null;
  for (let i = 0; i < 60 && b - a > 1; i += 1) {
    if (fc < fd) {
      b = d;
      d = c;
      fd = fc;
      c = b - phi * (b - a);
      fc = separationAt(primary, secondary, new Date(c))?.distance;
      if (fc === undefined) return null;
    } else {
      a = c;
      c = d;
      fc = fd;
      d = a + phi * (b - a);
      fd = separationAt(primary, secondary, new Date(d))?.distance;
      if (fd === undefined) return null;
    }
  }
  const tca = new Date((a + b) / 2);
  const final = separationAt(primary, secondary, tca);
  if (!final) return null;
  return { tca, distance: final.distance, relativeSpeed: final.relativeSpeed };
}

/**
 * Screen a primary object against secondaries for close approaches.
 *
 * Coarse-samples the separation distance over the window, then refines each
 * local minimum below threshold with golden-section search (~millisecond TCA
 * resolution).
 */
export function screenConjunctions(
  primary: Partial<OmmRecord>,
  secondaries: Array<Partial<OmmRecord>>,
  options: ScreenOptions = {},
): ConjunctionEvent[] {
  const threshold = options.threshold ?? 5;
  const stepSeconds = options.step ?? 60;
  if (!(threshold > 0)) throw new Error(`invalid threshold: ${String(options.threshold)}`);
  if (!(stepSeconds > 0)) throw new Error(`invalid step: ${String(options.step)}`);
  if (!primary.EPOCH && !options.timeWindow) {
    throw new Error('screenConjunctions needs options.timeWindow or a primary EPOCH');
  }

  const startMs = new Date(options.timeWindow?.start ?? primary.EPOCH!).getTime();
  const endMs = options.timeWindow?.end
    ? new Date(options.timeWindow.end).getTime()
    : startMs + 24 * 3600 * 1000;
  if (Number.isNaN(startMs) || Number.isNaN(endMs) || endMs <= startMs) {
    throw new Error('invalid screening window');
  }

  const primaryRec = ommToSatrec(primary);
  const events: ConjunctionEvent[] = [];

  for (const secondary of secondaries) {
    const secondaryRec = ommToSatrec(secondary);

    // Coarse pass: track local minima of the sampled separation.
    let previous: number | undefined;
    let beforePrevious: number | undefined;
    let best: { tca: Date; distance: number; relativeSpeed: number } | null = null;
    for (let t = startMs; t <= endMs; t += stepSeconds * 1000) {
      const sample = separationAt(primaryRec, secondaryRec, new Date(t));
      if (!sample) {
        previous = beforePrevious = undefined;
        continue;
      }
      const isInteriorMinimum =
        previous !== undefined && beforePrevious !== undefined && previous <= beforePrevious && previous <= sample.distance;
      if (isInteriorMinimum && previous! <= threshold * 4) {
        const refined = refineTca(primaryRec, secondaryRec, t - 2 * stepSeconds * 1000, t);
        if (refined && refined.distance <= threshold && (!best || refined.distance < best.distance)) {
          best = refined;
        }
      }
      beforePrevious = previous;
      previous = sample.distance;
    }
    // Window edges can hide a minimum; check both ends directly.
    for (const edge of [startMs, endMs]) {
      const sample = separationAt(primaryRec, secondaryRec, new Date(edge));
      if (sample && sample.distance <= threshold && (!best || sample.distance < best.distance)) {
        best = { tca: new Date(edge), distance: sample.distance, relativeSpeed: sample.relativeSpeed };
      }
    }

    if (best) {
      events.push({
        secondary,
        tca: best.tca.toISOString(),
        missDistance: best.distance,
        relativeVelocity: best.relativeSpeed,
      });
    }
  }

  return events.sort((a, b) => a.missDistance - b.missDistance);
}

/** RTN 3x3 covariance lower-triangle fields, in m^2, as carried by CDM objects. */
export interface RtnCovariance {
  CR_R: number;
  CT_R: number;
  CT_T: number;
  CN_R: number;
  CN_T: number;
  CN_N: number;
}

export interface PcInput {
  /** Total miss distance in meters (used when relative position is absent). */
  MISS_DISTANCE?: number;
  RELATIVE_POSITION_R?: number;
  RELATIVE_POSITION_T?: number;
  RELATIVE_POSITION_N?: number;
  RELATIVE_VELOCITY_R?: number;
  RELATIVE_VELOCITY_T?: number;
  RELATIVE_VELOCITY_N?: number;
  OBJECT1?: Partial<RtnCovariance>;
  OBJECT2?: Partial<RtnCovariance>;
  /** Combined hard-body radius in meters. */
  HARD_BODY_RADIUS?: number;
}

export interface PcOptions {
  /** Combined hard-body radius in meters; overrides the CDM field. Default 20. */
  hardBodyRadius?: number;
  /** Integration grid resolution. Default 120. */
  gridSize?: number;
}

type Mat2 = [[number, number], [number, number]];

function covarianceOf(object: Partial<RtnCovariance> | undefined): number[][] {
  const c = object ?? {};
  return [
    [c.CR_R ?? 0, c.CT_R ?? 0, c.CN_R ?? 0],
    [c.CT_R ?? 0, c.CT_T ?? 0, c.CN_T ?? 0],
    [c.CN_R ?? 0, c.CN_T ?? 0, c.CN_N ?? 0],
  ];
}

function dot3(a: number[], b: number[]): number {
  return a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
}

function scale3(a: number[], s: number): number[] {
  return [a[0] * s, a[1] * s, a[2] * s];
}

function sub3(a: number[], b: number[]): number[] {
  return [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
}

function cross3(a: number[], b: number[]): number[] {
  return [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
}

function normalize3(a: number[]): number[] {
  const n = Math.hypot(a[0], a[1], a[2]);
  if (n === 0) throw new Error('cannot normalize zero vector');
  return scale3(a, 1 / n);
}

function project(covariance: number[][], e1: number[], e2: number[]): Mat2 {
  const apply = (v: number[]) => [dot3(covariance[0], v), dot3(covariance[1], v), dot3(covariance[2], v)];
  const c1 = apply(e1);
  const c2 = apply(e2);
  return [
    [dot3(e1, c1), dot3(e1, c2)],
    [dot3(e2, c1), dot3(e2, c2)],
  ];
}

/**
 * 2-D probability of collision (Foster method).
 *
 * Combines both objects' RTN covariances, projects the relative state onto
 * the conjunction plane (perpendicular to the relative velocity), and
 * numerically integrates the bivariate normal density over the combined
 * hard-body circle. Assumes linear relative motion and uncorrelated object
 * covariances, per the standard short-encounter model. All CDM inputs are in
 * meters / m^2 (CCSDS 508.0).
 */
export function computePc(cdm: PcInput, options: PcOptions = {}): number {
  const hardBodyRadius = options.hardBodyRadius ?? cdm.HARD_BODY_RADIUS ?? 20;
  if (!(hardBodyRadius > 0)) throw new Error('hard-body radius must be positive');

  const hasRelativePosition =
    cdm.RELATIVE_POSITION_R !== undefined ||
    cdm.RELATIVE_POSITION_T !== undefined ||
    cdm.RELATIVE_POSITION_N !== undefined;
  if (!hasRelativePosition && cdm.MISS_DISTANCE === undefined) {
    throw new Error('computePc needs RELATIVE_POSITION_R/T/N or MISS_DISTANCE');
  }
  const relativePosition = hasRelativePosition
    ? [cdm.RELATIVE_POSITION_R ?? 0, cdm.RELATIVE_POSITION_T ?? 0, cdm.RELATIVE_POSITION_N ?? 0]
    : [cdm.MISS_DISTANCE ?? 0, 0, 0];
  const relativeVelocity = [
    cdm.RELATIVE_VELOCITY_R ?? 0,
    cdm.RELATIVE_VELOCITY_T ?? 0,
    cdm.RELATIVE_VELOCITY_N ?? 1,
  ];

  // Conjunction-plane basis: e1 along the in-plane component of the relative
  // position, e2 completing the plane perpendicular to relative velocity.
  const vHat = normalize3(relativeVelocity);
  const inPlane = sub3(relativePosition, scale3(vHat, dot3(relativePosition, vHat)));
  const e1 = dot3(inPlane, inPlane) > 0 ? normalize3(inPlane) : normalize3(cross3(vHat, [1, 0, 0]));
  const e2 = normalize3(cross3(vHat, e1));

  const combined = (() => {
    const c1 = covarianceOf(cdm.OBJECT1);
    const c2 = covarianceOf(cdm.OBJECT2);
    return c1.map((row, i) => row.map((value, j) => value + c2[i][j]));
  })();
  const c2d = project(combined, e1, e2);
  const mean: [number, number] = [dot3(relativePosition, e1), dot3(relativePosition, e2)];

  const det = c2d[0][0] * c2d[1][1] - c2d[0][1] * c2d[1][0];
  if (!(det > 0) || !(c2d[0][0] > 0) || !(c2d[1][1] > 0)) {
    throw new Error('combined covariance is not positive definite in the conjunction plane');
  }
  const inv: Mat2 = [
    [c2d[1][1] / det, -c2d[0][1] / det],
    [-c2d[1][0] / det, c2d[0][0] / det],
  ];

  // Polar-grid integration of the bivariate normal over the hard-body disc.
  const gridSize = options.gridSize ?? 120;
  const dr = hardBodyRadius / gridSize;
  const dTheta = (2 * Math.PI) / gridSize;
  const normalization = 1 / (2 * Math.PI * Math.sqrt(det));
  let pc = 0;
  for (let i = 0; i < gridSize; i += 1) {
    const r = (i + 0.5) * dr;
    for (let j = 0; j < gridSize; j += 1) {
      const theta = (j + 0.5) * dTheta;
      const dx = r * Math.cos(theta) - mean[0];
      const dy = r * Math.sin(theta) - mean[1];
      const quadratic = dx * (inv[0][0] * dx + inv[0][1] * dy) + dy * (inv[1][0] * dx + inv[1][1] * dy);
      pc += normalization * Math.exp(-0.5 * quadratic) * r * dr * dTheta;
    }
  }
  return Math.min(1, pc);
}
