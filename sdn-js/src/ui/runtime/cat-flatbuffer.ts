import * as flatbuffers from 'flatbuffers';
import { CAT } from 'spacedatastandards.org/lib/js/CAT/CAT.js';
import { dataAvailability } from 'spacedatastandards.org/lib/js/CAT/dataAvailability.js';
import { massCategory } from 'spacedatastandards.org/lib/js/CAT/massCategory.js';
import { operationalState } from 'spacedatastandards.org/lib/js/CAT/operationalState.js';
import { orbitRegime } from 'spacedatastandards.org/lib/js/CAT/orbitRegime.js';
import { spaceObjectClass } from 'spacedatastandards.org/lib/js/CAT/spaceObjectClass.js';

export function decodeCatFlatBuffer(bytes: Uint8Array): Record<string, unknown> {
  if (bytes.length === 0) {
    throw new Error('empty CAT FlatBuffer');
  }

  const cat = CAT.getSizePrefixedRootAsCAT(new flatbuffers.ByteBuffer(bytes));
  const record: Record<string, unknown> = {};

  addString(record, 'OBJECT_NAME', cat.OBJECT_NAME());
  addString(record, 'OBJECT_ID', cat.OBJECT_ID());
  addNumber(record, 'NORAD_CAT_ID', cat.NORAD_CAT_ID());
  addEnum(record, 'OBJECT_TYPE', cat.OBJECT_TYPE(), spaceObjectClass);
  addEnum(record, 'OPS_STATUS_CODE', cat.OPS_STATUS_CODE(), operationalState);
  addString(record, 'LAUNCH_DATE', cat.LAUNCH_DATE());
  addString(record, 'LAUNCH_SITE', cat.LAUNCH_SITE());
  addString(record, 'DECAY_DATE', cat.DECAY_DATE());
  addNumber(record, 'PERIOD', cat.PERIOD(), { includeZero: false });
  addNumber(record, 'INCLINATION', cat.INCLINATION(), { includeZero: false });
  addNumber(record, 'APOGEE', cat.APOGEE(), { includeZero: false });
  addNumber(record, 'PERIGEE', cat.PERIGEE(), { includeZero: false });
  addNumber(record, 'RCS', cat.RCS(), { includeZero: false });
  addEnum(record, 'DATA_STATUS_CODE', cat.DATA_STATUS_CODE(), dataAvailability, { skipValue: dataAvailability.NO_CURRENT_ELEMENTS });
  addString(record, 'ORBIT_CENTER', cat.ORBIT_CENTER());
  addEnum(record, 'ORBIT_TYPE', cat.ORBIT_TYPE(), orbitRegime);
  addString(record, 'DEPLOYMENT_DATE', cat.DEPLOYMENT_DATE());
  if (cat.MANEUVERABLE()) record.MANEUVERABLE = true;
  addNumber(record, 'SIZE', cat.SIZE(), { includeZero: false });
  addNumber(record, 'MASS', cat.MASS(), { includeZero: false });
  if (record.MASS) addEnum(record, 'MASS_TYPE', cat.MASS_TYPE(), massCategory);

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
  options: { skipValue?: number } = {},
): void {
  if (value === options.skipValue) return;
  const label = enumValues[value];
  if (label) record[key] = label;
}

function stringValue(value: string | Uint8Array | null): string | null {
  if (typeof value === 'string') return value.trim() || null;
  if (value instanceof Uint8Array) {
    const decoded = new TextDecoder().decode(value).trim();
    return decoded || null;
  }
  return null;
}
