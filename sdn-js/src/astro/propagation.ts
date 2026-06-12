/**
 * SGP4/SDP4 propagation built on satellite.js (the standard JS port of
 * Vallado's reference implementation, WGS-72 constants).
 */
import {
  json2satrec,
  propagate as satPropagate,
  sgp4 as satSgp4,
  twoline2satrec,
  type SatRec,
} from 'satellite.js';

import { splitTleForPropagation, type TleLines } from './internal';
import type { OmmRecord } from './tle';

export type Vector3 = [number, number, number];

export interface StateVector {
  /** TEME position in kilometers. */
  position: Vector3;
  /** TEME velocity in kilometers/second. */
  velocity: Vector3;
}

export interface EphemerisPoint extends StateVector {
  /** UTC time of this state, ISO 8601. */
  time: string;
}

export interface PropagateOptions {
  /** Window start (Date or ISO string). Defaults to the OMM EPOCH. */
  start?: Date | string;
  /** Window end (Date or ISO string). Defaults to start + 24h. */
  end?: Date | string;
  /** Step in seconds. Defaults to 60. */
  step?: number;
}

function toVector(value: { x: number; y: number; z: number }): Vector3 {
  return [value.x, value.y, value.z];
}

function asDate(value: Date | string, label: string): Date {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) {
    throw new Error(`invalid ${label}: ${String(value)}`);
  }
  return date;
}

function assertNoSatrecError(satrec: SatRec, context: string): void {
  if (satrec.error !== 0) {
    throw new Error(`SGP4 error ${satrec.error} ${context}`);
  }
}

/**
 * Build a satellite.js SatRec from an OMM record (the CCSDS field names used
 * across SDN, as produced by tleToOMM and the OMM FlatBuffer decoder).
 */
export function ommToSatrec(omm: Partial<OmmRecord>): SatRec {
  for (const field of ['EPOCH', 'MEAN_MOTION', 'ECCENTRICITY', 'INCLINATION'] as const) {
    if (omm[field] === undefined || omm[field] === null) {
      throw new Error(`OMM record is missing ${field}`);
    }
  }
  const satrec = json2satrec({
    OBJECT_NAME: omm.OBJECT_NAME ?? 'UNKNOWN',
    OBJECT_ID: omm.OBJECT_ID ?? '',
    EPOCH: omm.EPOCH!,
    MEAN_MOTION: omm.MEAN_MOTION!,
    ECCENTRICITY: omm.ECCENTRICITY!,
    INCLINATION: omm.INCLINATION!,
    RA_OF_ASC_NODE: omm.RA_OF_ASC_NODE ?? 0,
    ARG_OF_PERICENTER: omm.ARG_OF_PERICENTER ?? 0,
    MEAN_ANOMALY: omm.MEAN_ANOMALY ?? 0,
    NORAD_CAT_ID: omm.NORAD_CAT_ID ?? 0,
    BSTAR: omm.BSTAR ?? 0,
    // CCSDS stores rev/day^2 and rev/day^3; satellite.js expects the raw
    // TLE conventions (n-dot/2, n-ddot/6).
    MEAN_MOTION_DOT: (omm.MEAN_MOTION_DOT ?? 0) / 2,
    MEAN_MOTION_DDOT: (omm.MEAN_MOTION_DDOT ?? 0) / 6,
    ELEMENT_SET_NO: omm.ELEMENT_SET_NO ?? 999,
    REV_AT_EPOCH: omm.REV_AT_EPOCH ?? 0,
    CLASSIFICATION_TYPE: omm.CLASSIFICATION_TYPE === 'C' ? 'C' : 'U',
    EPHEMERIS_TYPE: 0,
  });
  assertNoSatrecError(satrec, 'initializing from OMM');
  return satrec;
}

/**
 * Propagate a TLE with SGP4/SDP4.
 *
 * @param tle Two-line element set: a 2/3-line string or {line1, line2}.
 * @param epochMinutes Minutes since the TLE epoch.
 * @returns TEME position (km) and velocity (km/s).
 */
export function sgp4(tle: string | TleLines, epochMinutes: number): StateVector {
  const lines = splitTleForPropagation(tle);
  const satrec = twoline2satrec(lines.line1, lines.line2);
  assertNoSatrecError(satrec, 'initializing from TLE');
  const result = satSgp4(satrec, epochMinutes);
  assertNoSatrecError(satrec, `at ${epochMinutes} minutes from epoch`);
  if (!result || typeof result.position === 'boolean' || typeof result.velocity === 'boolean') {
    throw new Error(`SGP4 propagation failed at ${epochMinutes} minutes from epoch`);
  }
  return { position: toVector(result.position), velocity: toVector(result.velocity) };
}

/**
 * Generate an ephemeris for an OMM record over a time window.
 */
export function propagate(omm: Partial<OmmRecord>, options: PropagateOptions = {}): EphemerisPoint[] {
  const satrec = ommToSatrec(omm);
  const start = asDate(options.start ?? omm.EPOCH!, 'start');
  const end = asDate(options.end ?? new Date(start.getTime() + 24 * 3600 * 1000), 'end');
  const stepSeconds = options.step ?? 60;
  if (!(stepSeconds > 0)) {
    throw new Error(`invalid step: ${String(options.step)}`);
  }
  if (end.getTime() < start.getTime()) {
    throw new Error('propagation window end precedes start');
  }

  const points: EphemerisPoint[] = [];
  for (let t = start.getTime(); t <= end.getTime(); t += stepSeconds * 1000) {
    const when = new Date(t);
    const result = satPropagate(satrec, when);
    assertNoSatrecError(satrec, `at ${when.toISOString()}`);
    if (!result || typeof result.position === 'boolean' || typeof result.velocity === 'boolean') {
      throw new Error(`SGP4 propagation failed at ${when.toISOString()}`);
    }
    points.push({
      time: when.toISOString(),
      position: toVector(result.position),
      velocity: toVector(result.velocity),
    });
  }
  return points;
}
