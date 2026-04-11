import { derivePeerIdFromPublicKey } from './crypto/hd-wallet';

const EPM_FILE_IDENTIFIER = '$EPM';
const EPM_KEY_TYPE_SIGNING = 0;
const EPM_KEYS_FIELD_INDEX = 13;
const EPM_MULTIFORMAT_ADDRESSES_FIELD_INDEX = 14;
const CRYPTO_KEY_PUBLIC_KEY_FIELD_INDEX = 0;
const CRYPTO_KEY_ADDRESS_TYPE_FIELD_INDEX = 5;
const CRYPTO_KEY_TYPE_FIELD_INDEX = 6;

const textDecoder = new TextDecoder();

export interface ServerDescriptor {
  publicKey: string | Uint8Array;
  cid?: string;
  ipns?: string;
  peerId?: string;
  relayAddresses?: string[];
}

export interface ServerDescriptorResolver {
  resolveCID?(cid: string): Promise<Uint8Array>;
  resolveIPNS?(name: string): Promise<Uint8Array>;
}

export interface NormalizedServerDescriptor {
  publicKey: Uint8Array;
  publicKeyHex: string;
  peerId: string;
  cid?: string;
  ipns?: string;
  relayAddresses: string[];
  rawEpmBytes?: Uint8Array;
  source: 'descriptor' | 'epm';
}

export type ServerDescriptorInput = ServerDescriptor | Uint8Array;

interface ParsedEPMDescriptor {
  publicKey: Uint8Array;
  publicKeyHex: string;
  relayAddresses: string[];
}

export async function normalizeServerDescriptor(
  input: ServerDescriptorInput,
  resolver: ServerDescriptorResolver = {},
): Promise<NormalizedServerDescriptor> {
  if (input instanceof Uint8Array) {
    return normalizeEPMDescriptor(input);
  }

  const publicKey = normalizeCompressedPublicKey(input.publicKey);
  const publicKeyHex = bytesToHex(publicKey);

  const derivedPeerId = await derivePeerIdFromPublicKey(publicKey);
  const declaredPeerId = trimOptional(input.peerId);
  if (declaredPeerId && declaredPeerId !== derivedPeerId) {
    throw new Error('provider peer id does not match the declared public key');
  }

  const normalized: NormalizedServerDescriptor = {
    publicKey,
    publicKeyHex,
    peerId: derivedPeerId,
    cid: trimOptional(input.cid),
    ipns: trimOptional(input.ipns),
    relayAddresses: normalizeRelayAddresses(input.relayAddresses),
    source: 'descriptor',
  };

  const resolvedEPM = await resolveDescriptorEPM(normalized, resolver);
  if (resolvedEPM) {
    const parsed = parseEPMDescriptor(resolvedEPM);
    if (!equalBytes(parsed.publicKey, normalized.publicKey)) {
      throw new Error('provider public key mismatch between descriptor and resolved EPM');
    }
    normalized.rawEpmBytes = resolvedEPM;
    if (normalized.relayAddresses.length === 0) {
      normalized.relayAddresses = parsed.relayAddresses;
    }
  }

  return normalized;
}

export async function normalizeEPMDescriptor(epmBytes: Uint8Array): Promise<NormalizedServerDescriptor> {
  const parsed = parseEPMDescriptor(epmBytes);
  return {
    publicKey: parsed.publicKey,
    publicKeyHex: parsed.publicKeyHex,
    peerId: await derivePeerIdFromPublicKey(parsed.publicKey),
    relayAddresses: parsed.relayAddresses,
    rawEpmBytes: epmBytes.slice(),
    source: 'epm',
  };
}

function parseEPMDescriptor(epmBytes: Uint8Array): ParsedEPMDescriptor {
  const table = readRootTable(epmBytes, EPM_FILE_IDENTIFIER);
  const keys = readTableVector(table, EPM_KEYS_FIELD_INDEX);

  let selectedKey: Uint8Array | null = null;
  let selectedKeyHex = '';
  let selectedPriority = Number.POSITIVE_INFINITY;

  for (const keyTable of keys) {
    const publicKeyHex = readStringField(keyTable, CRYPTO_KEY_PUBLIC_KEY_FIELD_INDEX);
    if (!publicKeyHex) {
      continue;
    }

    let publicKey: Uint8Array;
    try {
      publicKey = normalizeCompressedPublicKey(publicKeyHex);
    } catch {
      continue;
    }

    const keyType = readByteField(keyTable, CRYPTO_KEY_TYPE_FIELD_INDEX, EPM_KEY_TYPE_SIGNING);
    const addressType = readStringField(keyTable, CRYPTO_KEY_ADDRESS_TYPE_FIELD_INDEX).toLowerCase();
    const priority = keyPriority(keyType, addressType);
    if (priority < selectedPriority) {
      selectedKey = publicKey;
      selectedKeyHex = bytesToHex(publicKey);
      selectedPriority = priority;
    }
  }

  if (!selectedKey) {
    throw new Error('EPM is missing a compressed secp256k1 provider public key');
  }

  const relayAddresses = readStringVector(table, EPM_MULTIFORMAT_ADDRESSES_FIELD_INDEX)
    .filter((value) => value.trim().length > 0);

  return {
    publicKey: selectedKey,
    publicKeyHex: selectedKeyHex,
    relayAddresses,
  };
}

function keyPriority(keyType: number, addressType: string): number {
  if (keyType === EPM_KEY_TYPE_SIGNING && addressType.includes('secp256k1')) {
    return 0;
  }
  if (keyType === EPM_KEY_TYPE_SIGNING) {
    return 1;
  }
  if (addressType.includes('secp256k1')) {
    return 2;
  }
  return 3;
}

async function resolveDescriptorEPM(
  descriptor: Pick<NormalizedServerDescriptor, 'cid' | 'ipns'>,
  resolver: ServerDescriptorResolver,
): Promise<Uint8Array | null> {
  if (descriptor.cid && resolver.resolveCID) {
    return resolver.resolveCID(descriptor.cid);
  }
  if (descriptor.ipns && resolver.resolveIPNS) {
    return resolver.resolveIPNS(descriptor.ipns);
  }
  return null;
}

function normalizeCompressedPublicKey(value: string | Uint8Array): Uint8Array {
  if (value === undefined || value === null) {
    throw new Error('provider public key is required');
  }
  const bytes = typeof value === 'string' ? hexToBytes(value) : value.slice();
  if (bytes.length !== 33) {
    throw new Error(`provider public key must be a 33-byte compressed secp256k1 key, got ${bytes.length} bytes`);
  }
  if (bytes[0] !== 0x02 && bytes[0] !== 0x03) {
    throw new Error('provider public key must be compressed secp256k1 (0x02/0x03 prefix)');
  }
  return bytes;
}

function normalizeRelayAddresses(values: string[] | undefined): string[] {
  if (!Array.isArray(values)) {
    return [];
  }
  return values
    .map((value) => String(value).trim())
    .filter((value) => value.length > 0);
}

function trimOptional(value: string | undefined): string | undefined {
  const normalized = String(value || '').trim();
  return normalized || undefined;
}

function readRootTable(bytes: Uint8Array, identifier: string): FlatBufferTable {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const identifierOffset = bytes.length >= 8 && readIdentifier(bytes, 4) === identifier ? 0 : 4;
  if (bytes.length < identifierOffset + 8 || readIdentifier(bytes, identifierOffset + 4) !== identifier) {
    throw new Error(`invalid ${identifier} FlatBuffer identifier`);
  }
  const tableOffset = identifierOffset + view.getInt32(identifierOffset, true);
  return { bytes, view, tableOffset };
}

function readIdentifier(bytes: Uint8Array, offset: number): string {
  if (offset + 4 > bytes.length) {
    return '';
  }
  return String.fromCharCode(
    bytes[offset],
    bytes[offset + 1],
    bytes[offset + 2],
    bytes[offset + 3],
  );
}

interface FlatBufferTable {
  bytes: Uint8Array;
  view: DataView;
  tableOffset: number;
}

function readStringField(table: FlatBufferTable, fieldIndex: number): string {
  const offset = readFieldOffset(table, fieldIndex);
  if (offset === 0) {
    return '';
  }

  const relativeOffsetLocation = table.tableOffset + offset;
  const stringOffset = relativeOffsetLocation + table.view.getInt32(relativeOffsetLocation, true);
  const stringLength = table.view.getInt32(stringOffset, true);
  return textDecoder.decode(
    table.bytes.subarray(stringOffset + 4, stringOffset + 4 + stringLength),
  );
}

function readByteField(table: FlatBufferTable, fieldIndex: number, defaultValue: number): number {
  const offset = readFieldOffset(table, fieldIndex);
  return offset === 0 ? defaultValue : table.view.getUint8(table.tableOffset + offset);
}

function readStringVector(table: FlatBufferTable, fieldIndex: number): string[] {
  return readVectorLocations(table, fieldIndex).map((elementOffset) => {
    const stringOffset = elementOffset + table.view.getInt32(elementOffset, true);
    const stringLength = table.view.getInt32(stringOffset, true);
    return textDecoder.decode(
      table.bytes.subarray(stringOffset + 4, stringOffset + 4 + stringLength),
    );
  });
}

function readTableVector(table: FlatBufferTable, fieldIndex: number): FlatBufferTable[] {
  return readVectorLocations(table, fieldIndex).map((elementOffset) => ({
    bytes: table.bytes,
    view: table.view,
    tableOffset: elementOffset + table.view.getInt32(elementOffset, true),
  }));
}

function readVectorLocations(table: FlatBufferTable, fieldIndex: number): number[] {
  const offset = readFieldOffset(table, fieldIndex);
  if (offset === 0) {
    return [];
  }

  const vectorOffsetLocation = table.tableOffset + offset;
  const vectorStart = vectorOffsetLocation + table.view.getInt32(vectorOffsetLocation, true);
  const vectorLength = table.view.getInt32(vectorStart, true);
  const locations: number[] = [];

  for (let index = 0; index < vectorLength; index += 1) {
    locations.push(vectorStart + 4 + index * 4);
  }

  return locations;
}

function readFieldOffset(table: FlatBufferTable, fieldIndex: number): number {
  const vtableOffset = table.tableOffset - table.view.getInt32(table.tableOffset, true);
  const vtableLength = table.view.getUint16(vtableOffset, true);
  const fieldOffsetLocation = vtableOffset + 4 + fieldIndex * 2;
  if (fieldOffsetLocation + 2 > vtableOffset + vtableLength) {
    return 0;
  }
  return table.view.getUint16(fieldOffsetLocation, true);
}

function hexToBytes(value: string): Uint8Array {
  const normalized = value.trim().replace(/^0x/i, '').toLowerCase();
  if (!normalized) {
    throw new Error('provider public key is required');
  }
  if (normalized.length % 2 !== 0) {
    throw new Error('provider public key must be valid hex');
  }

  const bytes = new Uint8Array(normalized.length / 2);
  for (let index = 0; index < bytes.length; index += 1) {
    const byte = Number.parseInt(normalized.slice(index * 2, index * 2 + 2), 16);
    if (Number.isNaN(byte)) {
      throw new Error('provider public key must be valid hex');
    }
    bytes[index] = byte;
  }
  return bytes;
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
}

function equalBytes(left: Uint8Array, right: Uint8Array): boolean {
  if (left.length !== right.length) {
    return false;
  }
  for (let index = 0; index < left.length; index += 1) {
    if (left[index] !== right[index]) {
      return false;
    }
  }
  return true;
}
