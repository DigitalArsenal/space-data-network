import {
  createDefaultLibp2pFlatSqlSyncClient,
  createLibp2pFlatSqlSyncBackend,
  type Libp2pFlatSqlSyncBackendOptions,
  type Libp2pFlatSqlSyncClient,
} from './sdn-backend-libp2p-sync';
import type {
  FlatSqlPublishedShard,
  FlatSqlPublishedShardBatch,
  FlatSqlSyncChunk,
  FlatSqlSyncManifest,
  FlatSqlSyncQuery,
  FlatSqlWireSpeedProbeQuery,
  FlatSqlWireSpeedProbeResult,
} from '../../flatsql-sync';
import type { SdnBackend } from './sdn-backend';

type Libp2pFlatSqlSyncClientFactory = (
  options: Libp2pFlatSqlSyncBackendOptions,
) => Promise<Libp2pFlatSqlSyncClient>;

const PUBLISHED_SHARD_CLIENT_POOL_SIZE = 8;
const PUBLISHED_SHARD_SLOW_SOURCE_RATIO = 0.5;

interface PublishedShardSourceStats {
  successes: number;
  failures: number;
  ewmaBytesPerMs: number;
  active: number;
}

export class Libp2pFlatSqlSyncBackendCache {
  private readonly clientPromises = new Map<string, Promise<Libp2pFlatSqlSyncClient>>();
  private readonly publishedShardClientPools = new Map<string, Array<Promise<Libp2pFlatSqlSyncClient>>>();
  private readonly publishedShardClientIndexes = new Map<string, number>();
  private readonly publishedShardSourceStats = new Map<string, PublishedShardSourceStats>();
  private readonly backends = new Map<string, SdnBackend>();

  constructor(
    private readonly clientFactory: Libp2pFlatSqlSyncClientFactory = (options) =>
      createDefaultLibp2pFlatSqlSyncClient(normalizeCandidateAddrs(options.candidateAddrs)),
  ) {}

  backendFor(options: Libp2pFlatSqlSyncBackendOptions): SdnBackend {
    const normalizedOptions = normalizeOptions(options);
    const key = cacheKeyFor(normalizedOptions);
    const existing = this.backends.get(key);
    if (existing) return existing;

    const backend = createLibp2pFlatSqlSyncBackend({
      ...normalizedOptions,
      syncClient: undefined,
      nodeFactory: () => this.clientFor(normalizedOptions),
    });
    this.backends.set(key, backend);
    return backend;
  }

  async readFlatSqlSyncChunk(
    options: Libp2pFlatSqlSyncBackendOptions,
    query: FlatSqlSyncQuery,
  ): Promise<FlatSqlSyncChunk> {
    const normalizedOptions = normalizeOptions(options);
    const client = await this.clientFor(normalizedOptions);
    return await client.readFlatSqlSyncChunk(withSyncDefaults({
      ...query,
      targetPeerId: normalizedOptions.targetPeerId,
      candidateAddrs: normalizedOptions.candidateAddrs,
    }, query, normalizedOptions));
  }

  async openFlatSqlSyncManifest(
    options: Libp2pFlatSqlSyncBackendOptions,
    query: FlatSqlSyncQuery,
  ): Promise<FlatSqlSyncManifest> {
    const normalizedOptions = normalizeOptions(options);
    const client = await this.clientFor(normalizedOptions);
    if (!client.openFlatSqlSyncManifest) throw new Error('remote FlatSQL sync manifest is unavailable');
    return await client.openFlatSqlSyncManifest(withSyncDefaults({
      ...query,
      op: 'open_manifest',
      targetPeerId: normalizedOptions.targetPeerId,
      candidateAddrs: normalizedOptions.candidateAddrs,
    }, query, normalizedOptions));
  }

  async readFlatSqlPublishedShard(
    options: Libp2pFlatSqlSyncBackendOptions,
    query: FlatSqlSyncQuery & { cid: string },
  ): Promise<FlatSqlPublishedShard> {
    const normalizedOptions = normalizeOptions(options);
    const client = await this.publishedShardClientFor(normalizedOptions);
    if (!client.readFlatSqlPublishedShard) throw new Error('remote FlatSQL published shard stream is unavailable');
    return await client.readFlatSqlPublishedShard(withSyncDefaults({
      ...query,
      op: 'read_published_shard',
      targetPeerId: normalizedOptions.targetPeerId,
      candidateAddrs: normalizedOptions.candidateAddrs,
    }, query, normalizedOptions));
  }

  async readFlatSqlPublishedShardFromSources(
    sources: Libp2pFlatSqlSyncBackendOptions[],
    query: FlatSqlSyncQuery & { cid: string },
    preferredSourceIndex = 0,
  ): Promise<FlatSqlPublishedShard> {
    const orderedSources = orderedPublishedShardSources(sources, preferredSourceIndex, this.publishedShardSourceStats);
    let lastError: unknown = null;
    for (const source of orderedSources) {
      const startedAt = nowMs();
      recordPublishedShardSourceStart(this.publishedShardSourceStats, source);
      try {
        const shard = await this.readFlatSqlPublishedShard(source, query);
        recordPublishedShardSourceSuccess(this.publishedShardSourceStats, source, shard.streamBytes.byteLength, nowMs() - startedAt);
        return shard;
      } catch (error) {
        recordPublishedShardSourceFailure(this.publishedShardSourceStats, source);
        lastError = error;
      } finally {
        recordPublishedShardSourceEnd(this.publishedShardSourceStats, source);
      }
    }
    if (lastError instanceof Error) throw lastError;
    throw new Error('remote FlatSQL published shard stream is unavailable');
  }

  async readFlatSqlPublishedShardBatch(
    options: Libp2pFlatSqlSyncBackendOptions,
    query: FlatSqlSyncQuery & { cids: string[] },
  ): Promise<FlatSqlPublishedShardBatch> {
    const normalizedOptions = normalizeOptions(options);
    const client = await this.publishedShardClientFor(normalizedOptions);
    if (!client.readFlatSqlPublishedShardBatch) throw new Error('remote FlatSQL published shard batch stream is unavailable');
    return await client.readFlatSqlPublishedShardBatch(withSyncDefaults({
      ...query,
      op: 'read_published_shard_batch',
      targetPeerId: normalizedOptions.targetPeerId,
      candidateAddrs: normalizedOptions.candidateAddrs,
    }, query, normalizedOptions));
  }

  async measureWireSpeed(
    options: Libp2pFlatSqlSyncBackendOptions,
    query: Omit<FlatSqlWireSpeedProbeQuery, 'targetPeerId' | 'candidateAddrs'>,
  ): Promise<FlatSqlWireSpeedProbeResult> {
    const normalizedOptions = normalizeOptions(options);
    const client = await this.clientFor(normalizedOptions);
    if (!client.measureWireSpeed) throw new Error('remote FlatSQL sync wire-speed probe is unavailable');
    return await client.measureWireSpeed({
      ...query,
      targetPeerId: normalizedOptions.targetPeerId,
      candidateAddrs: normalizedOptions.candidateAddrs,
    });
  }

  async releaseControlClient(options: Libp2pFlatSqlSyncBackendOptions): Promise<void> {
    if (options.syncClient) return;
    const normalizedOptions = normalizeOptions(options);
    const key = cacheKeyFor(normalizedOptions);
    const clientPromise = this.clientPromises.get(key);
    if (!clientPromise) return;
    this.clientPromises.delete(key);
    const client = await clientPromise;
    await client.stop?.();
  }

  async destroy(): Promise<void> {
    const clientPromises = Array.from(new Set([
      ...this.clientPromises.values(),
      ...Array.from(this.publishedShardClientPools.values()).flat(),
    ]));
    this.clientPromises.clear();
    this.publishedShardClientPools.clear();
    this.publishedShardClientIndexes.clear();
    this.publishedShardSourceStats.clear();
    this.backends.clear();
    const stopPromises = clientPromises.map(async (clientPromise) => {
      const client = await clientPromise;
      await client.stop?.();
    });
    await Promise.race([
      Promise.allSettled(stopPromises),
      delay(25),
    ]);
  }

  private clientFor(options: Libp2pFlatSqlSyncBackendOptions): Promise<Libp2pFlatSqlSyncClient> {
    const key = cacheKeyFor(options);
    const existing = this.clientPromises.get(key);
    if (existing) return existing;

    let clientPromise: Promise<Libp2pFlatSqlSyncClient>;
    clientPromise = Promise.resolve(options.syncClient ?? options.nodeFactory?.() ?? this.clientFactory(options)).catch((error) => {
      if (this.clientPromises.get(key) === clientPromise) this.clientPromises.delete(key);
      throw error;
    });
    this.clientPromises.set(key, clientPromise);
    return clientPromise;
  }

  private publishedShardClientFor(options: Libp2pFlatSqlSyncBackendOptions): Promise<Libp2pFlatSqlSyncClient> {
    if (options.syncClient) return this.clientFor(options);
    const key = `${cacheKeyFor(options)}:published-shards`;
    let pool = this.publishedShardClientPools.get(key);
    if (!pool) {
      pool = Array.from({ length: PUBLISHED_SHARD_CLIENT_POOL_SIZE }, () => (
        Promise.resolve(options.nodeFactory?.() ?? this.clientFactory(options)).catch((error) => {
          this.publishedShardClientPools.delete(key);
          this.publishedShardClientIndexes.delete(key);
          throw error;
        })
      ));
      this.publishedShardClientPools.set(key, pool);
    }
    const index = this.publishedShardClientIndexes.get(key) ?? 0;
    this.publishedShardClientIndexes.set(key, (index + 1) % pool.length);
    return pool[index] ?? this.clientFor(options);
  }
}

async function delay(milliseconds: number): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, milliseconds));
}

function normalizeOptions(options: Libp2pFlatSqlSyncBackendOptions): Libp2pFlatSqlSyncBackendOptions {
  return {
    ...options,
    targetPeerId: options.targetPeerId.trim(),
    candidateAddrs: normalizeCandidateAddrs(options.candidateAddrs),
  };
}

function orderedPublishedShardSources(
  sources: Libp2pFlatSqlSyncBackendOptions[],
  preferredSourceIndex: number,
  sourceStats = new Map<string, PublishedShardSourceStats>(),
): Libp2pFlatSqlSyncBackendOptions[] {
  const normalizedSources = uniquePublishedShardSources(sources);
  if (normalizedSources.length <= 1) return normalizedSources;
  const preferredIndex = normalizePreferredSourceIndex(preferredSourceIndex, normalizedSources.length);
  const preferredOrder = [
    ...normalizedSources.slice(preferredIndex),
    ...normalizedSources.slice(0, preferredIndex),
  ];
  if (!normalizedSources.some((source) => sourceHasStats(sourceStats, source))) return normalizedSources;
  const preferredRank = new Map(preferredOrder.map((source, index) => [cacheKeyFor(source), index]));
  return [...preferredOrder].sort((left, right) => comparePublishedShardSources(left, right, sourceStats, preferredRank));
}

function comparePublishedShardSources(
  left: Libp2pFlatSqlSyncBackendOptions,
  right: Libp2pFlatSqlSyncBackendOptions,
  sourceStats: Map<string, PublishedShardSourceStats>,
  preferredRank: Map<string, number>,
): number {
  const leftKey = cacheKeyFor(left);
  const rightKey = cacheKeyFor(right);
  const leftStats = sourceStats.get(leftKey);
  const rightStats = sourceStats.get(rightKey);
  const leftAttempts = publishedShardSourceAttempts(leftStats);
  const rightAttempts = publishedShardSourceAttempts(rightStats);
  if (leftAttempts === 0 && (rightStats?.successes ?? 0) > 0) return 1;
  if (rightAttempts === 0 && (leftStats?.successes ?? 0) > 0) return -1;
  if (leftAttempts === 0 && rightAttempts > 0) return -1;
  if (rightAttempts === 0 && leftAttempts > 0) return 1;
  if ((leftStats?.failures ?? 0) !== (rightStats?.failures ?? 0)) {
    return (leftStats?.failures ?? 0) - (rightStats?.failures ?? 0);
  }
  const leftRawBytesPerMs = leftStats?.ewmaBytesPerMs ?? 0;
  const rightRawBytesPerMs = rightStats?.ewmaBytesPerMs ?? 0;
  if (leftAttempts > 0 && rightAttempts > 0 && leftRawBytesPerMs > 0 && rightRawBytesPerMs > 0) {
    if (leftRawBytesPerMs < rightRawBytesPerMs * PUBLISHED_SHARD_SLOW_SOURCE_RATIO) return 1;
    if (rightRawBytesPerMs < leftRawBytesPerMs * PUBLISHED_SHARD_SLOW_SOURCE_RATIO) return -1;
  }
  const leftEffectiveBytesPerMs = effectiveSourceBytesPerMs(leftStats);
  const rightEffectiveBytesPerMs = effectiveSourceBytesPerMs(rightStats);
  if (leftEffectiveBytesPerMs !== rightEffectiveBytesPerMs) {
    return rightEffectiveBytesPerMs - leftEffectiveBytesPerMs;
  }
  return (preferredRank.get(leftKey) ?? 0) - (preferredRank.get(rightKey) ?? 0);
}

function sourceHasStats(
  sourceStats: Map<string, PublishedShardSourceStats>,
  source: Libp2pFlatSqlSyncBackendOptions,
): boolean {
  return publishedShardSourceAttempts(sourceStats.get(cacheKeyFor(source))) > 0;
}

function publishedShardSourceAttempts(stats: PublishedShardSourceStats | undefined): number {
  if (!stats) return 0;
  return stats.successes + stats.failures;
}

function effectiveSourceBytesPerMs(stats: PublishedShardSourceStats | undefined): number {
  if (!stats) return 0;
  return stats.ewmaBytesPerMs / Math.sqrt(1 + Math.max(0, stats.active));
}

function recordPublishedShardSourceStart(
  sourceStats: Map<string, PublishedShardSourceStats>,
  source: Libp2pFlatSqlSyncBackendOptions,
): void {
  const stats = publishedShardSourceStatsFor(sourceStats, source);
  stats.active += 1;
}

function recordPublishedShardSourceEnd(
  sourceStats: Map<string, PublishedShardSourceStats>,
  source: Libp2pFlatSqlSyncBackendOptions,
): void {
  const stats = publishedShardSourceStatsFor(sourceStats, source);
  stats.active = Math.max(0, stats.active - 1);
}

function recordPublishedShardSourceSuccess(
  sourceStats: Map<string, PublishedShardSourceStats>,
  source: Libp2pFlatSqlSyncBackendOptions,
  byteCount: number,
  elapsedMs: number,
): void {
  const current = publishedShardSourceStatsFor(sourceStats, source);
  const sample = Math.max(0, byteCount) / Math.max(1, elapsedMs);
  const ewmaBytesPerMs = current.successes === 0
    ? sample
    : (current.ewmaBytesPerMs * 0.7) + (sample * 0.3);
  current.successes += 1;
  current.ewmaBytesPerMs = ewmaBytesPerMs;
}

function recordPublishedShardSourceFailure(
  sourceStats: Map<string, PublishedShardSourceStats>,
  source: Libp2pFlatSqlSyncBackendOptions,
): void {
  const current = publishedShardSourceStatsFor(sourceStats, source);
  current.failures += 1;
  current.ewmaBytesPerMs *= 0.5;
}

function publishedShardSourceStatsFor(
  sourceStats: Map<string, PublishedShardSourceStats>,
  source: Libp2pFlatSqlSyncBackendOptions,
): PublishedShardSourceStats {
  const key = cacheKeyFor(normalizeOptions(source));
  let stats = sourceStats.get(key);
  if (!stats) {
    stats = {
      successes: 0,
      failures: 0,
      ewmaBytesPerMs: 0,
      active: 0,
    };
    sourceStats.set(key, stats);
  }
  return stats;
}

function nowMs(): number {
  return Date.now();
}

function uniquePublishedShardSources(sources: Libp2pFlatSqlSyncBackendOptions[]): Libp2pFlatSqlSyncBackendOptions[] {
  const unique: Libp2pFlatSqlSyncBackendOptions[] = [];
  const seen = new Set<string>();
  for (const source of sources) {
    const normalized = normalizeOptions(source);
    if (!normalized.targetPeerId || normalized.candidateAddrs.length === 0) continue;
    const key = cacheKeyFor(normalized);
    if (seen.has(key)) continue;
    seen.add(key);
    unique.push(normalized);
  }
  return unique;
}

function normalizePreferredSourceIndex(value: number, sourceCount: number): number {
  if (!Number.isFinite(value) || sourceCount <= 0) return 0;
  const index = Math.floor(value) % sourceCount;
  return index < 0 ? index + sourceCount : index;
}

function normalizeCandidateAddrs(candidateAddrs: string[]): string[] {
  return Array.from(new Set(candidateAddrs.map((addr) => addr.trim()).filter(Boolean))).sort();
}

function cacheKeyFor(options: Libp2pFlatSqlSyncBackendOptions): string {
  return JSON.stringify({
    targetPeerId: options.targetPeerId,
    candidateAddrs: normalizeCandidateAddrs(options.candidateAddrs),
    datastoreKey: options.datastoreKey ?? null,
    providerId: options.providerId ?? null,
    sourceName: options.sourceName ?? null,
  });
}

function withSyncDefaults<T extends FlatSqlSyncQuery>(
  request: T,
  query: FlatSqlSyncQuery,
  options: Libp2pFlatSqlSyncBackendOptions,
): T {
  const normalized = { ...request };
  const queryHasDatastoreKey = Object.prototype.hasOwnProperty.call(query, 'datastoreKey') && query.datastoreKey !== undefined;
  const queryHasProviderId = Object.prototype.hasOwnProperty.call(query, 'providerId') && query.providerId !== undefined;
  const queryHasSourceName = Object.prototype.hasOwnProperty.call(query, 'sourceName') && query.sourceName !== undefined;
  const datastoreKey = normalizeOptionalText(queryHasDatastoreKey ? query.datastoreKey : options.datastoreKey);
  const providerId = normalizeOptionalText(queryHasProviderId ? query.providerId : options.providerId);
  const sourceName = normalizeOptionalText(queryHasSourceName ? query.sourceName : options.sourceName);
  delete normalized.datastoreKey;
  delete normalized.providerId;
  delete normalized.sourceName;
  if (datastoreKey) normalized.datastoreKey = datastoreKey;
  if (providerId) normalized.providerId = providerId;
  if (sourceName) normalized.sourceName = sourceName;
  return normalized;
}

function normalizeOptionalText(value: string | null | undefined): string | undefined {
  const trimmed = value?.trim();
  return trimmed || undefined;
}
