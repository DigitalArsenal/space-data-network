import type { HostedEpmRecord } from './identity';
import { normalizeHttpEndpointUrl } from './endpoint-url';
export type { LocalLlmQueryContext, LocalLlmQueryDraft, LocalLlmQueryRequest } from './llm-query-context';

export const BACKEND_MODES = ['desktop-local', 'remote-sdn', 'browser-node'] as const;

export type SdnBackendMode = (typeof BACKEND_MODES)[number];
export type CapabilityState =
  | 'available'
  | 'degraded'
  | 'unavailable'
  | 'permission-required'
  | 'remote-only'
  | 'local-only';

export interface BackendCapability {
  id: string;
  state: CapabilityState;
  reason?: string;
}

export interface BackendResult<T> {
  ok: boolean;
  capability: BackendCapability;
  data: T | null;
}

export interface SdnBackendConfig {
  mode: SdnBackendMode;
  kuboApiUrl: string | null;
  gatewayUrl: string | null;
  desktopProxyUrl: string | null;
  serverUrl: string | null;
}

export interface PartialSdnBackendConfig {
  mode?: string | null;
  kuboApiUrl?: string | null;
  gatewayUrl?: string | null;
  desktopProxyUrl?: string | null;
  serverUrl?: string | null;
}

export interface NodeSummary {
  displayName: string;
  peerId: string | null;
  agentVersion: string | null;
  online: boolean;
  runtime: SdnBackendMode;
}

export interface ObservedSdnPeer {
  id: string;
  name: string;
  addrs: string[];
  trustLevel: string;
  agentVersion?: string;
  protocols?: string[];
  artifactPeerAddrs?: string[];
  metadata?: Record<string, unknown>;
}

export interface StorageSummary {
  usedBytes: number | null;
  pinnedBytes: number | null;
  cacheBytes: number | null;
  quotaBytes: number | null;
}

export interface LocalObjectSummary {
  id: string;
  label: string;
  schema: string | null;
  source: string | null;
  sizeBytes: number | null;
  state: string;
  cid?: string;
}

export interface DataSchemaSummary {
  schemaName: string;
  count: number;
  totalBytes: number;
}

export interface DataSourceSummary {
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

export interface DataSummary {
  totalRecords: number;
  totalBytes: number;
  schemas: DataSchemaSummary[];
  sources: DataSourceSummary[];
}

export interface RawDataQuery {
  schema: string;
  datastoreKey?: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  peerId?: string;
  cursor?: string;
  snapshotId?: string;
  head?: string;
  queryProfile?: string;
  syncFilter?: string;
  limit?: number;
  offset?: number;
}

export interface RawDataRecord {
  schemaName: string;
  cid: string;
  peerId: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  producerPeerId?: string;
  producerPublicKey?: string;
  timestamp?: string;
  sizeBytes: number;
  dataBase64?: string;
  dataBytes?: Uint8Array;
}

export interface RawDataRecordBytes {
  schemaName: string;
  cid: string;
  bytes: Uint8Array;
}

export interface DataScanResult {
  schema: string;
  totalCount: number;
  count: number;
  limit: number;
  offset: number;
  cursor: string;
  nextCursor: string;
  snapshotId: string;
  head: string;
  highWaterMark: string;
  scanHash: string;
  chunkHash: string;
  queryProfile: string;
  syncProtocol: string;
  maxChunkSize: number;
  transports: string[];
  results: RawDataRecord[];
}

export interface ChannelListOptions {
  standardCode?: string;
  visibility?: string;
  subject?: string;
  grantId?: string;
}

export interface ChannelActionOptions {
  subject?: string;
  grantId?: string;
  visibility?: string;
}

export interface ChannelSummary {
  channelId: string;
  sourceId: string;
  standardCode: string;
  feedUuid: string | null;
  visibility: string;
  subscribed: boolean;
  pnmVerified: boolean;
  dpmVerified: boolean;
  grantState: string;
  encryptionState: string;
}

export interface ChannelMonitor extends ChannelSummary {
  channelHead: string;
  providerPeer: string;
  localRows: number;
  remoteRows: number;
  syncedRows: number;
  missingRows: number;
  pinnedCount: number;
  pinnedRows: number;
  syncedBytes: number;
  throughputBytesPerSecond: number;
  wireSpeedUtilization: number | null;
  lastVerifiedUpdate: string;
}

export interface ChannelBackend {
  list(options?: ChannelListOptions): Promise<BackendResult<ChannelSummary[]>>;
  get(channelId: string): Promise<BackendResult<ChannelSummary>>;
  subscribe(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<Record<string, unknown>>>;
  unsubscribe(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<Record<string, unknown>>>;
  publish(channelId: string, body?: BodyInit | null, options?: ChannelActionOptions): Promise<BackendResult<Record<string, unknown>>>;
  issueGrant(channelId: string, body?: Record<string, unknown>, options?: ChannelActionOptions): Promise<BackendResult<Record<string, unknown>>>;
  openStream(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<Uint8Array>>;
  monitor(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<ChannelMonitor>>;
}

export interface RawDataStreamRequest {
  schema: string;
  datastoreKey?: string;
  scanHash?: string;
  chunkHash?: string;
  snapshotId?: string;
  head?: string;
  cursor?: string;
  nextCursor?: string;
  totalCount?: number;
  highWaterMark?: string;
  queryProfile?: string;
  syncFilter?: string;
  records: RawDataRecord[];
}

export interface SdnHealth {
  healthy: boolean;
  details: Record<string, unknown>;
}

export interface NodeAccessUser {
  xpub: string;
  name: string;
  trustLevel: string;
  signingPubKeyHex: string;
  source: string;
  configManaged: boolean;
  createdAt?: string;
  lastLogin?: string;
}

export interface NodeAccessUserInput {
  xpub: string;
  name?: string;
  trustLevel: string;
  signingPubKeyHex?: string;
}

export interface NodeIdentitySettings {
  ttlMs: number | 'app';
  flatbufferStoragePath?: string;
  updatedAt?: string;
  session?: NodeIdentityPersistedSession;
}

export interface FlatbufferStorageLocationSelection {
  canceled: boolean;
  path: string | null;
}

export interface NodeIdentityPersistedSession {
  unlocked: boolean;
  expiresAt?: string | null;
  profile?: Record<string, unknown> | null;
}

export interface WalletNodeIdentityPayload {
  peerId: string;
  xpub?: string;
  walletAccountId?: string;
  walletAccountLabel?: string;
  identityPublicKey?: string;
  signingPublicKey: string;
  encryptionPublicKey?: string;
  signature?: string;
  signaturePayload?: string;
  signatureTimestamp?: number;
}

export interface WalletNodeIdentityApplyOptions {
  replace?: boolean;
}

export interface NodeIdentityApplyResult {
  status: 'updated' | 'unchanged' | 'mismatch';
  profile?: Record<string, unknown>;
  current?: Record<string, unknown>;
  proposed?: Record<string, unknown>;
}

export interface WalletStorageSnapshot {
  entries: Record<string, string>;
  encryptedAtRest: boolean;
  storage?: string;
  updatedAt?: string | null;
}

export interface SdnBackend {
  readonly mode: SdnBackendMode;
  readonly channels: ChannelBackend;
  connect(): Promise<BackendResult<NodeSummary>>;
  getCapabilities(): Promise<BackendCapability[]>;
  getNodeSummary(): Promise<BackendResult<NodeSummary>>;
  getHealth(): Promise<BackendResult<SdnHealth>>;
  getNodeProfile(): Promise<BackendResult<Record<string, unknown>>>;
  saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>>;
  getNodeIdentitySettings(): Promise<BackendResult<NodeIdentitySettings>>;
  saveNodeIdentitySettings(settings: NodeIdentitySettings): Promise<BackendResult<NodeIdentitySettings>>;
  selectFlatbufferStorageLocation(currentPath?: string | null): Promise<BackendResult<FlatbufferStorageLocationSelection>>;
  applyWalletNodeIdentity(payload: WalletNodeIdentityPayload, options?: WalletNodeIdentityApplyOptions): Promise<BackendResult<NodeIdentityApplyResult>>;
  logoutNodeIdentity(): Promise<BackendResult<Record<string, unknown>>>;
  getWalletStorage(): Promise<BackendResult<WalletStorageSnapshot>>;
  saveWalletStorage(entries: Record<string, string | null>): Promise<BackendResult<WalletStorageSnapshot>>;
  listWalletsAndEpms(): Promise<BackendResult<Array<Record<string, unknown>>>>;
  beginClaimEpm(): Promise<BackendResult<Record<string, unknown>>>;
  exportCore(): Promise<BackendResult<Record<string, unknown>>>;
  importCore(core: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>>;
  listNodeAccessUsers(): Promise<BackendResult<NodeAccessUser[]>>;
  saveNodeAccessUser(user: NodeAccessUserInput): Promise<BackendResult<Record<string, unknown>>>;
  revokeNodeAdmin(xpub: string): Promise<BackendResult<Record<string, unknown>>>;
  deleteNodeAccessUser(xpub: string): Promise<BackendResult<Record<string, unknown>>>;
  listHostedEpms(): Promise<BackendResult<HostedEpmRecord[]>>;
  saveHostedEpm(record: HostedEpmRecord): Promise<BackendResult<HostedEpmRecord>>;
  importHostedEpm(input: { name: string; bytes?: Uint8Array; text?: string }): Promise<BackendResult<HostedEpmRecord>>;
  deleteHostedEpm(id: string): Promise<BackendResult<Record<string, unknown>>>;
  downloadHostedEpm(id: string, format: 'json' | 'epm' | 'vcard'): Promise<BackendResult<{ url: string; filename: string }>>;
  listObservedPeers(): Promise<BackendResult<ObservedSdnPeer[]>>;
  listTrustedPeers(): Promise<BackendResult<ObservedSdnPeer[]>>;
  searchDirectory(query: string): Promise<BackendResult<Array<Record<string, unknown>>>>;
  connectPeer(peerId: string): Promise<BackendResult<Record<string, unknown>>>;
  searchListings(query: string): Promise<BackendResult<Array<Record<string, unknown>>>>;
  listOwnedItems(): Promise<BackendResult<Array<Record<string, unknown>>>>;
  requestGrant(listingId: string): Promise<BackendResult<Record<string, unknown>>>;
  installModule(moduleId: string): Promise<BackendResult<Record<string, unknown>>>;
  subscribeDataFeed(feedId: string): Promise<BackendResult<Record<string, unknown>>>;
  getStorageSummary(): Promise<BackendResult<StorageSummary>>;
  listObjects(): Promise<BackendResult<LocalObjectSummary[]>>;
  inspectObject(id: string): Promise<BackendResult<LocalObjectSummary | Record<string, unknown>>>;
  getDataSummary(): Promise<BackendResult<DataSummary>>;
  scanRawData(query: RawDataQuery): Promise<BackendResult<DataScanResult>>;
  streamRawData(request: RawDataStreamRequest): Promise<BackendResult<RawDataRecord[]>>;
  queryRawData(query: RawDataQuery): Promise<BackendResult<RawDataRecord[]>>;
  readRawDataRecord(schemaName: string, cid: string): Promise<BackendResult<RawDataRecordBytes>>;
  pinObject(id: string): Promise<BackendResult<Record<string, unknown>>>;
  unpinObject(id: string): Promise<BackendResult<Record<string, unknown>>>;
  listRulesets(): Promise<BackendResult<Array<Record<string, unknown>>>>;
  saveRuleset(ruleset: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>>;
  runSqlQuery(query: string): Promise<BackendResult<Array<Record<string, unknown>>>>;
  getKuboStatus(): Promise<BackendResult<Record<string, unknown>>>;
  listFiles(path?: string): Promise<BackendResult<Array<Record<string, unknown>>>>;
  resolveCid(cid: string): Promise<BackendResult<{ cid: string; gatewayUrl: string }>>;
  readGatewayUrl(path: string): Promise<BackendResult<{ path: string; gatewayUrl: string }>>;
}

export function isBackendMode(value: string | null | undefined): value is SdnBackendMode {
  return BACKEND_MODES.includes(value as SdnBackendMode);
}

export function normalizeBackendConfig(input: PartialSdnBackendConfig): SdnBackendConfig {
  const mode = isBackendMode(input.mode) ? input.mode : 'desktop-local';
  return {
    mode,
    kuboApiUrl: normalizeEndpointUrl(input.kuboApiUrl) ?? (mode === 'desktop-local' ? 'http://127.0.0.1:5001' : null),
    gatewayUrl: normalizeEndpointUrl(input.gatewayUrl) ?? (mode === 'desktop-local' ? 'http://127.0.0.1:8081' : null),
    desktopProxyUrl: normalizeEndpointUrl(input.desktopProxyUrl) ?? null,
    serverUrl: normalizeEndpointUrl(input.serverUrl) ?? null,
  };
}

export function createCapability(id: string, state: CapabilityState, reason?: string): BackendCapability {
  return reason ? { id, state, reason } : { id, state };
}

export function createAvailableResult<T>(id: string, data: T): BackendResult<T> {
  return { ok: true, capability: createCapability(id, 'available'), data };
}

export function createUnavailableResult<T>(id: string, reason: string): BackendResult<T> {
  return { ok: false, capability: createCapability(id, 'unavailable', reason), data: null };
}

export function createDegradedResult<T>(id: string, reason: string, data: T | null = null): BackendResult<T> {
  return { ok: false, capability: createCapability(id, 'degraded', reason), data };
}

export function createCapabilityResult<T>(
  id: string,
  state: CapabilityState,
  reason: string,
  data: T | null = null,
): BackendResult<T> {
  return { ok: state === 'available', capability: createCapability(id, state, reason), data };
}

function normalizeEndpointUrl(value: string | null | undefined): string | null {
  return normalizeHttpEndpointUrl(value);
}
