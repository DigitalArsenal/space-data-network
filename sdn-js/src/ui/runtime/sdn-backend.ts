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
  dataBytes?: Uint8Array;
}

export interface RawDataRecordBytes {
  schemaName: string;
  cid: string;
  bytes: Uint8Array;
}

export type SearchMode = 'local' | 'daemon' | 'live-dht';

export interface SharedSearchRequest {
  query?: string;
  schema?: string;
  providerId?: string;
  providerPeerId?: string;
  sourceName?: string;
  batchId?: string;
  queryProfile?: string;
  mode?: SearchMode;
  limit?: number;
}

export interface ProviderSearchRow {
  peerId?: string;
  displayName?: string;
  legalName?: string;
  bitcoinAddress?: string;
  epmCid?: string;
  source?: string;
  updatedAt?: string;
  schemaName?: string;
  providerPeerId?: string;
  providerPublicKey?: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  queryProfile?: string;
  localRows?: number;
  pinnedRows?: number;
  cachedBytes?: number;
  pinnedBytes?: number;
  snapshotId?: string;
  head?: string;
  highWaterMark?: string;
  lastSyncedAt?: string;
  [key: string]: unknown;
}

export interface DataSearchRow {
  schemaName?: string;
  providerId?: string;
  sourceName?: string;
  batchId?: string;
  queryProfile?: string;
  providerPeerId?: string;
  providerPublicKey?: string;
  localRows?: number;
  pinnedRows?: number;
  cachedBytes?: number;
  pinnedBytes?: number;
  snapshotId?: string;
  head?: string;
  highWaterMark?: string;
  lastSyncedAt?: string;
  [key: string]: unknown;
}

export interface SearchResult<T> {
  count: number;
  results: T[];
}

export interface ConjunctionScreenRequest {
  primarySchema: string;
  secondarySchema: string;
  encrypted?: boolean;
  grantId?: string;
  channelId?: string;
  resultChannelId?: string;
  assessorPeerId?: string;
  primaryProviderId?: string;
  primarySourceName?: string;
  primaryPnmCid?: string;
  primaryQuery?: string;
  secondaryProviderId?: string;
  secondarySourceName?: string;
  secondaryPnmCid?: string;
  secondaryQuery?: string;
  moduleId?: string;
  moduleVersion?: string;
  includeProvenance?: boolean;
  limit?: number;
}

export interface ConjunctionEvent {
  primaryObject?: string;
  secondaryObject?: string;
  tca?: string;
  missDistanceKm?: number;
  probability?: number;
  providerId?: string;
  sourceName?: string;
  status?: string;
  [key: string]: unknown;
}

export interface ConjunctionScreenResult {
  workflow: string;
  mode: string;
  status?: string;
  primarySchema: string;
  secondarySchema: string;
  encrypted: boolean;
  grantId?: string;
  channelId?: string;
  resultChannelId?: string;
  assessorPeerId?: string;
  count: number;
  events: ConjunctionEvent[];
  provenance?: Record<string, unknown>;
  sources?: Array<Record<string, unknown>>;
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
  encryptedStreamHeader?: string;
  encryptedRecordIndex?: number | string;
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
  timingsMs: ChannelMonitorTimings;
  lastVerifiedUpdate: string;
}

export interface ChannelMonitorTimings {
  discovery: number;
  grantNegotiation: number;
  pnmDpmVerification: number;
  transfer: number;
  decrypt: number;
  hashVerification: number;
  durableImport: number;
}

export interface ChannelFieldStreamFieldRow {
  fieldPath: string;
  fieldIdPath: number[];
  state: string;
  encoding: string;
  keyId?: string;
  ciphertextLength: number;
  valueLength: number;
  releaseTags: string[];
  decision?: string;
}

export interface ChannelFieldStreamMessage {
  messageId: string;
  providerPeerId: string;
  listingId: string;
  streamId: string;
  schemaCode: string;
  policyId: string;
  policyVersion: number;
  keyEpoch?: string;
  sequence: string;
  subjectId?: string;
  fields: ChannelFieldStreamFieldRow[];
}

export interface ChannelKeyEnvelopeRequest {
  recipientKeyId: string;
  contentKeyId?: string;
}

export interface ChannelBackend {
  list(options?: ChannelListOptions): Promise<BackendResult<ChannelSummary[]>>;
  get(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<ChannelSummary>>;
  subscribe(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<Record<string, unknown>>>;
  unsubscribe(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<Record<string, unknown>>>;
  publish(channelId: string, body?: BodyInit | null, options?: ChannelActionOptions): Promise<BackendResult<Record<string, unknown>>>;
  issueGrant(channelId: string, body?: Record<string, unknown>, options?: ChannelActionOptions): Promise<BackendResult<Record<string, unknown>>>;
  keyUnwrap(channelId: string, body: ChannelKeyEnvelopeRequest, options?: ChannelActionOptions): Promise<BackendResult<Record<string, unknown>>>;
  openStream(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<Uint8Array>>;
  openFieldStream(channelId: string, options?: ChannelActionOptions): Promise<BackendResult<ChannelFieldStreamMessage>>;
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

export interface SdnBackend {
  readonly mode: SdnBackendMode;
  readonly channels: ChannelBackend;
  connect(): Promise<BackendResult<NodeSummary>>;
  getCapabilities(): Promise<BackendCapability[]>;
  getNodeSummary(): Promise<BackendResult<NodeSummary>>;
  getHealth(): Promise<BackendResult<SdnHealth>>;
  getNodeProfile(): Promise<BackendResult<Record<string, unknown>>>;
  saveNodeProfile(profile: Record<string, unknown>): Promise<BackendResult<Record<string, unknown>>>;
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
  searchProviders(request: SharedSearchRequest): Promise<BackendResult<SearchResult<ProviderSearchRow>>>;
  searchData(request: SharedSearchRequest): Promise<BackendResult<SearchResult<DataSearchRow>>>;
  scanRawData(query: RawDataQuery): Promise<BackendResult<DataScanResult>>;
  streamRawData(request: RawDataStreamRequest): Promise<BackendResult<RawDataRecord[]>>;
  queryRawData(query: RawDataQuery): Promise<BackendResult<RawDataRecord[]>>;
  readRawDataRecord(schemaName: string, cid: string): Promise<BackendResult<RawDataRecordBytes>>;
  screenConjunction(request: ConjunctionScreenRequest): Promise<BackendResult<ConjunctionScreenResult>>;
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
