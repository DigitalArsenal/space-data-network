import {
  type DirectoryImportRequest,
  type DirectoryImportResult,
  type DirectoryNodeRecord,
  type DirectoryRecordBase,
  type DirectoryRecordKind,
  type DirectorySnapshot,
  type DirectoryUserRecord,
} from './types';

export type {
  DirectoryAdapter,
  DirectoryImportRequest,
  DirectoryImportResult,
  DirectoryNodeRecord,
  DirectoryRecordBase,
  DirectoryRecordKind,
  DirectorySnapshot,
  DirectoryUserRecord,
} from './types';

export function createDirectorySnapshot(
  seed: Partial<DirectorySnapshot> & { query: string },
): DirectorySnapshot {
  return {
    query: normalizeDirectoryQuery(seed.query),
    nodes: seed.nodes ? seed.nodes.map(cloneDirectoryNodeRecord) : [],
    users: seed.users ? seed.users.map(cloneDirectoryUserRecord) : [],
  };
}

export function cloneDirectorySnapshot(snapshot: DirectorySnapshot): DirectorySnapshot {
  return createDirectorySnapshot(snapshot);
}

export function normalizeDirectoryQuery(query: string): string {
  return String(query ?? '').trim();
}

export function normalizeDirectoryRecord(
  input: Record<string, unknown>,
  kind: 'node',
): DirectoryNodeRecord;
export function normalizeDirectoryRecord(
  input: Record<string, unknown>,
  kind: 'user',
): DirectoryUserRecord;
export function normalizeDirectoryRecord(
  input: Record<string, unknown>,
  kind: DirectoryRecordKind,
): DirectoryNodeRecord | DirectoryUserRecord;
export function normalizeDirectoryRecord(
  input: Record<string, unknown>,
  kind: DirectoryRecordKind,
): DirectoryNodeRecord | DirectoryUserRecord {
  const peerId = pickString(input, ['peer_id', 'peerId']) ?? '';
  const base: DirectoryRecordBase = {
    kind,
    peer_id: peerId,
    dn: pickString(input, ['dn', 'display_name', 'displayName', 'name']) ?? undefined,
    legal_name: pickString(input, ['legal_name', 'legalName']) ?? undefined,
    bitcoin_address: pickString(input, ['bitcoin_address', 'bitcoinAddress']) ?? undefined,
    epm_cid: pickString(input, ['epm_cid', 'epmCid']) ?? undefined,
    source: pickString(input, ['source']) ?? undefined,
    updated_at: pickNumber(input, ['updated_at', 'updatedAt']) ?? undefined,
  };
  return base as DirectoryNodeRecord | DirectoryUserRecord;
}

export function matchesDirectoryRecord(
  record: DirectoryRecordBase,
  query: string,
): boolean {
  const normalizedQuery = normalizeDirectoryQuery(query).toLowerCase();
  if (!normalizedQuery) {
    return true;
  }

  return [
    record.peer_id,
    record.dn,
    record.legal_name,
    record.bitcoin_address,
    record.epm_cid,
    record.source,
  ].some((value) => String(value ?? '').toLowerCase().includes(normalizedQuery));
}

export function isDirectoryRecordKind(value: unknown): value is DirectoryRecordKind {
  return value === 'node' || value === 'user';
}

export function splitDirectoryRecords(
  records: Array<Record<string, unknown>>,
): DirectorySnapshot {
  const nodes: DirectoryNodeRecord[] = [];
  const users: DirectoryUserRecord[] = [];

  for (const record of records) {
    const kind = inferDirectoryKind(record);
    if (kind === 'node') {
      nodes.push(normalizeDirectoryRecord(record, kind));
    } else if (kind === 'user') {
      users.push(normalizeDirectoryRecord(record, kind));
    }
  }

  return createDirectorySnapshot({
    query: '',
    nodes,
    users,
  });
}

export function normalizeDirectoryImportResult(payload: unknown): DirectoryImportResult {
  if (!isRecord(payload)) {
    return { imported: 0, nodes: [], users: [] };
  }
  return {
    imported: pickNumber(payload, ['imported', 'count']) ?? 0,
    nodes: normalizeDirectoryImportItems(payload.nodes, 'node'),
    users: normalizeDirectoryImportItems(payload.users, 'user'),
  };
}

export function normalizeDirectoryImportRequest(
  request: DirectoryImportRequest,
): DirectoryImportResult {
  const parsedVCard = request.vcard ? parseDirectoryVCard(request.vcard) : null;
  const rawRecord = request.epm_json ?? request.record ?? parsedVCard?.record;
  if (!rawRecord) {
    throw new Error('epm_json, record, or vcard is required');
  }

  const kind = request.kind
    ?? parsedVCard?.kind
    ?? inferDirectoryKind(rawRecord)
    ?? undefined;
  if (!kind || !isDirectoryRecordKind(kind)) {
    throw new Error('directory record kind is required');
  }

  const record = normalizeDirectoryRecord({
    ...rawRecord,
    kind,
    directory_kind: kind,
    epm_cid: request.epm_cid ?? parsedVCard?.epm_cid ?? pickString(rawRecord, ['epm_cid', 'epmCid']),
    source: request.source ?? pickString(rawRecord, ['source']) ?? 'manual-upload',
  }, kind);
  if (!record.peer_id) {
    throw new Error(`peer_id is required for ${kind} directory record`);
  }

  return {
    imported: 1,
    nodes: kind === 'node' ? [record as DirectoryNodeRecord] : [],
    users: kind === 'user' ? [record as DirectoryUserRecord] : [],
  };
}

export function inferDirectoryKind(record: Record<string, unknown>): DirectoryRecordKind | null {
  const kind = pickString(record, ['kind', 'directory_kind']);
  if (isDirectoryRecordKind(kind)) {
    return kind;
  }

  const entityType = pickEntityType(record, ['entity_type', 'ENTITY_TYPE', 'entityType']);
  if (entityType) {
    return entityType;
  }

  if (pickString(record, ['bitcoin_address', 'bitcoinAddress', 'epm_cid', 'epmCid'])) {
    return 'node';
  }
  return 'user';
}

export function pickDirectoryItems(payload: unknown): Array<Record<string, unknown>> {
  if (Array.isArray(payload)) {
    return payload.filter(isRecord);
  }
  if (!isRecord(payload)) {
    return [];
  }
  const candidates = ['results', 'items', 'records', 'nodes', 'users'];
  for (const key of candidates) {
    const value = payload[key];
    if (Array.isArray(value)) {
      return value.filter(isRecord);
    }
  }
  return [];
}

function normalizeDirectoryImportItems(
  payload: unknown,
  kind: 'node',
): DirectoryNodeRecord[];
function normalizeDirectoryImportItems(
  payload: unknown,
  kind: 'user',
): DirectoryUserRecord[];
function normalizeDirectoryImportItems(
  payload: unknown,
  kind: 'node' | 'user',
): Array<DirectoryNodeRecord | DirectoryUserRecord> {
  return pickDirectoryItems(payload)
    .map((record) => kind === 'node'
      ? normalizeDirectoryRecord(record, 'node')
      : normalizeDirectoryRecord(record, 'user'));
}

function parseDirectoryVCard(vcard: string): {
  kind?: DirectoryRecordKind;
  epm_cid?: string;
  record: Record<string, unknown>;
} {
  const fields = parseVCardFields(vcard);
  const kind = fields.get('X-SDN-DIRECTORY-KIND')?.toLowerCase();
  const record: Record<string, unknown> = {};
  setIfPresent(record, 'peer_id', fields.get('X-SDN-PEER-ID') ?? fields.get('UID'));
  setIfPresent(record, 'dn', fields.get('FN'));
  setIfPresent(record, 'legal_name', fields.get('ORG'));
  setIfPresent(record, 'bitcoin_address', fields.get('X-SDN-BITCOIN-ADDRESS') ?? fields.get('X-BITCOIN-ADDRESS'));
  return {
    kind: isDirectoryRecordKind(kind) ? kind : undefined,
    epm_cid: fields.get('X-SDN-EPM-CID'),
    record,
  };
}

function parseVCardFields(vcard: string): Map<string, string> {
  const fields = new Map<string, string>();
  const unfolded: string[] = [];
  for (const line of String(vcard ?? '').split(/\r?\n/)) {
    if (/^[ \t]/.test(line) && unfolded.length) {
      unfolded[unfolded.length - 1] += line.slice(1);
    } else {
      unfolded.push(line);
    }
  }
  for (const line of unfolded) {
    const separator = line.indexOf(':');
    if (separator < 0) {
      continue;
    }
    const name = line.slice(0, separator).split(';', 1)[0]?.trim().toUpperCase();
    const value = line.slice(separator + 1).trim();
    if (name && value && !fields.has(name)) {
      fields.set(name, value);
    }
  }
  return fields;
}

function setIfPresent(record: Record<string, unknown>, key: string, value: string | undefined) {
  if (value?.trim()) {
    record[key] = value.trim();
  }
}

function cloneDirectoryNodeRecord(record: DirectoryNodeRecord): DirectoryNodeRecord {
  return { ...record };
}

function cloneDirectoryUserRecord(record: DirectoryUserRecord): DirectoryUserRecord {
  return { ...record };
}

function pickString(payload: Record<string, unknown>, keys: string[]): string | null {
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === 'string' && value.trim()) {
      return value.trim();
    }
  }
  return null;
}

function pickNumber(payload: Record<string, unknown>, keys: string[]): number | null {
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === 'number' && Number.isFinite(value)) {
      return value;
    }
    if (typeof value === 'string' && value.trim()) {
      const parsed = Number(value);
      if (Number.isFinite(parsed)) {
        return parsed;
      }
    }
  }
  return null;
}

function pickEntityType(payload: Record<string, unknown>, keys: string[]): DirectoryRecordKind | null {
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === 'string') {
      const normalized = value.trim().toLowerCase();
      if (normalized === 'node' || normalized === '1') {
        return 'node';
      }
      if (normalized === 'user' || normalized === '0') {
        return 'user';
      }
    }
    if (typeof value === 'number') {
      if (value === 1) {
        return 'node';
      }
      if (value === 0) {
        return 'user';
      }
    }
  }
  return null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object';
}
