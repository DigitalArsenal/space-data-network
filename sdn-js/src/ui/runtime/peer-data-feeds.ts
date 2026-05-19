import { subscriptionKey, type DataDirectoryState } from './data-directory';
import type { DataSummary } from './sdn-backend';

export interface DataSummaryFeedRow {
  datastoreKey?: string;
  schemaName: string;
  providerId: string;
  sourceName: string;
  batchId: string;
  producerPeerId: string;
  producerPublicKey: string;
  count: number;
  totalBytes: number;
}

export interface DataSummaryFeedSelection {
  standardId: string;
  datastoreKey?: string | null;
  providerId?: string | null;
  sourceName?: string | null;
}

export interface PeerDataFeedSource {
  id: string;
  label: string;
  peerId: string;
  publicKey: string | null;
  syncAddrs: string[];
}

export interface PeerDataFeed {
  id: string;
  dataSourceId: string;
  peerId: string;
  datastoreKey: string | null;
  providerName: string;
  providerId: string | null;
  providerPublicKey: string | null;
  sourceName: string | null;
  standardId: string;
  remoteRows: number;
  syncAddrs: string[];
}

export const STORE_FEED_STANDARD_IDS = ['OMM', 'PNM', 'CAT', 'SPW', 'MPE', 'EPM'];

export function dataSummaryListingsForSource(
  source: PeerDataFeedSource,
  summary: DataSummary | null,
): Array<Record<string, unknown>> {
  if (!summary) return [];
  const rows: DataSummaryFeedRow[] = summary.sources.length > 0
    ? summary.sources
    : summary.schemas.map((schema) => ({
      schemaName: schema.schemaName,
      providerId: source.id,
      sourceName: '',
      batchId: '',
      producerPeerId: source.peerId,
      producerPublicKey: source.publicKey ?? '',
      count: schema.count,
      totalBytes: schema.totalBytes,
    }));
  return rows
    .filter((row) => row.count > 0)
    .map((row) => {
      const standardId = standardIdFromSchema(row.schemaName);
      const datastoreKey = stringValue(row.datastoreKey);
      const rowId = datastoreKey ?? [
        row.providerId,
        row.sourceName,
        row.batchId,
        row.schemaName,
      ].filter(Boolean).join(':');
      return {
        kind: 'data',
        id: `${source.id}:${rowId || standardId}`,
        dataSourceId: source.id,
        peerId: source.peerId,
        providerPeerId: source.peerId,
        providerName: source.label,
        publicKey: stringValue(row.producerPublicKey) ?? source.publicKey,
        providerPublicKey: stringValue(row.producerPublicKey) ?? source.publicKey,
        standardId,
        schemaName: row.schemaName,
        remoteRows: row.count,
        recordCount: row.count,
        totalBytes: row.totalBytes,
        datastoreKey,
        providerId: row.providerId,
        sourceName: row.sourceName,
        batchId: row.batchId,
      };
    });
}

export function buildPeerDataFeeds(
  sources: PeerDataFeedSource[],
  listings: Array<Record<string, unknown>>,
  state: DataDirectoryState,
): PeerDataFeed[] {
  const feeds = new Map<string, PeerDataFeed>();
  for (const source of sources) {
    for (const standardId of STORE_FEED_STANDARD_IDS) {
      const key = subscriptionKey(source.id, standardId);
      const subscription = state.subscriptions.find((entry) => entry.id === key);
      feeds.set(key, {
        id: key,
        dataSourceId: source.id,
        peerId: source.peerId,
        datastoreKey: subscription?.datastoreKey ?? null,
        providerName: source.label,
        providerId: subscription?.providerId ?? source.id,
        providerPublicKey: source.publicKey,
        sourceName: subscription?.sourceName ?? null,
        standardId,
        remoteRows: subscription?.remoteRows ?? 0,
        syncAddrs: source.syncAddrs,
      });
    }
  }

  for (const listing of listings) {
    if (listingKind(listing) !== 'data') continue;
    const listingDataSourceId = stringValue(listing.dataSourceId);
    const peerId = listingPeerId(listing);
    const source = sources.find((candidate) => (
      (listingDataSourceId && candidate.id === listingDataSourceId)
      || (peerId && candidate.peerId === peerId)
    )) ?? null;
    const resolvedPeerId = peerId ?? source?.peerId;
    if (!resolvedPeerId) continue;
    const standards = listingStandards(listing);
    for (const standardId of standards.length ? standards : STORE_FEED_STANDARD_IDS) {
      const dataSourceId = listingDataSourceId
        ?? source?.id
        ?? stringValue(listing.id)
        ?? stringValue(listing.listingId)
        ?? `listing:${resolvedPeerId}:${standardId}`;
      const listingDatastoreKey = stringValue(listing.datastoreKey) ?? stringValue(listing.datastore_key) ?? null;
      const key = subscriptionKey(dataSourceId, standardId, listingDatastoreKey);
      const existing = feeds.get(key);
      const listingProviderId = stringValue(listing.providerId) ?? stringValue(listing.provider_id) ?? existing?.providerId ?? null;
      const listingSourceName = stringValue(listing.sourceName) ?? stringValue(listing.source_name) ?? existing?.sourceName ?? null;
      const datastoreKey = listingDatastoreKey ?? existing?.datastoreKey ?? null;
      feeds.set(key, {
        id: key,
        dataSourceId,
        peerId: resolvedPeerId,
        datastoreKey,
        providerName: listingProviderName(listing, source?.label ?? resolvedPeerId),
        providerId: listingProviderId,
        providerPublicKey: stringValue(listing.publicKey) ?? stringValue(listing.providerPublicKey) ?? existing?.providerPublicKey ?? null,
        sourceName: listingSourceName,
        standardId,
        remoteRows: numberValue(listing.remoteRows) ?? numberValue(listing.recordCount) ?? existing?.remoteRows ?? 0,
        syncAddrs: source?.syncAddrs ?? existing?.syncAddrs ?? [],
      });
    }
  }

  return Array.from(feeds.values())
    .filter((feed) => feed.remoteRows > 0 || state.subscriptions.some((subscription) => subscription.id === feed.id))
    .sort((left, right) => left.providerName.localeCompare(right.providerName) || left.standardId.localeCompare(right.standardId));
}

export function preferredDataSummarySource(
  rows: DataSummaryFeedRow[],
  selection: DataSummaryFeedSelection,
): DataSummaryFeedRow | null {
  const standardId = standardIdFromSchema(selection.standardId);
  const matchingSchema = rows.filter((row) => standardIdFromSchema(row.schemaName) === standardId);
  if (matchingSchema.length === 0) return null;

  const datastoreKey = stringValue(selection.datastoreKey);
  if (datastoreKey) {
    return matchingSchema.find((row) => stringValue(row.datastoreKey) === datastoreKey) ?? null;
  }

  const providerId = stringValue(selection.providerId);
  const matchingProvider = providerId
    ? matchingSchema.filter((row) => row.providerId === providerId)
    : [];
  const candidates = matchingProvider.length > 0 ? matchingProvider : matchingSchema;
  const sourceName = stringValue(selection.sourceName);
  if (sourceName) {
    const exactSource = candidates.find((row) => row.sourceName === sourceName);
    if (exactSource) return exactSource;
  }

  return [...candidates].sort((left, right) => (
    right.count - left.count
    || right.totalBytes - left.totalBytes
    || left.sourceName.localeCompare(right.sourceName)
  ))[0] ?? null;
}

function listingKind(listing: Record<string, unknown>): 'data' | 'module' {
  const kind = stringValue(listing.listingKind) ?? stringValue(listing.kind) ?? stringValue(listing.type);
  return kind === 'data' || kind === 'data_stream' || kind === 'dataset' ? 'data' : 'module';
}

function listingPeerId(listing: Record<string, unknown>): string | null {
  return stringValue(listing.peerId)
    ?? stringValue(listing.providerPeerId)
    ?? stringValue(listing.producerPeerId)
    ?? stringValue(listing.authorPeerId)
    ?? null;
}

function listingProviderName(listing: Record<string, unknown>, fallback: string): string {
  return stringValue(listing.providerName)
    ?? stringValue(listing.authorName)
    ?? stringValue(listing.sellerName)
    ?? fallback;
}

function listingStandards(listing: Record<string, unknown>): string[] {
  const values = [
    ...stringArrayValue(listing.standards),
    ...stringArrayValue(listing.standardsUsed),
    ...stringArrayValue(listing.schemas),
    stringValue(listing.standardId),
    stringValue(listing.schemaName),
  ].filter((value): value is string => Boolean(value));
  return Array.from(new Set(values.map(standardIdFromSchema).filter(Boolean)));
}

function standardIdFromSchema(schemaName: string): string {
  return schemaName.split('.')[0]?.trim().toUpperCase() ?? '';
}

function stringArrayValue(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0) : [];
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined;
}

function numberValue(value: unknown): number | null {
  const numeric = Number(value);
  return Number.isFinite(numeric) && numeric >= 0 ? Math.floor(numeric) : null;
}
