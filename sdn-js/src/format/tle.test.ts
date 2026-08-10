import { describe, expect, it } from 'vitest';

import { tleToOMM } from './tle';

// Vallado SGP4 verification satellite 00005 (Vanguard 1), from the canonical
// tcppver/tv.out set. Used here ONLY as well-known fixed-width TLE text — this
// suite asserts PARSING, never propagation.
const VANGUARD_L1 = '1 00005U 58002B   00179.78495062  .00000023  00000-0  28098-4 0  4753';
const VANGUARD_L2 = '2 00005  34.2682 348.7242 1859667 331.7664  19.3264 10.82419157413667';

// ISS TLE (epoch 2008-09-20), same set.
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

  it('accepts the {line1, line2} object form and rejects disagreeing catalog numbers', () => {
    const omm = tleToOMM({ line1: ISS_L1, line2: ISS_L2, name: 'ISS' });
    expect(omm.OBJECT_NAME).toBe('ISS');
    expect(omm.REFERENCE_FRAME).toBe('TEME');

    expect(() => tleToOMM({ line1: VANGUARD_L1, line2: ISS_L2 })).toThrow(/catalog numbers disagree/);
  });

  it('names an unnamed element set from its catalog number', () => {
    const omm = tleToOMM(VANGUARD_L1, VANGUARD_L2);
    expect(omm.OBJECT_NAME).toBe('NORAD 5');
  });

  /**
   * The excision receipt. `sdn-js/astro` published a JS SGP4 propagator; this
   * module is what legitimately survived it, and it survived BECAUSE it only
   * restates text. If someone re-imports a propagator here, this fails.
   */
  it('is format conversion only — it imports no propagator and advances no state', async () => {
    const { readFile } = await import('node:fs/promises');
    const source = await readFile(new URL('./tle.ts', import.meta.url), 'utf8');

    // Zero runtime dependencies: a parser that needs an import is a parser that
    // has started doing something else.
    const imports = source.match(/^\s*import\b.*$/gm) ?? [];
    expect(imports).toEqual([]);
    expect(source).not.toMatch(/\brequire\s*\(/);

    // `SGP4` appears only as the CCSDS MEAN_ELEMENT_THEORY / EPHEMERIS_TYPE
    // field VALUE — a string this record carries, not a thing this file calls.
    const code = source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
    expect(code).not.toMatch(/satrec|gstime|eciToEcf|twoline2satrec|json2satrec/i);
    expect(code).not.toMatch(/\bsgp4\s*\(/i);
    expect(source).toContain('FORMAT CONVERSION ONLY');
  });
});
