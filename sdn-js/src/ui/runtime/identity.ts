import { sha256 } from '../../crypto/hd-wallet';

export type HostedEpmKind = 'node-self' | 'hosted';

export interface HostedEpmRecord {
  id: string;
  kind: HostedEpmKind;
  label: string;
  peerId: string;
  epmCid?: string;
  epmJson: Record<string, unknown>;
  source?: string;
  updatedAt?: number;
}

export interface ChunkedQrPayloadOptions {
  id: string;
  mimeType: string;
  maxPayloadChars: number;
}

const QR_PREFIX = 'sdn-epm-qr:v1:';
const SECRET_KEYS = new Set([
  'private_key',
  'PRIVATE_KEY',
  'xpriv',
  'XPRIV',
  'mnemonic',
  'seed',
  'core',
  'privateKey',
  'secret',
  'encrypted_core',
  'encryptedCore',
]);
const NORMALIZED_SECRET_KEYS = new Set([
  'core',
  'encryptedcore',
  'mnemonic',
  'seed',
  'xpriv',
  'walletprivate',
  'walletprivatekey',
  'walletprivatematerial',
]);

export function normalizeHostedEpmRecord(input: Record<string, unknown>): HostedEpmRecord {
  const epmJson = normalizeRecord(input.epm_json ?? input.epmJson ?? input);
  const id = pickString(input, ['id', 'epm_id', 'epmId']) || pickString(epmJson, ['peer_id', 'peerId']) || 'self';
  const kind = input.kind === 'node-self' ? 'node-self' : 'hosted';
  return {
    id,
    kind,
    label: pickString(epmJson, ['dn', 'DN', 'displayName', 'name']) || id,
    peerId: pickString(epmJson, ['peer_id', 'peerId', 'PeerID']) || '',
    epmCid: pickString(input, ['epm_cid', 'epmCid']) || pickString(epmJson, ['epm_cid', 'epmCid']),
    epmJson: createPublicEpmExport(epmJson),
    source: pickString(input, ['source']),
    updatedAt: pickNumber(input, ['updated_at', 'updatedAt']),
  };
}

export function createPublicEpmExport(input: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(input)) {
    if (isSecretEpmKey(key)) continue;
    if (Array.isArray(value)) {
      out[key] = value.map((item) => (isRecord(item) ? createPublicEpmExport(item) : item));
    } else if (isRecord(value)) {
      out[key] = createPublicEpmExport(value);
    } else {
      out[key] = value;
    }
  }
  return out;
}

export function createVCardQrPayload(input: Record<string, unknown> | HostedEpmRecord): string {
  const record = isHostedEpmRecord(input) ? input : normalizeHostedEpmRecord(input);
  const epm = createPublicEpmExport(record.epmJson);
  const lines = ['BEGIN:VCARD', 'VERSION:3.0'];
  addVCardLine(lines, 'FN', pickString(epm, ['dn', 'DN']) || record.label);
  addVCardLine(lines, 'X-SDN-DIRECTORY-KIND', record.kind === 'node-self' ? 'node' : 'user');
  addVCardLine(lines, 'X-SDN-PEER-ID', record.peerId);
  addVCardLine(lines, 'X-SDN-EPM-CID', record.epmCid || pickString(epm, ['epm_cid', 'epmCid']));
  addVCardLine(lines, 'X-SDN-PUBLIC-KEY', pickString(epm, ['public_key', 'PUBLIC_KEY', 'signing_pubkey_hex']));
  lines.push('END:VCARD');
  return lines.join('\r\n');
}

export async function createChunkedQrPayloads(
  bytes: Uint8Array,
  options: ChunkedQrPayloadOptions,
): Promise<string[]> {
  const digest = bytesToHex(await sha256(bytes));
  const encoded = bytesToBase64(bytes);
  const payloadChars = Math.max(1, options.maxPayloadChars - 320);
  const total = Math.max(1, Math.ceil(encoded.length / payloadChars));
  const chunks: string[] = [];

  for (let index = 0; index < total; index += 1) {
    const payload = encoded.slice(index * payloadChars, (index + 1) * payloadChars);
    chunks.push(
      `${QR_PREFIX}${JSON.stringify({
        id: options.id,
        index,
        mimeType: options.mimeType,
        payload,
        sha256: digest,
        total,
      })}`,
    );
  }

  return chunks;
}

export async function reassembleChunkedQrPayloads(chunks: string[]): Promise<Uint8Array> {
  const parsed = chunks.map(parseChunk);
  if (parsed.length === 0) {
    throw new Error('Missing QR chunk payloads');
  }

  const first = parsed[0];
  const byIndex = new Map<number, ChunkedQrPayload>();
  for (const chunk of parsed) {
    if (chunk.id !== first.id || chunk.mimeType !== first.mimeType || chunk.sha256 !== first.sha256 || chunk.total !== first.total) {
      throw new Error('QR chunk metadata mismatch');
    }
    if (byIndex.has(chunk.index)) {
      throw new Error(`Duplicate QR chunk ${chunk.index}`);
    }
    byIndex.set(chunk.index, chunk);
  }

  for (let index = 0; index < first.total; index += 1) {
    if (!byIndex.has(index)) {
      throw new Error(`Missing QR chunk ${index + 1} of ${first.total}`);
    }
  }

  const payload = Array.from({ length: first.total }, (_, index) => byIndex.get(index)!.payload).join('');
  const bytes = base64ToBytes(payload);
  const digest = bytesToHex(await sha256(bytes));
  if (digest !== first.sha256) {
    throw new Error('QR payload digest mismatch');
  }
  return bytes;
}

interface ChunkedQrPayload {
  id: string;
  index: number;
  mimeType: string;
  payload: string;
  sha256: string;
  total: number;
}

function parseChunk(value: string): ChunkedQrPayload {
  if (!value.startsWith(QR_PREFIX)) {
    throw new Error('Invalid SDN EPM QR chunk prefix');
  }
  const parsed = JSON.parse(value.slice(QR_PREFIX.length)) as Partial<ChunkedQrPayload>;
  const index = parsed.index;
  const total = parsed.total;
  if (
    typeof parsed.id !== 'string' ||
    typeof parsed.mimeType !== 'string' ||
    typeof parsed.payload !== 'string' ||
    typeof parsed.sha256 !== 'string' ||
    typeof index !== 'number' ||
    typeof total !== 'number' ||
    !Number.isInteger(index) ||
    !Number.isInteger(total) ||
    index < 0 ||
    total <= 0 ||
    index >= total
  ) {
    throw new Error('Invalid SDN EPM QR chunk metadata');
  }
  return { id: parsed.id, index, mimeType: parsed.mimeType, payload: parsed.payload, sha256: parsed.sha256, total };
}

function addVCardLine(lines: string[], key: string, value: string | undefined): void {
  if (value?.trim()) lines.push(`${key}:${value.replace(/\r?\n/g, ' ')}`);
}

function normalizeRecord(value: unknown): Record<string, unknown> {
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value);
      return isRecord(parsed) ? parsed : {};
    } catch {
      return {};
    }
  }
  return isRecord(value) ? { ...value } : {};
}

function pickString(input: Record<string, unknown>, keys: string[]): string | undefined {
  for (const key of keys) {
    const value = input[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return undefined;
}

function pickNumber(input: Record<string, unknown>, keys: string[]): number | undefined {
  for (const key of keys) {
    const value = input[key];
    if (typeof value === 'number' && Number.isFinite(value)) return value;
  }
  return undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function isSecretEpmKey(key: string): boolean {
  if (SECRET_KEYS.has(key)) return true;
  const normalized = key.replace(/[^a-z0-9]/gi, '').toLowerCase();
  return (
    NORMALIZED_SECRET_KEYS.has(normalized) ||
    normalized.includes('encryptedcore') ||
    normalized.includes('walletprivate') ||
    normalized.includes('private') ||
    normalized.includes('secret') ||
    normalized.includes('mnemonic') ||
    normalized.includes('xpriv') ||
    normalized === 'seed' ||
    normalized.endsWith('seed')
  );
}

function isHostedEpmRecord(value: Record<string, unknown> | HostedEpmRecord): value is HostedEpmRecord {
  return (
    isRecord(value.epmJson) &&
    typeof value.id === 'string' &&
    (value.kind === 'node-self' || value.kind === 'hosted') &&
    typeof value.label === 'string' &&
    typeof value.peerId === 'string'
  );
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}

function bytesToBase64(bytes: Uint8Array): string {
  const binary = Array.from(bytes, (byte) => String.fromCharCode(byte)).join('');
  if (typeof btoa === 'function') return btoa(binary);
  return Buffer.from(bytes).toString('base64');
}

function base64ToBytes(value: string): Uint8Array {
  if (typeof atob === 'function') {
    return Uint8Array.from(atob(value), (char) => char.charCodeAt(0));
  }
  return new Uint8Array(Buffer.from(value, 'base64'));
}
