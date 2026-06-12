/**
 * Earth reference frame conversions (TEME/ECI -> ECEF -> geodetic, WGS-84).
 */
import { eciToEcf, gstime } from 'satellite.js';

import type { Vector3 } from './propagation';

const WGS84_A = 6378.137; // km
const WGS84_F = 1 / 298.257223563;
const WGS84_E2 = WGS84_F * (2 - WGS84_F);

export interface GeodeticPoint {
  /** Geodetic latitude in degrees. */
  latitude: number;
  /** Longitude in degrees, [-180, 180]. */
  longitude: number;
  /** Height above the WGS-84 ellipsoid in kilometers. */
  altitude: number;
}

type VectorInput = Vector3 | { x: number; y: number; z: number };

function asXyz(value: VectorInput): { x: number; y: number; z: number } {
  if (Array.isArray(value)) {
    return { x: value[0], y: value[1], z: value[2] };
  }
  return value;
}

/** Greenwich mean sidereal time (radians) for a UTC date — convenience re-export. */
export function gmst(date: Date | string): number {
  const when = date instanceof Date ? date : new Date(date);
  if (Number.isNaN(when.getTime())) {
    throw new Error(`invalid date: ${String(date)}`);
  }
  return gstime(when);
}

/**
 * Rotate an ECI (TEME) vector into ECEF using the given GMST (radians).
 * Returns the same shape it was given (array in, array out).
 */
export function eciToEcef<T extends VectorInput>(eci: T, gmstRadians: number): T {
  const rotated = eciToEcf(asXyz(eci), gmstRadians);
  if (Array.isArray(eci)) {
    return [rotated.x, rotated.y, rotated.z] as T;
  }
  return { x: rotated.x, y: rotated.y, z: rotated.z } as T;
}

/**
 * Convert an ECEF position (km) to WGS-84 geodetic coordinates using
 * Bowring's method with iterative refinement.
 */
export function ecefToLla(ecef: VectorInput): GeodeticPoint {
  const { x, y, z } = asXyz(ecef);
  const longitude = Math.atan2(y, x);
  const p = Math.sqrt(x * x + y * y);

  // Polar singularity: directly above a pole the iteration divides by
  // cos(latitude); answer it in closed form instead.
  if (p < 1e-9) {
    const polarRadius = WGS84_A * (1 - WGS84_F);
    return {
      latitude: z >= 0 ? 90 : -90,
      longitude: 0,
      altitude: Math.abs(z) - polarRadius,
    };
  }

  let latitude = Math.atan2(z, p * (1 - WGS84_E2));
  let altitude = 0;
  for (let i = 0; i < 8; i += 1) {
    const sinLat = Math.sin(latitude);
    const n = WGS84_A / Math.sqrt(1 - WGS84_E2 * sinLat * sinLat);
    altitude = p / Math.cos(latitude) - n;
    const next = Math.atan2(z, p * (1 - (WGS84_E2 * n) / (n + altitude)));
    if (Math.abs(next - latitude) < 1e-12) {
      latitude = next;
      break;
    }
    latitude = next;
  }

  return {
    latitude: (latitude * 180) / Math.PI,
    longitude: (longitude * 180) / Math.PI,
    altitude,
  };
}
