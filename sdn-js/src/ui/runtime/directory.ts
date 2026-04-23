import {
  type DirectoryNodeRecord,
  type DirectoryRecordBase,
  type DirectoryRecordKind,
  type DirectorySnapshot,
  type DirectoryUserRecord,
} from './types';

export type {
  DirectoryAdapter,
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

export function inferDirectoryKind(record: Record<string, unknown>): DirectoryRecordKind | null {
  const kind = pickString(record, ['kind', 'directory_kind']);
  if (isDirectoryRecordKind(kind)) {
    return kind;
  }

  if (pickString(record, ['bitcoin_address', 'bitcoinAddress', 'epm_cid', 'epmCid'])) {
    return 'node';
  }
  if (pickString(record, ['legal_name', 'legalName', 'dn', 'display_name', 'displayName', 'name'])) {
    return 'user';
  }
  return null;
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object';
}
