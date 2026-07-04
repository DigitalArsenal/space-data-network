/**
 * Space Data Network JavaScript Library
 *
 * A browser-compatible P2P library for space data standards.
 */

export * from './version-info.generated';
export { SDNNode } from './node';
export { createHeliaSDNNode } from './helia';
export type { HeliaSDNNode } from './helia';
export type { SDNConfig, SDNNodeEvents } from './node';
export { IPFS_BOOTSTRAP_PEERS, LEGACY_ID_EXCHANGE_PROTOCOL } from './node';
export {
  FLATSQL_SYNC_PROTOCOL_ID,
  decodeFlatSqlSyncChunk,
  decodeFlatSqlSyncManifest,
  decodeFlatSqlWireSpeedProbe,
  encodeFlatSqlWireSpeedProbeRequest,
  encodeFlatSqlSyncRequest,
  requestFlatSqlSyncChunk,
  requestFlatSqlSyncManifest,
  requestFlatSqlWireSpeedProbe,
} from './flatsql-sync';
export type {
  FlatSqlSyncChunk,
  FlatSqlSyncHeader,
  FlatSqlSyncManifest,
  FlatSqlSyncManifestSegment,
  FlatSqlSyncQuery,
  FlatSqlSyncRecordRef,
  FlatSqlSyncTransport,
  FlatSqlWireSpeedProbeQuery,
  FlatSqlWireSpeedProbeResult,
} from './flatsql-sync';
export {
  MODULE_DELIVERY_PROTOCOL_ID,
  ModuleDeliveryProtocolError,
  fetchEncryptedModuleBundle,
  requestEncryptedModuleBundle,
  requestModuleGrant,
} from './module-delivery';
export type {
  DiscoveredProvider,
  EncryptedModuleBundleResult,
  ModuleDeliveryEvent,
  ModuleDeliveryObserver,
  ModuleDeliveryStage,
  ModuleDeliveryTransport,
  ModuleGrantRequestOptions,
  ModuleGrantResult,
  RequesterIdentity,
} from './module-delivery';
export {
  buildFieldStreamGrantSignaturePayload,
  buildFieldStreamProviderSignaturePayload,
  createFieldStreamReplayGuard,
  decodeFieldStreamMessageSummary,
  decodeFieldStreamPolicySummary,
  resolveFieldStreamMessageView,
  verifyFieldStreamGrantSignature,
  verifyFieldStreamProviderSignature,
} from './field-stream';
export type {
  FieldStreamAccessGrant,
  FieldStreamAudienceSummary,
  FieldStreamDecryptFieldInput,
  FieldStreamFieldSummary,
  FieldStreamFieldVisibility,
  FieldStreamGrantPolicyPayload,
  FieldStreamGrantSignatureInput,
  FieldStreamMessageSummary,
  FieldStreamMessageView,
  FieldStreamPolicySummary,
  FieldStreamProviderSignatureInput,
  FieldStreamReplayGuard,
  FieldStreamResolvedField,
  FieldStreamRuleSummary,
  ResolveFieldStreamMessageViewOptions,
} from './field-stream';
export {
  normalizeEPMDescriptor,
  normalizeServerDescriptor,
} from './server-descriptor';
export type {
  NormalizedServerDescriptor,
  ServerDescriptor,
  ServerDescriptorInput,
  ServerDescriptorResolver,
} from './server-descriptor';
// The legacy IndexedDB `SDNStorage` backend was removed in loop D.5; the
// FlatSQL-WASM engine record store below is THE record store.
export type { StoredRecord, QueryFilter } from './storage';
// THE SDNNode store (loop D.1): the FlatSQL-WASM engine record store.
export {
  FlatSQLEngineRecordStore,
  PersistenceStoreSnapshotPersistence,
  MemorySnapshotPersistence,
  HeliaSnapshotPersistence,
  flatBufferMatchesFileId,
} from './engine-record-store';
export type {
  FlatSQLEngineRecordStoreOptions,
  EngineQueryParam,
  SnapshotPersistence,
  HeliaLike,
  // Datasync-fed store (loop D.4): sync/published-shard ingest into the engine.
  EngineSyncChunkIngestOptions,
  EnginePublishedShardIngestOptions,
  EngineSyncIngestResult,
} from './engine-record-store';
// PRIMARY public query API (loop D.2): engine-native epoch profiles —
// same template SQL/params as sdn-server engine_records.go, aligned raw
// FlatBuffer streams out.
export {
  ENGINE_EPOCH_PROFILES,
  ENGINE_EPOCH_FALLBACK_PROFILE,
  ENGINE_EPOCH_FALLBACK_LIMIT,
  DEFAULT_ENGINE_EPOCH_SPECS,
  normalizeEngineEpochProfile,
  buildEngineEpochProfileSql,
  resolveEngineEpochQuery,
  buildEpochProfileSql,
  EPOCH_SQL_PROFILES,
  EPOCH_SQL_PROFILE_DESCRIPTIONS,
} from './epoch-query-sql';
export type {
  EngineEpochProfile,
  EngineEpochProfileSpec,
  EngineEpochQuery,
  EngineEpochQueryRequest,
  EngineEpochProfileDefaults,
  EngineEpochQueryProfilesConfig,
  EpochSqlProfile,
  EpochProfileSqlOptions,
} from './epoch-query-sql';
// HTTP transport flatbuffers-first (loop D.3): ONE conditional stream
// request; 304 served from the local engine store.
export { RemoteEpochStreamClient } from './remote-epoch-stream';
export type {
  EpochStreamTransport,
  EpochStreamLocalStore,
  RemoteEpochStreamRequest,
  RemoteEpochStreamResult,
} from './remote-epoch-stream';
// Per-standard engine store surface (per-source shadow tables + unified views).
export {
  createLocalFlatSqlStore,
  clearLocalFlatSqlStore,
  createDefaultLocalFlatSqlPersistenceStore,
  MemoryFlatSqlPersistenceStore,
  LOCAL_FLATSQL_DEFAULT_SOURCE,
  decodeFlatSqlSizePrefixedStream,
  iterateFlatSqlSizePrefixedStream,
  engineEpochSpecForSchema,
  flatSqlSizePrefixedStreamInfo,
  stripSdnFlatBufferSizePrefix,
  isReadOnlyFlatSqlQuery,
} from './local-flatsql';
export type {
  LocalFlatSqlStore,
  LocalFlatSqlStoreOptions,
  LocalFlatSqlSchema,
  LocalFlatSqlIngestRecord,
  LocalFlatSqlIngestOptions,
  LocalFlatSqlStreamIngestOptions,
  LocalFlatSqlQueryOptions,
  LocalFlatSqlQueryResult,
  LocalFlatSqlStandardStats,
  LocalFlatSqlPinLedgerEntry,
  LocalFlatSqlPinLedgerQuery,
  LocalFlatSqlPersistenceStore,
} from './local-flatsql';
// Compatibility alias over the engine record store (published surface).
export { FlatSQLStorage } from './flatsql-storage';
export type { FlatSQLStorageOptions } from './flatsql-storage';
export type { NodeRecordStorage } from './node';
// Datasync session (loop D.4): bounded cursor walk into the engine store.
export { syncFlatSqlSchemaIntoStore } from './datasync-session';
export type {
  FlatSqlSchemaSyncOptions,
  FlatSqlSchemaSyncSummary,
  FlatSqlSyncChunkTransport,
} from './datasync-session';
export { createModuleHostCapabilityAdapters } from './module-host-adapters';
export {
  eciesWrap,
  eciesUnwrap,
  eciesWrapForRecipients,
  EciesKeyExchange,
  DEFAULT_GRANT_CONTEXT,
} from './ecies';
export type {
  EciesWrapResult,
  EciesRecipient,
  EciesRecipientEnvelope,
} from './ecies';
export {
  ChannelKeys,
  encryptChannelMessage,
  decryptChannelMessage,
  channelChatTopic,
} from './channel-keys';
export type {
  ChannelMember,
  ChannelMemberEnvelope,
  ChannelKeysOptions,
  ChannelMessage,
  EncryptChannelMessageOptions,
} from './channel-keys';
export { secp256k1ECDH, secp256k1PublicKey } from './crypto/hd-wallet';
export {
  buildSignedPnm,
  verifySignedPnm,
  publishSignedPnm,
  signAndPublishPnm,
  pnmSignaturePayload,
  PNM_SCHEMA,
  PNM_TOPIC,
} from './pnm-publisher';
export type {
  BuildSignedPnmOptions,
  SignedPnmEvidence,
  RawTopicPublisher,
} from './pnm-publisher';
export {
  createModuleRuntimeManager,
  MemoryModuleBytesStore,
  IndexedDbModuleBytesStore,
} from './module-lifecycle';
export type {
  CachedModuleBytes,
  ModuleBytesStore,
  ModuleHarnessLike,
  ModuleLoader,
  ModuleRuntimeManagerOptions,
  RunningModule,
  TimerScheduleOverride,
} from './module-lifecycle';
export type {
  ModuleHostAdapterOptions,
  ModuleHostCapabilityAdapters,
  ModuleHostRecordStore,
  ModuleHostKeySlotResolver,
} from './module-host-adapters';
export { preloadFlatSQLWASI, getFlatSQLWASIPath } from './flatsql';
export { loadEdgeRelays, getBootstrapRelays, DEFAULT_EDGE_RELAYS, EdgeDiscovery, multiaddrToStatusURL } from './edge-discovery';
export type { RelayStatus, RelayProbeResult, DiscoveryMetrics } from './edge-discovery';
export { SDS_SCHEMAS, SUPPORTED_SCHEMAS } from './schemas';
export type { SchemaName } from './schemas';
export {
  CHANNEL_TOPIC_PREFIX,
  assertStandardCode,
  channelDiscoveryTopic,
  formatChannelId,
  parseChannelId,
  schemaNameFromStandardCode,
  standardCodeFromSchemaName,
} from './channels';
export type { ChannelIdInput, ParsedChannelId } from './channels';
export {
  createChannelStreamDispatcher,
  createEncryptedChannelStreamDispatcher,
} from './channel-stream';
export type {
  ChannelStreamDispatcher,
  ChannelStreamDispatcherOptions,
  ChannelStreamPushOptions,
  ChannelStreamStats,
  ChannelStreamType,
  EncryptedChannelStreamDispatcherOptions,
  NativeStreamingDispatcher,
} from './channel-stream';
// Crypto and HD Wallet exports (unified from hd-wallet-wasm)
export {
  // Initialization
  initHDWallet,
  isHDWalletAvailable,
  injectEntropy,
  hasEntropy,

  // Mnemonic
  generateMnemonic,
  validateMnemonic,
  mnemonicToSeed,

  // Key derivation
  deriveEd25519Key,
  deriveEd25519KeyPair,
  ed25519PublicKey,
  x25519PublicKey,
  deriveSecp256k1Key,

  // PeerID
  derivePeerIdFromPublicKey,
  derivePeerIdFromXpub,
  deriveIpnsHashFromXpub,

  // SDN identity
  deriveIdentity,
  identityFromMnemonic,
  deriveXPub,
  derivePublicIdentityKeysFromXpub,

  // Signing
  sign,
  verify,

  // Encryption
  encrypt,
  decrypt,
  encryptBytes,
  decryptBytes,

  // ECDH
  x25519ECDH,

  // Utilities
  randomBytes,
  generateKey,
  sha256,

  // Constants
  LanguageCode,
  SDNDerivation,
  buildIdentityPath,
  buildSigningPath,
  buildEncryptionPath,
} from './crypto/index';
export type {
  HDWalletOptions,
  MnemonicOptions,
  DerivedKey,
  KeyPair,
  IdentityKeyPair,
  EncryptionKeyPair,
  DerivedIdentity,
  XpubDerivedPublicIdentityKeys,
} from './crypto/index';

// Key Storage
export { HDKeyStore } from './crypto/key-store';
export type { WalletMetadata } from './crypto/key-store';

// DHT Discovery + Baked Keys
export {
  MODULE_DELIVERY_DISCOVERY_NAMESPACE,
  computeProviderDiscoveryCID,
  deriveProviderPeerId,
  discoverProvider,
} from './discovery';

// EPM Resolution
export {
  EPMResolver,
  createEPMResolver,
  KeyType,
} from './epm-resolver';
export type {
  EPMKey,
  ParsedEPM,
  EPMResolverOptions,
  KeyExchangeAlgorithm,
  ChainProof,
} from './epm-resolver';

// EPM Attestation (content signing + chain binding proofs)
export {
  buildCanonicalPayload,
  buildEPMSigningContent,
  signEPMContent,
  verifyEPMSignature,
  buildBitcoinChainProof,
  buildEthereumChainProof,
  buildSolanaChainProof,
  buildAllChainProofs,
  verifyChainProof,
  verifyAllChainProofs,
} from 'hd-wallet-wasm';

// Subscription Management
export {
  SubscriptionManager,
  defaultSubscriptionManager,
  evaluateFilter,
  evaluateFilters,
  validateSubscriptionConfig,
  createDefaultConfig,
  generateSubscriptionId,
  serializeRoutingHeader,
  deserializeRoutingHeader,
  getSchemaRoutingTopic,
  getPeerRoutingTopic,
  StreamingMode,
} from './subscription';
export type {
  SubscriptionConfig,
  QueryFilter as SubscriptionQueryFilter,
  RoutingHeader,
  ActiveSubscription,
  SubscriptionEvent,
  SubscriptionEventType,
  SubscriptionEventHandler,
} from './subscription';

// Unified Client
export { SDNClient, SDNTransportError } from './client';
export type {
  NodeCatalog,
  SchemaCatalogEntry,
  DataQueryOptions,
  DataQueryStreamResult,
  DataQueryJsonResult,
  PublishResult,
  BatchPublishResult,
  LogHeadResponse,
  LogHeadsResponse,
  ChannelAccessOptions,
  ChannelActionResponse,
  ChannelGrantRequest,
  ChannelListOptions,
  ChannelMonitor,
  ChannelSummary,
  SDNClientChannels,
  SDNClientOptions,
} from './client';

// Node Resolver
export { resolveNode, detectIdentifierType } from './resolver';
export type { ResolvedNode, ResolveOptions, IdentifierType } from './resolver';

// Module dependency resolver + installer (decentralized package manager;
// browser mirror of the Go internal/deps package).
export {
  resolveClosure,
  installClosure,
  MapModuleCatalog,
  DependencyError,
  compareSemver,
  satisfies,
} from './module-dependency-resolver';
export type {
  ModuleDependency,
  ModuleManifest,
  ModuleCatalog,
  InstalledModules,
  PlanStep,
  InstallFn,
  DependencyErrorCode,
} from './module-dependency-resolver';
export {
  InstalledModuleRegistry,
  MemoryRegistryStore,
  LocalStorageRegistryStore,
  createModuleInstaller,
} from './installed-module-registry';
export type {
  InstalledModuleRecord,
  RegistryStore,
  FetchAndDecrypt,
  RegisterModule,
  ModuleInstallerOptions,
} from './installed-module-registry';

// Transport + Auth
export {
  HttpTransport,
  FLATBUFFER_STREAM_CONTENT_TYPE,
  iterateSizePrefixedFrames,
} from './transport/http';
export type { LogHeadInfo, HttpTransportOptions } from './transport/http';
export { SessionAuth } from './transport/auth';
export type { AuthProvider } from './transport/auth';
