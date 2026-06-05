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
  normalizeEPMDescriptor,
  normalizeServerDescriptor,
} from './server-descriptor';
export type {
  NormalizedServerDescriptor,
  ServerDescriptor,
  ServerDescriptorInput,
  ServerDescriptorResolver,
} from './server-descriptor';
export { SDNStorage } from './storage';
export type { StoredRecord, QueryFilter, LogSyncState } from './storage';
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
  DataQueryResponse,
  DataRecord,
  PublishResult,
  BatchPublishResult,
  LogHeadResponse,
  LogEntriesResponse,
  LogHeadsResponse,
  SDNClientOptions,
} from './client';

// Node Resolver
export { resolveNode, detectIdentifierType } from './resolver';
export type { ResolvedNode, ResolveOptions, IdentifierType } from './resolver';

// Transport + Auth
export { HttpTransport } from './transport/http';
export type { LogEntry, LogHeadInfo } from './transport/http';
export { SessionAuth } from './transport/auth';
export type { AuthProvider } from './transport/auth';
