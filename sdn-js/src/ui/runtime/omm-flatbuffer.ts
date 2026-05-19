import * as flatbuffers from 'flatbuffers';
import { OMM } from 'spacedatastandards.org/lib/js/OMM/OMM.js';
import { ephemerisFormat } from 'spacedatastandards.org/lib/js/OMM/ephemerisFormat.js';
import { meanElementSource } from 'spacedatastandards.org/lib/js/OMM/meanElementSource.js';
import { timingStandard } from 'spacedatastandards.org/lib/js/OMM/timingStandard.js';

export function decodeOmmFlatBuffer(bytes: Uint8Array): Record<string, unknown> {
  if (bytes.length === 0) {
    throw new Error('empty OMM FlatBuffer');
  }

  const omm = OMM.getSizePrefixedRootAsOMM(new flatbuffers.ByteBuffer(bytes));
  const record: Record<string, unknown> = {};

  addNumber(record, 'CCSDS_OMM_VERS', omm.CCSDS_OMM_VERS(), { includeZero: false });
  addString(record, 'CREATION_DATE', omm.CREATION_DATE());
  addString(record, 'ORIGINATOR', omm.ORIGINATOR());
  addString(record, 'OBJECT_NAME', omm.OBJECT_NAME());
  addString(record, 'OBJECT_ID', omm.OBJECT_ID());
  addString(record, 'CENTER_NAME', omm.CENTER_NAME());
  addString(record, 'REFERENCE_FRAME_EPOCH', omm.REFERENCE_FRAME_EPOCH());
  addEnum(record, 'TIME_SYSTEM', omm.TIME_SYSTEM(), timingStandard);
  addEnum(record, 'MEAN_ELEMENT_THEORY', omm.MEAN_ELEMENT_THEORY(), meanElementSource);
  addString(record, 'COMMENT', omm.COMMENT());
  addString(record, 'EPOCH', omm.EPOCH());
  addNumber(record, 'SEMI_MAJOR_AXIS', omm.SEMI_MAJOR_AXIS(), { includeZero: false });
  addNumber(record, 'MEAN_MOTION', omm.MEAN_MOTION());
  addNumber(record, 'ECCENTRICITY', omm.ECCENTRICITY());
  addNumber(record, 'INCLINATION', omm.INCLINATION());
  addNumber(record, 'RA_OF_ASC_NODE', omm.RA_OF_ASC_NODE());
  addNumber(record, 'ARG_OF_PERICENTER', omm.ARG_OF_PERICENTER());
  addNumber(record, 'MEAN_ANOMALY', omm.MEAN_ANOMALY());
  addNumber(record, 'GM', omm.GM(), { includeZero: false });
  addNumber(record, 'MASS', omm.MASS(), { includeZero: false });
  addNumber(record, 'SOLAR_RAD_AREA', omm.SOLAR_RAD_AREA(), { includeZero: false });
  addNumber(record, 'SOLAR_RAD_COEFF', omm.SOLAR_RAD_COEFF(), { includeZero: false });
  addNumber(record, 'DRAG_AREA', omm.DRAG_AREA(), { includeZero: false });
  addNumber(record, 'DRAG_COEFF', omm.DRAG_COEFF(), { includeZero: false });
  addEnum(record, 'EPHEMERIS_TYPE', omm.EPHEMERIS_TYPE(), ephemerisFormat);
  addString(record, 'CLASSIFICATION_TYPE', omm.CLASSIFICATION_TYPE());
  addNumber(record, 'NORAD_CAT_ID', omm.NORAD_CAT_ID());
  addNumber(record, 'ELEMENT_SET_NO', omm.ELEMENT_SET_NO(), { includeZero: false });
  addNumber(record, 'REV_AT_EPOCH', omm.REV_AT_EPOCH(), { includeZero: false });
  addNumber(record, 'BSTAR', omm.BSTAR());
  addNumber(record, 'MEAN_MOTION_DOT', omm.MEAN_MOTION_DOT(), { includeZero: false });
  addNumber(record, 'MEAN_MOTION_DDOT', omm.MEAN_MOTION_DDOT(), { includeZero: false });
  addNumber(record, 'USER_DEFINED_BIP_0044_TYPE', omm.USER_DEFINED_BIP_0044_TYPE(), { includeZero: false });
  addString(record, 'USER_DEFINED_OBJECT_DESIGNATOR', omm.USER_DEFINED_OBJECT_DESIGNATOR());
  addString(record, 'USER_DEFINED_EARTH_MODEL', omm.USER_DEFINED_EARTH_MODEL());
  addNumber(record, 'USER_DEFINED_EPOCH_TIMESTAMP', omm.USER_DEFINED_EPOCH_TIMESTAMP(), { includeZero: false });
  addNumber(record, 'USER_DEFINED_MICROSECONDS', omm.USER_DEFINED_MICROSECONDS(), { includeZero: false });

  const covariance = numberVector(omm.covarianceLength(), (index) => omm.COVARIANCE(index));
  if (covariance.length > 0) record.COVARIANCE = covariance;

  return record;
}

function addString(record: Record<string, unknown>, key: string, value: string | Uint8Array | null): void {
  const stringified = stringValue(value);
  if (stringified) record[key] = stringified;
}

function addNumber(
  record: Record<string, unknown>,
  key: string,
  value: number | null,
  options: { includeZero?: boolean } = {},
): void {
  if (value == null || !Number.isFinite(value)) return;
  if (value === 0 && options.includeZero === false) return;
  record[key] = value;
}

function addEnum<T extends Record<number, string>>(
  record: Record<string, unknown>,
  key: string,
  value: number,
  enumValues: T,
): void {
  record[key] = enumValues[value] ?? String(value);
}

function numberVector(length: number, read: (index: number) => number | null): number[] {
  const values: number[] = [];
  for (let index = 0; index < length; index += 1) {
    const value = read(index);
    if (typeof value === 'number' && Number.isFinite(value)) values.push(value);
  }
  return values;
}

function stringValue(value: string | Uint8Array | null): string | null {
  if (typeof value === 'string') return value.trim() || null;
  if (value instanceof Uint8Array) {
    const decoded = new TextDecoder().decode(value).trim();
    return decoded || null;
  }
  return null;
}
