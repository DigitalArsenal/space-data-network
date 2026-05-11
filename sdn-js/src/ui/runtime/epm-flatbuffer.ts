import * as flatbuffers from 'flatbuffers';
import { EPM } from 'spacedatastandards.org/lib/js/EPM/EPM.js';
import { EntityType } from 'spacedatastandards.org/lib/js/EPM/EntityType.js';
import { KeyType } from 'spacedatastandards.org/lib/js/EPM/KeyType.js';

export function decodeEpmFlatBuffer(bytes: Uint8Array): Record<string, unknown> {
  if (bytes.length === 0) {
    throw new Error('empty EPM FlatBuffer');
  }

  const epm = EPM.getSizePrefixedRootAsEPM(new flatbuffers.ByteBuffer(bytes));
  const multiformatAddress = stringVector(epm.multiformatAddressLength(), (index) => epm.MULTIFORMAT_ADDRESS(index));
  const keys = decodePublicKeys(epm);
  const profile: Record<string, unknown> = {
    directory_kind: epm.ENTITY_TYPE() === EntityType.Node ? 'node' : 'user',
    entity_type: epm.ENTITY_TYPE() === EntityType.Node ? 'node' : 'user',
  };

  addString(profile, 'dn', epm.DN());
  addString(profile, 'legal_name', epm.LEGAL_NAME());
  addString(profile, 'family_name', epm.FAMILY_NAME());
  addString(profile, 'given_name', epm.GIVEN_NAME());
  addString(profile, 'additional_name', epm.ADDITIONAL_NAME());
  addString(profile, 'honorific_prefix', epm.HONORIFIC_PREFIX());
  addString(profile, 'honorific_suffix', epm.HONORIFIC_SUFFIX());
  addString(profile, 'job_title', epm.JOB_TITLE());
  addString(profile, 'occupation', epm.OCCUPATION());
  addString(profile, 'email', epm.EMAIL());
  addString(profile, 'telephone', epm.TELEPHONE());
  addString(profile, 'signature', epm.SIGNATURE());

  const alternateNames = stringVector(epm.alternateNamesLength(), (index) => epm.ALTERNATE_NAMES(index));
  if (alternateNames.length > 0) profile.alternate_names = alternateNames;
  if (keys.length > 0) {
    profile.keys = keys;
    const signing = keys.find((key) => key.key_type === 'signing');
    const encryption = keys.find((key) => key.key_type === 'encryption');
    if (signing?.public_key) profile.signing_public_key = signing.public_key;
    if (encryption?.public_key) profile.encryption_public_key = encryption.public_key;
  }
  if (multiformatAddress.length > 0) {
    profile.multiformat_address = multiformatAddress;
    const peerId = peerIdFromMultiaddrs(multiformatAddress);
    if (peerId) profile.peer_id = peerId;
  }

  const timestamp = epm.SIGNATURE_TIMESTAMP();
  if (timestamp > 0n) {
    profile.signature_timestamp = timestamp <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(timestamp) : timestamp.toString();
  }

  return profile;
}

function decodePublicKeys(epm: EPM): Array<Record<string, string>> {
  const keys: Array<Record<string, string>> = [];
  for (let index = 0; index < epm.keysLength(); index += 1) {
    const key = epm.KEYS(index);
    if (!key) continue;
    const record: Record<string, string> = {};
    addString(record, 'public_key', key.PUBLIC_KEY());
    addString(record, 'xpub', key.XPUB());
    addString(record, 'key_address', key.KEY_ADDRESS());
    addString(record, 'address_type', key.ADDRESS_TYPE());
    if (key.KEY_TYPE() === KeyType.Encryption) record.key_type = 'encryption';
    if (key.KEY_TYPE() === KeyType.Signing) record.key_type = 'signing';
    if (Object.keys(record).length > 0) keys.push(record);
  }
  return keys;
}

function stringVector(length: number, read: (index: number) => string | Uint8Array | null): string[] {
  const values: string[] = [];
  for (let index = 0; index < length; index += 1) {
    const value = stringValue(read(index));
    if (value) values.push(value);
  }
  return values;
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

function peerIdFromMultiaddrs(addresses: string[]): string | null {
  for (let index = addresses.length - 1; index >= 0; index -= 1) {
    const matches = [...addresses[index].matchAll(/\/(?:p2p|ipfs|ipns)\/([^/]+)/g)];
    const last = matches.at(-1)?.[1];
    if (last) return last;
  }
  return null;
}
