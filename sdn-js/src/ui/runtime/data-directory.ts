export type PgpOwnertrust = 'unknown' | 'never' | 'marginal' | 'full' | 'ultimate';
export type StorageUnit = 'MB' | 'GB' | 'TB';
export type DataFeedRetentionPolicy = 'append-only' | 'replace-snapshot';

export interface DataFeedSubscription {
  id: string;
  dataSourceId: string;
  peerId: string;
  datastoreKey: string | null;
  standardId: string;
  providerName: string;
  providerId: string | null;
  providerPublicKey: string | null;
  sourceName: string | null;
  remoteRows: number;
  storageCap: number;
  storageUnit: StorageUnit;
  syncFilter: string;
  queryProfile: string;
  retentionPolicy: DataFeedRetentionPolicy;
  createdAt: string;
  updatedAt: string;
}

export interface DataFeedSubscriptionInput {
  dataSourceId: string;
  peerId: string;
  datastoreKey?: string | null;
  standardId: string;
  providerName: string;
  providerId?: string | null;
  providerPublicKey: string | null;
  sourceName?: string | null;
  remoteRows: number;
  storageCap: number;
  storageUnit: StorageUnit;
  syncFilter: string;
  queryProfile?: string | null;
  retentionPolicy?: string | null;
}

export interface DataDirectoryState {
  peerTrust: Record<string, PgpOwnertrust>;
  subscriptions: DataFeedSubscription[];
}

export interface DataDirectoryMigrationSource {
  dataSourceId: string;
  peerId: string;
  providerName: string;
  providerPublicKey: string | null;
  legacyDataSourceIds?: string[];
  remoteRowsByStandard?: Record<string, number>;
}

export interface LegacySchemaSyncPreference {
  mode?: unknown;
  storageCap?: unknown;
  storageUnit?: unknown;
}

export interface LegacySchemaSyncProgress {
  totalRows?: unknown;
}

export interface DataDirectoryStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

export const DATA_DIRECTORY_STORAGE_KEY = 'sdn:data-directory:v1';
export const PGP_OWNERTRUST_LEVELS: PgpOwnertrust[] = ['unknown', 'never', 'marginal', 'full', 'ultimate'];
export const DEFAULT_OWNERTRUST: PgpOwnertrust = 'unknown';
export const DATA_SOURCE_OWNERTRUST: PgpOwnertrust = 'marginal';
export const DEFAULT_DATA_FEED_QUERY_PROFILE = 'dataset-publication-offset-v1';
export const DEFAULT_DATA_FEED_RETENTION_POLICY: DataFeedRetentionPolicy = 'append-only';
export const DATA_FEED_RETENTION_POLICIES: DataFeedRetentionPolicy[] = ['append-only', 'replace-snapshot'];

const TRUSTED_DIRECTORY_LEVELS = new Set<PgpOwnertrust>(['marginal', 'full', 'ultimate']);
const DATA_FEED_QUERY_PROFILES = new Set(['ordered-offset-v1', 'dataset-publication-offset-v1']);
const CELESTRAK_SOURCE_STANDARDS: Array<{ pattern: RegExp; standards: string[] }> = [
  { pattern: /^celestrak-gp(?:$|-)/, standards: ['OMM', 'MPE'] },
  { pattern: /^celestrak-satcat(?:$|-)|^celestrak-cat(?:$|-)/, standards: ['CAT'] },
  { pattern: /^celestrak-space-weather(?:$|-)/, standards: ['SPW'] },
  { pattern: /^celestrak-publication-log(?:$|-)/, standards: ['PNM'] },
];

export function normalizeOwnertrust(value: unknown): PgpOwnertrust {
  return PGP_OWNERTRUST_LEVELS.includes(value as PgpOwnertrust) ? value as PgpOwnertrust : DEFAULT_OWNERTRUST;
}

export function isTrustedDirectoryOwnertrust(value: unknown): boolean {
  return TRUSTED_DIRECTORY_LEVELS.has(normalizeOwnertrust(value));
}

export function ownertrustForDataSourceSubscription(current: unknown): PgpOwnertrust {
  const normalized = normalizeOwnertrust(current);
  return normalized === 'full' || normalized === 'ultimate' ? normalized : DATA_SOURCE_OWNERTRUST;
}

export function subscriptionKey(dataSourceId: string, standardId: string, datastoreKey?: string | null): string {
  const baseKey = `${dataSourceId}:${normalizeStandardId(standardId)}`;
  const normalizedDatastoreKey = normalizeOptionalString(datastoreKey);
  return normalizedDatastoreKey ? `${baseKey}:datastore:${encodeURIComponent(normalizedDatastoreKey)}` : baseKey;
}

export function updatePeerOwnertrust(
  state: DataDirectoryState,
  peerId: string,
  ownertrust: PgpOwnertrust,
): DataDirectoryState {
  const normalizedPeerId = peerId.trim();
  if (!normalizedPeerId) return normalizeDataDirectoryState(state);
  return {
    ...normalizeDataDirectoryState(state),
    peerTrust: {
      ...normalizeDataDirectoryState(state).peerTrust,
      [normalizedPeerId]: normalizeOwnertrust(ownertrust),
    },
  };
}

export function upsertDataFeedSubscription(
  state: DataDirectoryState,
  input: DataFeedSubscriptionInput,
): DataDirectoryState {
  const currentState = normalizeDataDirectoryState(state);
  const id = subscriptionKey(input.dataSourceId, input.standardId, input.datastoreKey);
  const now = new Date().toISOString();
  const existing = currentState.subscriptions.find((subscription) => subscription.id === id);
  const subscription: DataFeedSubscription = {
    id,
    dataSourceId: input.dataSourceId.trim(),
    peerId: input.peerId.trim(),
    datastoreKey: normalizeOptionalString(input.datastoreKey),
    standardId: normalizeStandardId(input.standardId),
    providerName: input.providerName.trim() || input.peerId.trim() || input.dataSourceId.trim(),
    providerId: normalizeOptionalString(input.providerId),
    providerPublicKey: normalizeOptionalString(input.providerPublicKey),
    sourceName: normalizeSubscriptionSourceName(input.standardId, input.sourceName),
    remoteRows: normalizeNonNegativeInteger(input.remoteRows),
    storageCap: normalizeStorageCap(input.storageCap),
    storageUnit: normalizeStorageUnit(input.storageUnit),
    syncFilter: input.syncFilter.trim(),
    queryProfile: normalizeQueryProfile(input.queryProfile),
    retentionPolicy: normalizeRetentionPolicy(input.retentionPolicy, input.standardId),
    createdAt: existing?.createdAt ?? now,
    updatedAt: now,
  };
  const subscriptions = [
    ...currentState.subscriptions.filter((candidate) => candidate.id !== id),
    subscription,
  ].sort((left, right) => left.providerName.localeCompare(right.providerName) || left.standardId.localeCompare(right.standardId));

  return {
    peerTrust: {
      ...currentState.peerTrust,
      [subscription.peerId]: ownertrustForDataSourceSubscription(currentState.peerTrust[subscription.peerId]),
    },
    subscriptions,
  };
}

export function updateDataFeedSubscription(
  state: DataDirectoryState,
  subscriptionId: string,
  patch: Partial<Pick<DataFeedSubscription, 'providerId' | 'sourceName' | 'remoteRows' | 'storageCap' | 'storageUnit' | 'syncFilter' | 'queryProfile' | 'retentionPolicy'>>,
): DataDirectoryState {
  const currentState = normalizeDataDirectoryState(state);
  const subscriptions = currentState.subscriptions.map((subscription) => {
    if (subscription.id !== subscriptionId) return subscription;
    return normalizeDataFeedSubscription({
      ...subscription,
      ...patch,
      updatedAt: new Date().toISOString(),
    }) ?? subscription;
  });
  return { ...currentState, subscriptions };
}

export function removeDataFeedSubscription(state: DataDirectoryState, subscriptionId: string): DataDirectoryState {
  const currentState = normalizeDataDirectoryState(state);
  return {
    ...currentState,
    subscriptions: currentState.subscriptions.filter((subscription) => subscription.id !== subscriptionId),
  };
}

export function migrateSchemaSyncPreferencesToDataDirectory(
  state: DataDirectoryState,
  preferences: Record<string, LegacySchemaSyncPreference>,
  sources: DataDirectoryMigrationSource[],
  progress: Record<string, LegacySchemaSyncProgress> = {},
): DataDirectoryState {
  let nextState = normalizeDataDirectoryState(state);
  const sourcesById = new Map(sources.map((source) => [source.dataSourceId, source]));
  for (const [key, preference] of Object.entries(preferences)) {
    if (preference?.mode !== 'sync') continue;
    if (nextState.subscriptions.some((subscription) => subscription.id === key)) continue;
    const parsed = parseSchemaSyncPreferenceKey(key);
    if (!parsed) continue;
    const source = sourcesById.get(parsed.dataSourceId);
    if (!source) continue;
    const remoteRows = normalizeNonNegativeInteger(source.remoteRowsByStandard?.[parsed.standardId])
      || normalizeNonNegativeInteger(progress[key]?.totalRows);
    nextState = upsertDataFeedSubscription(nextState, {
      dataSourceId: parsed.dataSourceId,
      peerId: source.peerId,
      standardId: parsed.standardId,
      providerName: source.providerName,
      providerPublicKey: source.providerPublicKey,
      remoteRows,
      storageCap: normalizeStorageCap(preference.storageCap),
      storageUnit: normalizeStorageUnit(preference.storageUnit),
      syncFilter: '',
      queryProfile: DEFAULT_DATA_FEED_QUERY_PROFILE,
    });
  }
  return nextState;
}

export function canonicalizeDataDirectorySourceIds(
  state: DataDirectoryState,
  sources: DataDirectoryMigrationSource[],
): DataDirectoryState {
  const currentState = normalizeDataDirectoryState(state);
  const canonicalByAlias = new Map<string, string>();
  for (const source of sources) {
    const canonicalId = source.dataSourceId.trim();
    if (!canonicalId) continue;
    for (const alias of dataSourceAliasesForMigrationSource(source)) {
      if (alias && alias !== canonicalId) canonicalByAlias.set(alias, canonicalId);
    }
  }

  if (canonicalByAlias.size === 0) return currentState;

  const subscriptions = new Map<string, DataFeedSubscription>();
  for (const subscription of currentState.subscriptions) {
    const canonicalDataSourceId = canonicalByAlias.get(subscription.dataSourceId) ?? subscription.dataSourceId;
    const migrated = canonicalDataSourceId === subscription.dataSourceId
      ? subscription
      : {
          ...subscription,
          id: subscriptionKey(canonicalDataSourceId, subscription.standardId, subscription.datastoreKey),
          dataSourceId: canonicalDataSourceId,
        };
    const existing = subscriptions.get(migrated.id);
    subscriptions.set(migrated.id, existing ? mergeDataFeedSubscriptions(existing, migrated) : migrated);
  }

  return {
    ...currentState,
    subscriptions: Array.from(subscriptions.values())
      .sort((left, right) => left.providerName.localeCompare(right.providerName) || left.standardId.localeCompare(right.standardId)),
  };
}

export function normalizeDataDirectoryState(value: unknown): DataDirectoryState {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return emptyDataDirectoryState();
  const candidate = value as Record<string, unknown>;
  const peerTrust: Record<string, PgpOwnertrust> = {};
  if (candidate.peerTrust && typeof candidate.peerTrust === 'object' && !Array.isArray(candidate.peerTrust)) {
    for (const [peerId, ownertrust] of Object.entries(candidate.peerTrust as Record<string, unknown>)) {
      if (peerId.trim()) peerTrust[peerId.trim()] = normalizeOwnertrust(ownertrust);
    }
  }
  const subscriptions = Array.isArray(candidate.subscriptions)
    ? candidate.subscriptions
      .map(normalizeDataFeedSubscription)
      .filter((subscription): subscription is DataFeedSubscription => subscription !== null)
    : [];
  return { peerTrust, subscriptions };
}

export function loadDataDirectoryState(storage = defaultStorage()): DataDirectoryState {
  if (!storage) return emptyDataDirectoryState();
  try {
    return normalizeDataDirectoryState(JSON.parse(storage.getItem(DATA_DIRECTORY_STORAGE_KEY) ?? '{}'));
  } catch {
    return emptyDataDirectoryState();
  }
}

export function persistDataDirectoryState(state: DataDirectoryState, storage = defaultStorage()): void {
  if (!storage) return;
  try {
    storage.setItem(DATA_DIRECTORY_STORAGE_KEY, JSON.stringify(normalizeDataDirectoryState(state)));
  } catch {
    // Quota or private browsing should not block peer discovery.
  }
}

export function emptyDataDirectoryState(): DataDirectoryState {
  return { peerTrust: {}, subscriptions: [] };
}

function normalizeDataFeedSubscription(value: unknown): DataFeedSubscription | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const candidate = value as Record<string, unknown>;
  const dataSourceId = stringValue(candidate.dataSourceId);
  const peerId = stringValue(candidate.peerId);
  const standardId = normalizeStandardId(candidate.standardId);
  if (!dataSourceId || !peerId || !standardId) return null;
  const datastoreKey = normalizeOptionalString(candidate.datastoreKey);
  const id = datastoreKey ? subscriptionKey(dataSourceId, standardId, datastoreKey) : stringValue(candidate.id) ?? subscriptionKey(dataSourceId, standardId);
  const now = new Date().toISOString();
  return {
    id,
    dataSourceId,
    peerId,
    datastoreKey,
    standardId,
    providerName: stringValue(candidate.providerName) ?? peerId,
    providerId: normalizeOptionalString(candidate.providerId),
    providerPublicKey: normalizeOptionalString(candidate.providerPublicKey),
    sourceName: normalizeSubscriptionSourceName(standardId, candidate.sourceName),
    remoteRows: normalizeNonNegativeInteger(candidate.remoteRows),
    storageCap: normalizeStorageCap(candidate.storageCap),
    storageUnit: normalizeStorageUnit(candidate.storageUnit),
    syncFilter: stringValue(candidate.syncFilter) ?? '',
    queryProfile: normalizeQueryProfile(candidate.queryProfile),
    retentionPolicy: normalizeRetentionPolicy(candidate.retentionPolicy, standardId),
    createdAt: stringValue(candidate.createdAt) ?? now,
    updatedAt: stringValue(candidate.updatedAt) ?? now,
  };
}

function dataSourceAliasesForMigrationSource(source: DataDirectoryMigrationSource): string[] {
  const aliases = new Set<string>(source.legacyDataSourceIds?.map((value) => value.trim()).filter(Boolean) ?? []);
  aliases.add(source.peerId.trim());
  if (source.dataSourceId.startsWith('configured:')) {
    aliases.add(source.dataSourceId.slice('configured:'.length));
  }
  return Array.from(aliases);
}

function mergeDataFeedSubscriptions(left: DataFeedSubscription, right: DataFeedSubscription): DataFeedSubscription {
  return {
    ...left,
    providerName: right.providerName || left.providerName,
    providerId: right.providerId ?? left.providerId,
    providerPublicKey: right.providerPublicKey ?? left.providerPublicKey,
    sourceName: right.sourceName ?? left.sourceName,
    remoteRows: Math.max(left.remoteRows, right.remoteRows),
    storageCap: right.storageCap || left.storageCap,
    storageUnit: right.storageUnit || left.storageUnit,
    syncFilter: right.syncFilter || left.syncFilter,
    queryProfile: right.queryProfile || left.queryProfile,
    retentionPolicy: right.retentionPolicy || left.retentionPolicy,
    createdAt: left.createdAt < right.createdAt ? left.createdAt : right.createdAt,
    updatedAt: left.updatedAt > right.updatedAt ? left.updatedAt : right.updatedAt,
  };
}

function parseSchemaSyncPreferenceKey(key: string): { dataSourceId: string; standardId: string } | null {
  const separatorIndex = key.lastIndexOf(':');
  if (separatorIndex <= 0 || separatorIndex >= key.length - 1) return null;
  const dataSourceId = key.slice(0, separatorIndex).trim();
  const standardId = normalizeStandardId(key.slice(separatorIndex + 1));
  return dataSourceId && standardId ? { dataSourceId, standardId } : null;
}

function normalizeStandardId(value: unknown): string {
  return String(value ?? '').trim().toUpperCase();
}

function normalizeStorageUnit(value: unknown): StorageUnit {
  return value === 'GB' || value === 'TB' ? value : 'MB';
}

function normalizeStorageCap(value: unknown): number {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return 1;
  return Math.max(0.1, Math.min(1_000_000, Math.round(numeric * 10) / 10));
}

function normalizeNonNegativeInteger(value: unknown): number {
  const numeric = Number(value);
  if (!Number.isFinite(numeric) || numeric < 0) return 0;
  return Math.floor(numeric);
}

function normalizeOptionalString(value: unknown): string | null {
  return stringValue(value) ?? null;
}

function normalizeSubscriptionSourceName(standardId: string, value: unknown): string | null {
  const sourceName = normalizeOptionalString(value);
  if (!sourceName) return null;
  const normalizedStandardId = normalizeStandardId(standardId);
  if (sourceName.toUpperCase() === normalizedStandardId) return null;
  const lowerSourceName = sourceName.toLowerCase();
  const knownSource = CELESTRAK_SOURCE_STANDARDS.find((entry) => entry.pattern.test(lowerSourceName));
  if (knownSource && !knownSource.standards.includes(normalizedStandardId)) return null;
  return sourceName;
}

function normalizeQueryProfile(value: unknown): string {
  const candidate = stringValue(value);
  return candidate && DATA_FEED_QUERY_PROFILES.has(candidate) ? candidate : DEFAULT_DATA_FEED_QUERY_PROFILE;
}

export function defaultDataFeedRetentionPolicy(standardId: unknown): DataFeedRetentionPolicy {
  return normalizeStandardId(standardId) === 'CAT' ? 'replace-snapshot' : DEFAULT_DATA_FEED_RETENTION_POLICY;
}

export function normalizeDataFeedRetentionPolicy(value: unknown, standardId: unknown): DataFeedRetentionPolicy {
  return normalizeRetentionPolicy(value, standardId);
}

function normalizeRetentionPolicy(value: unknown, standardId: unknown): DataFeedRetentionPolicy {
  const candidate = stringValue(value);
  return candidate && DATA_FEED_RETENTION_POLICIES.includes(candidate as DataFeedRetentionPolicy)
    ? candidate as DataFeedRetentionPolicy
    : defaultDataFeedRetentionPolicy(standardId);
}

function stringValue(value: unknown): string | null {
  return typeof value === 'string' && value.trim() ? value.trim() : null;
}

function defaultStorage(): DataDirectoryStorage | null {
  return typeof window === 'undefined' ? null : window.localStorage;
}
