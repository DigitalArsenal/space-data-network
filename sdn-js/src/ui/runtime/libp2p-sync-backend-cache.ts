import {
  createDefaultLibp2pFlatSqlSyncClient,
  createLibp2pFlatSqlSyncBackend,
  type Libp2pFlatSqlSyncBackendOptions,
  type Libp2pFlatSqlSyncClient,
} from './sdn-backend-libp2p-sync';
import type {
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

export class Libp2pFlatSqlSyncBackendCache {
  private readonly clientPromises = new Map<string, Promise<Libp2pFlatSqlSyncClient>>();
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
    return await client.readFlatSqlSyncChunk({
      ...query,
      targetPeerId: normalizedOptions.targetPeerId,
      candidateAddrs: normalizedOptions.candidateAddrs,
      datastoreKey: query.datastoreKey ?? normalizedOptions.datastoreKey ?? undefined,
      providerId: query.providerId ?? normalizedOptions.providerId ?? undefined,
      sourceName: query.sourceName ?? normalizedOptions.sourceName ?? undefined,
    });
  }

  async openFlatSqlSyncManifest(
    options: Libp2pFlatSqlSyncBackendOptions,
    query: FlatSqlSyncQuery,
  ): Promise<FlatSqlSyncManifest> {
    const normalizedOptions = normalizeOptions(options);
    const client = await this.clientFor(normalizedOptions);
    if (!client.openFlatSqlSyncManifest) throw new Error('remote FlatSQL sync manifest is unavailable');
    return await client.openFlatSqlSyncManifest({
      ...query,
      op: 'open_manifest',
      targetPeerId: normalizedOptions.targetPeerId,
      candidateAddrs: normalizedOptions.candidateAddrs,
      datastoreKey: query.datastoreKey ?? normalizedOptions.datastoreKey ?? undefined,
      providerId: query.providerId ?? normalizedOptions.providerId ?? undefined,
      sourceName: query.sourceName ?? normalizedOptions.sourceName ?? undefined,
    });
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

  async destroy(): Promise<void> {
    const clientPromises = Array.from(this.clientPromises.values());
    this.clientPromises.clear();
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
