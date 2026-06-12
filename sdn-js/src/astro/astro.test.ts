import { describe, expect, it } from 'vitest';

import {
  computePc,
  ecefToLla,
  eciToEcef,
  gmst,
  propagate,
  screenConjunctions,
  sgp4,
  tleToOMM,
} from './index';

// Vallado SGP4 verification satellite 00005 (Vanguard 1, wgs72), from the
// canonical tcppver/tv.out verification set.
const VANGUARD_L1 = '1 00005U 58002B   00179.78495062  .00000023  00000-0  28098-4 0  4753';
const VANGUARD_L2 = '2 00005  34.2682 348.7242 1859667 331.7664  19.3264 10.82419157413667';

// ISS TLE (epoch 2008-09-20), also from the Vallado verification set.
const ISS_L1 = '1 25544U 98067A   08264.51782528 -.00002182  00000-0 -11606-4 0  2927';
const ISS_L2 = '2 25544  51.6416 247.4627 0006703 130.5360 325.0288 15.72125391563537';

describe('tleToOMM', () => {
  it('converts the Vanguard TLE to a CCSDS OMM record', () => {
    const omm = tleToOMM(VANGUARD_L1, VANGUARD_L2, 'VANGUARD 1');
    expect(omm).toMatchObject({
      OBJECT_NAME: 'VANGUARD 1',
      OBJECT_ID: '1958-002B',
      NORAD_CAT_ID: 5,
      CLASSIFICATION_TYPE: 'U',
      MEAN_ELEMENT_THEORY: 'SGP4',
      CENTER_NAME: 'EARTH',
      TIME_SYSTEM: 'UTC',
      MEAN_MOTION: 10.82419157,
      INCLINATION: 34.2682,
      RA_OF_ASC_NODE: 348.7242,
      ARG_OF_PERICENTER: 331.7664,
      MEAN_ANOMALY: 19.3264,
      ELEMENT_SET_NO: 475,
      REV_AT_EPOCH: 41366,
    });
    expect(omm.ECCENTRICITY).toBeCloseTo(0.1859667, 10);
    expect(omm.BSTAR).toBeCloseTo(0.28098e-4, 12);
    // 2000 day 179.78495062 -> 2000-06-27 ~18:50:19 UTC
    expect(omm.EPOCH.startsWith('2000-06-27T18:50:1')).toBe(true);
  });

  it('accepts a single 3-line string and validates checksums', () => {
    const omm = tleToOMM(`ISS (ZARYA)\n${ISS_L1}\n${ISS_L2}`);
    expect(omm.OBJECT_NAME).toBe('ISS (ZARYA)');
    expect(omm.NORAD_CAT_ID).toBe(25544);
    expect(omm.MEAN_MOTION_DOT).toBeCloseTo(-0.00002182 * 2, 12);

    const corrupted = VANGUARD_L1.slice(0, 68) + '9';
    expect(() => tleToOMM(corrupted, VANGUARD_L2)).toThrow(/checksum/);
  });
});

describe('sgp4', () => {
  it('matches the Vallado verification vector for 00005 at epoch', () => {
    const { position, velocity } = sgp4({ line1: VANGUARD_L1, line2: VANGUARD_L2 }, 0);
    expect(position[0]).toBeCloseTo(7022.46529266, 2);
    expect(position[1]).toBeCloseTo(-1400.08296755, 2);
    expect(position[2]).toBeCloseTo(0.03995155, 2);
    expect(velocity[0]).toBeCloseTo(1.893841015, 4);
    expect(velocity[1]).toBeCloseTo(6.405893759, 4);
    expect(velocity[2]).toBeCloseTo(4.53480725, 4);
  });

  it('matches the Vallado verification vector for 00005 at 360 minutes', () => {
    const { position } = sgp4(`${VANGUARD_L1}\n${VANGUARD_L2}`, 360);
    expect(position[0]).toBeCloseTo(-7154.0312026, 1);
    expect(position[1]).toBeCloseTo(-3783.17682504, 1);
    expect(position[2]).toBeCloseTo(-3536.19412294, 1);
  });

  it('produces a physically sane ISS state', () => {
    const { position, velocity } = sgp4(`${ISS_L1}\n${ISS_L2}`, 0);
    const radius = Math.hypot(...position);
    const speed = Math.hypot(...velocity);
    expect(radius).toBeGreaterThan(6650);
    expect(radius).toBeLessThan(6810);
    expect(speed).toBeGreaterThan(7.5);
    expect(speed).toBeLessThan(7.8);
  });
});

describe('propagate', () => {
  it('agrees with sgp4() when driven from a tleToOMM record', () => {
    const omm = tleToOMM(ISS_L1, ISS_L2, 'ISS');
    const direct = sgp4({ line1: ISS_L1, line2: ISS_L2 }, 0);
    const [first] = propagate(omm, {
      start: omm.EPOCH,
      end: omm.EPOCH,
      step: 60,
    });
    // The OMM epoch string is millisecond-truncated, so allow sub-km slack.
    for (let axis = 0; axis < 3; axis += 1) {
      expect(Math.abs(first.position[axis] - direct.position[axis])).toBeLessThan(0.5);
    }
  });

  it('produces a stepped ephemeris over the window', () => {
    const omm = tleToOMM(ISS_L1, ISS_L2);
    const points = propagate(omm, {
      start: omm.EPOCH,
      end: new Date(new Date(omm.EPOCH).getTime() + 10 * 60 * 1000),
      step: 120,
    });
    expect(points).toHaveLength(6);
    expect(new Date(points[1].time).getTime() - new Date(points[0].time).getTime()).toBe(120000);
  });
});

describe('frames', () => {
  it('eciToEcef at zero GMST is identity, and a quarter turn swaps axes', () => {
    expect(eciToEcef([7000, 0, 0], 0)).toEqual([7000, 0, 0]);
    const rotated = eciToEcef([7000, 0, 0], Math.PI / 2);
    expect(rotated[0]).toBeCloseTo(0, 6);
    expect(rotated[1]).toBeCloseTo(-7000, 6);
    expect(rotated[2]).toBeCloseTo(0, 6);
    const objectShaped = eciToEcef({ x: 7000, y: 0, z: 0 }, Math.PI / 2);
    expect(objectShaped.y).toBeCloseTo(-7000, 6);
  });

  it('ecefToLla recovers known geodetic reference points', () => {
    const equator = ecefToLla([6378.137, 0, 0]);
    expect(equator.latitude).toBeCloseTo(0, 9);
    expect(equator.longitude).toBeCloseTo(0, 9);
    expect(equator.altitude).toBeCloseTo(0, 6);

    // WGS-84 polar radius 6356.752314245 km -> 90N at zero height.
    const pole = ecefToLla([0, 0, 6356.752314245]);
    expect(pole.latitude).toBeCloseTo(90, 6);
    expect(pole.altitude).toBeCloseTo(0, 5);

    const above = ecefToLla([6478.137, 0, 0]);
    expect(above.altitude).toBeCloseTo(100, 6);
  });

  it('gmst is stable for a known date', () => {
    // 2000-01-01 12:00 UT1≈UTC (J2000): GMST ≈ 280.46062° = 4.894961 rad.
    expect(gmst(new Date(Date.UTC(2000, 0, 1, 12)))).toBeCloseTo(4.894961, 3);
  });
});

describe('screenConjunctions', () => {
  it('flags an object against itself and not a far-separated plane', () => {
    const iss = tleToOMM(ISS_L1, ISS_L2, 'ISS');
    const coplanar = { ...iss, OBJECT_NAME: 'SHADOW', MEAN_ANOMALY: (iss.MEAN_ANOMALY + 0.05) % 360 };
    const farPlane = { ...iss, OBJECT_NAME: 'FAR', RA_OF_ASC_NODE: (iss.RA_OF_ASC_NODE + 90) % 360 };

    const events = screenConjunctions(iss, [coplanar, farPlane], {
      threshold: 25,
      timeWindow: {
        start: iss.EPOCH,
        end: new Date(new Date(iss.EPOCH).getTime() + 3 * 3600 * 1000),
      },
      step: 30,
    });

    expect(events.length).toBeGreaterThanOrEqual(1);
    expect(events[0].secondary.OBJECT_NAME).toBe('SHADOW');
    expect(events[0].missDistance).toBeLessThan(25);
    expect(events[0].relativeVelocity).toBeLessThan(1);
    expect(events.some((event) => event.secondary.OBJECT_NAME === 'FAR')).toBe(false);
  });
});

describe('computePc', () => {
  const tightCovariance = { CR_R: 100, CT_T: 100, CN_N: 100, CT_R: 0, CN_R: 0, CN_T: 0 };

  it('approaches 1 for a direct hit with covariance far smaller than the hard body', () => {
    const pc = computePc(
      {
        RELATIVE_POSITION_R: 0,
        RELATIVE_POSITION_T: 0,
        RELATIVE_POSITION_N: 0,
        RELATIVE_VELOCITY_T: 7000,
        OBJECT1: tightCovariance,
        OBJECT2: tightCovariance,
      },
      { hardBodyRadius: 200 },
    );
    expect(pc).toBeGreaterThan(0.99);
  });

  it('is effectively zero for a kilometer-scale miss with meter-scale covariance', () => {
    const pc = computePc({
      MISS_DISTANCE: 25_000,
      RELATIVE_VELOCITY_T: 7000,
      OBJECT1: tightCovariance,
      OBJECT2: tightCovariance,
      HARD_BODY_RADIUS: 20,
    });
    expect(pc).toBeLessThan(1e-9);
  });

  it('decreases monotonically as miss distance grows', () => {
    const wide = { CR_R: 1e6, CT_T: 1e6, CN_N: 1e6, CT_R: 0, CN_R: 0, CN_T: 0 };
    const at = (miss: number) =>
      computePc({
        RELATIVE_POSITION_R: miss,
        RELATIVE_VELOCITY_T: 7000,
        OBJECT1: wide,
        OBJECT2: wide,
        HARD_BODY_RADIUS: 20,
      });
    const near = at(100);
    const mid = at(1000);
    const far = at(5000);
    expect(near).toBeGreaterThan(mid);
    expect(mid).toBeGreaterThan(far);
    expect(near).toBeLessThan(1);
  });

  it('rejects degenerate covariance', () => {
    expect(() =>
      computePc({
        RELATIVE_POSITION_R: 10,
        OBJECT1: { CR_R: 0, CT_T: 0, CN_N: 0 },
        OBJECT2: { CR_R: 0, CT_T: 0, CN_N: 0 },
      }),
    ).toThrow(/positive definite/);
  });
});
