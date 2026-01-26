/**
 * Space Data Network JavaScript Library
 *
 * A browser-compatible P2P library for space data standards.
 */

export { SDNNode } from './node';
export type { SDNConfig, SDNNodeEvents } from './node';
export { SDNStorage } from './storage';
export type { StoredRecord, QueryFilter } from './storage';
export { loadEdgeRelays, getBootstrapRelays, DEFAULT_EDGE_RELAYS } from './edge-discovery';
export { SDS_SCHEMAS, SUPPORTED_SCHEMAS } from './schemas';
export type { SchemaName } from './schemas';
// Legacy crypto exports (backward compatible)
export {
  loadCryptoModule,
  isCryptoAvailable,
  encrypt,
  decrypt,
  sign,
  verify,
  generateKey,
  randomBytes,
  sha256,
  isCryptoWasmVerified,
  getCryptoMetrics,
} from './crypto';
export type { CryptoMetrics } from './crypto';

// New HD Wallet exports
export {
  initHDWallet,
  isHDWalletAvailable,
  injectEntropy,
  hasEntropy,
  generateMnemonic,
  validateMnemonic,
  mnemonicToSeed,
  deriveEd25519Key,
  deriveEd25519KeyPair,
  ed25519PublicKey,
  x25519PublicKey,
  deriveIdentity,
  identityFromMnemonic,
  x25519ECDH,
  encryptBytes,
  decryptBytes,
  LanguageCode,
  ErrorCode,
  SDNDerivation,
  buildSigningPath,
  buildEncryptionPath,
} from './crypto/index';
export type {
  HDWalletOptions,
  MnemonicOptions,
  DerivedKey,
  KeyPair,
  EncryptionKeyPair,
  DerivedIdentity,
} from './crypto/index';

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
} from './epm-resolver';

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

// Storefront / Marketplace
export {
  StorefrontClient,
  createStorefrontClient,
  AccessType,
  PaymentMethod,
  GrantStatus,
  PurchaseStatus,
  ReviewStatus,
} from './storefront';
export type {
  StorefrontClientConfig,
  StorefrontEvents,
  Listing,
  AccessGrant,
  PurchaseRequest,
  Review,
  ReviewStats,
  SearchQuery,
  SearchResult,
  SearchFacets,
  CreditsBalance,
  PricingTier,
  DataCoverage,
  SpatialCoverage,
  TemporalCoverage,
  ProviderReputation,
  DataQualityMetrics,
  DeliveryMethod,
  CreateListingRequest,
  CreatePurchaseRequest,
  CreateReviewRequest,
} from './storefront';
