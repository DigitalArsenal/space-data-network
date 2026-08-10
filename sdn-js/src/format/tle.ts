/**
 * Two-line element set TEXT PARSING and conversion to CCSDS OMM records.
 *
 * FORMAT CONVERSION ONLY — no propagation, no frames, no physics of any kind.
 * Elements are parsed out of 69-column fixed-width text and restated under
 * CCSDS field names; they are never ADVANCED. Nothing here integrates, rotates,
 * or evaluates an orbit.
 *
 * That distinction is the whole reason this file survived the excision of
 * `src/astro/` (graph task `sdn-js-astro-ships-a-js-propagator`). OWNER LAW
 * 2026-08-09, verbatim: "There is no JS physics at all. Everything we are doing
 * is through WASM space data module SDK no exceptions." JavaScript in a shipped
 * bundle holds exactly four things — UI state, rendering glue, unit/format
 * conversion, and dispatch. This is the third one, and it must stay the third
 * one: if a future edit here needs to know where an object IS rather than what
 * its element set SAYS, that belongs in `propagator/sgp4` reached through the
 * space-data-module-sdk browser harness, never in this file.
 *
 * Field names follow the OMM FlatBuffer surface used across SDN (see
 * src/ui/runtime/omm-flatbuffer.ts), so the output of tleToOMM() can be fed
 * directly to the OMM builders and to any module that takes an OMM record.
 */

/** The two element lines of a TLE, plus the optional title line. */
export interface TleLines {
  line1: string;
  line2: string;
  name?: string;
}

export interface OmmRecord {
  CCSDS_OMM_VERS: number;
  CREATION_DATE: string;
  OBJECT_NAME: string;
  OBJECT_ID: string;
  CENTER_NAME: string;
  REFERENCE_FRAME: string;
  TIME_SYSTEM: string;
  MEAN_ELEMENT_THEORY: string;
  EPOCH: string;
  MEAN_MOTION: number;
  ECCENTRICITY: number;
  INCLINATION: number;
  RA_OF_ASC_NODE: number;
  ARG_OF_PERICENTER: number;
  MEAN_ANOMALY: number;
  EPHEMERIS_TYPE: string;
  CLASSIFICATION_TYPE: string;
  NORAD_CAT_ID: number;
  ELEMENT_SET_NO: number;
  REV_AT_EPOCH: number;
  BSTAR: number;
  MEAN_MOTION_DOT: number;
  MEAN_MOTION_DDOT: number;
}

function tleChecksum(line: string): number {
  let sum = 0;
  for (const ch of line.slice(0, 68)) {
    if (ch >= '0' && ch <= '9') sum += ch.charCodeAt(0) - 48;
    else if (ch === '-') sum += 1;
  }
  return sum % 10;
}

function assertTleLine(line: string, lineNumber: 1 | 2): string {
  const trimmed = line.replace(/\s+$/, '');
  if (trimmed.length < 69) {
    throw new Error(`TLE line ${lineNumber} is too short (${trimmed.length} chars, need 69)`);
  }
  if (trimmed[0] !== String(lineNumber)) {
    throw new Error(`TLE line ${lineNumber} must start with "${lineNumber}"`);
  }
  const expected = tleChecksum(trimmed);
  const actual = Number(trimmed[68]);
  if (expected !== actual) {
    throw new Error(`TLE line ${lineNumber} checksum mismatch: expected ${expected}, found ${actual}`);
  }
  return trimmed;
}

/** Parse the TLE "implied decimal point" exponent format, e.g. " 28098-4" -> 0.28098e-4. */
function parseImpliedExponent(field: string): number {
  const compact = field.trim();
  if (compact === '' || /^[+-]?0+$/.test(compact)) return 0;
  const match = /^([+-]?)(\d+)([+-]\d)$/.exec(compact);
  if (!match) {
    throw new Error(`invalid TLE exponent field: "${field}"`);
  }
  const sign = match[1] === '-' ? -1 : 1;
  const mantissa = Number(`0.${match[2]}`);
  const exponent = Number(match[3]);
  return sign * mantissa * 10 ** exponent;
}

function tleEpochToIso(epochField: string): string {
  const twoDigitYear = Number(epochField.slice(0, 2));
  const year = twoDigitYear < 57 ? 2000 + twoDigitYear : 1900 + twoDigitYear;
  const dayOfYear = Number(epochField.slice(2));
  if (!Number.isFinite(dayOfYear) || dayOfYear < 1 || dayOfYear >= 367) {
    throw new Error(`invalid TLE epoch day: "${epochField}"`);
  }
  const millisecondsIntoYear = (dayOfYear - 1) * 86_400_000;
  const epoch = new Date(Date.UTC(year, 0, 1) + Math.round(millisecondsIntoYear));
  // Keep microsecond-ish precision out; OMM EPOCH strings in this repo use Z-suffixed ISO.
  return epoch.toISOString().replace(/\.(\d{3})Z$/, (_m, ms) => (ms === '000' ? 'Z' : `.${ms}Z`));
}

/** Format the TLE international designator ("58002B  ") as an OMM OBJECT_ID ("1958-002B"). */
function formatObjectId(designator: string): string {
  const compact = designator.trim();
  if (compact === '') return '';
  const yearDigits = Number(compact.slice(0, 2));
  const year = yearDigits < 57 ? 2000 + yearDigits : 1900 + yearDigits;
  return `${year}-${compact.slice(2)}`;
}

function splitTleInput(tle: string | TleLines, line2?: string, name?: string): TleLines {
  if (typeof tle !== 'string') {
    return tle;
  }
  if (typeof line2 === 'string' && /^2/.test(line2.trim())) {
    return { line1: tle, line2, name };
  }
  const lines = tle
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
  const first = lines.find((line) => line.startsWith('1 '));
  const second = lines.find((line) => line.startsWith('2 '));
  if (!first || !second) {
    throw new Error('TLE input must contain element lines starting with "1 " and "2 "');
  }
  const titleLine = lines.find((line) => line !== first && line !== second);
  return { line1: first, line2: second, name: name ?? titleLine };
}

/**
 * Convert a TLE to a CCSDS OMM record.
 *
 * Accepts either a single string (2 or 3 lines) or (line1, line2[, name]).
 * MEAN_MOTION_DOT / MEAN_MOTION_DDOT are reported in rev/day^2 and rev/day^3
 * per CCSDS 502.0 (the raw TLE fields store n-dot/2 and n-ddot/6).
 */
export function tleToOMM(tle: string | TleLines, line2?: string, name?: string): OmmRecord {
  const lines = splitTleInput(tle, line2, name);
  const l1 = assertTleLine(lines.line1, 1);
  const l2 = assertTleLine(lines.line2, 2);

  const noradFromLine1 = l1.slice(2, 7).trim();
  const noradFromLine2 = l2.slice(2, 7).trim();
  if (noradFromLine1 !== noradFromLine2) {
    throw new Error(`TLE line catalog numbers disagree: ${noradFromLine1} vs ${noradFromLine2}`);
  }

  return {
    CCSDS_OMM_VERS: 3,
    CREATION_DATE: new Date().toISOString().replace(/\.\d{3}Z$/, 'Z'),
    OBJECT_NAME: lines.name?.trim() || `NORAD ${Number(noradFromLine1)}`,
    OBJECT_ID: formatObjectId(l1.slice(9, 17)),
    CENTER_NAME: 'EARTH',
    REFERENCE_FRAME: 'TEME',
    TIME_SYSTEM: 'UTC',
    MEAN_ELEMENT_THEORY: 'SGP4',
    EPOCH: tleEpochToIso(l1.slice(18, 32).trim()),
    MEAN_MOTION: Number(l2.slice(52, 63)),
    ECCENTRICITY: Number(`0.${l2.slice(26, 33).trim()}`),
    INCLINATION: Number(l2.slice(8, 16)),
    RA_OF_ASC_NODE: Number(l2.slice(17, 25)),
    ARG_OF_PERICENTER: Number(l2.slice(34, 42)),
    MEAN_ANOMALY: Number(l2.slice(43, 51)),
    EPHEMERIS_TYPE: 'SGP4',
    CLASSIFICATION_TYPE: l1[7] ?? 'U',
    NORAD_CAT_ID: Number(noradFromLine1),
    ELEMENT_SET_NO: Number(l1.slice(64, 68)),
    REV_AT_EPOCH: Number(l2.slice(63, 68)),
    BSTAR: parseImpliedExponent(l1.slice(53, 61)),
    MEAN_MOTION_DOT: Number(l1.slice(33, 43)) * 2,
    MEAN_MOTION_DDOT: parseImpliedExponent(l1.slice(44, 52)) * 6,
  };
}
