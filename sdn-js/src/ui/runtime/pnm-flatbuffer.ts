import * as flatbuffers from 'flatbuffers';
import { PNM } from 'spacedatastandards.org/lib/js/PNM/PNM.js';

export function decodePnmFlatBuffer(bytes: Uint8Array): Record<string, unknown> {
  if (bytes.length === 0) {
    throw new Error('empty PNM FlatBuffer');
  }

  const pnm = PNM.getSizePrefixedRootAsPNM(new flatbuffers.ByteBuffer(bytes));
  const record: Record<string, unknown> = {};

  addString(record, 'MULTIFORMAT_ADDRESS', pnm.MULTIFORMAT_ADDRESS());
  addString(record, 'PUBLISH_TIMESTAMP', pnm.PUBLISH_TIMESTAMP());
  addString(record, 'CID', pnm.CID());
  addString(record, 'FILE_NAME', pnm.FILE_NAME());
  addString(record, 'FILE_ID', pnm.FILE_ID());
  addString(record, 'SIGNATURE', pnm.SIGNATURE());
  addString(record, 'TIMESTAMP_SIGNATURE', pnm.TIMESTAMP_SIGNATURE());
  addString(record, 'SIGNATURE_TYPE', pnm.SIGNATURE_TYPE());
  addString(record, 'TIMESTAMP_SIGNATURE_TYPE', pnm.TIMESTAMP_SIGNATURE_TYPE());

  return record;
}

function addString(record: Record<string, unknown>, key: string, value: string | Uint8Array | null): void {
  const stringified = stringValue(value);
  if (stringified) record[key] = stringified;
}

function stringValue(value: string | Uint8Array | null): string | null {
  if (typeof value === 'string') return value.trim() || null;
  if (value instanceof Uint8Array) {
    const decoded = new TextDecoder().decode(value).trim();
    return decoded || null;
  }
  return null;
}
