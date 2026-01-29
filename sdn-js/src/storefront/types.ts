/**
 * Storefront/Marketplace types for Space Data Network
 */

/** Access type for storefront listings */
export enum AccessType {
  OneTime = 0,
  Subscription = 1,
  Streaming = 2,
  Query = 3,
}

/** Payment methods supported */
export enum PaymentMethod {
  CryptoETH = 0,
  CryptoSOL = 1,
  CryptoBTC = 2,
  CryptoUSDC = 3,
  SDNCredits = 4,
  FiatStripe = 5,
  Free = 6,
}

/** Grant status */
export enum GrantStatus {
  Active = 0,
  Revoked = 1,
  Expired = 2,
  Suspended = 3,
  Pending = 4,
}

/** Purchase status */
export enum PurchaseStatus {
  Pending = 0,
  PaymentDetected = 1,
  PaymentConfirmed = 2,
  Completed = 3,
  Failed = 4,
  Cancelled = 5,
  RefundRequested = 6,
  Refunded = 7,
  Expired = 8,
}

/** Review status */
export enum ReviewStatus {
  Published = 0,
  Pending = 1,
  Flagged = 2,
  Hidden = 3,
  Removed = 4,
}

/** Spatial coverage definition */
export interface SpatialCoverage {
  type: 'global' | 'region' | 'object_list' | 'custom';
  regions?: string[]; // LEO, MEO, GEO, HEO
  objectIds?: string[]; // NORAD catalog IDs
  minAltitudeKm?: number;
  maxAltitudeKm?: number;
  geoBounds?: [number, number, number, number]; // [min_lat, min_lon, max_lat, max_lon]
}

/** Temporal coverage definition */
export interface TemporalCoverage {
  startEpoch?: string; // ISO 8601
  endEpoch?: string; // ISO 8601
  updateFrequency: 'realtime' | 'hourly' | 'daily' | 'weekly' | 'monthly';
  historicalDepthDays?: number;
  latencySeconds?: number;
}

/** Combined data coverage */
export interface DataCoverage {
  spatial: SpatialCoverage;
  temporal: TemporalCoverage;
}

/** Pricing tier for a listing */
export interface PricingTier {
  name: string;
  priceAmount: number; // In smallest unit (cents, satoshis)
  priceCurrency: string; // USD, ETH, SOL, SDN_CREDITS
  durationDays: number; // 0 = one-time, >0 = subscription
  rateLimit?: number; // Requests per hour
  maxRecordsPerRequest?: number;
  features?: string[];
  description?: string;
}

/** Provider reputation metrics */
export interface ProviderReputation {
  totalSales: number;
  averageRating: number; // 1-5 scale
  totalRatings: number;
  uptimePercentage: number; // 0-100
  avgDeliveryLatencyMs: number;
  disputeCount: number;
  providerSince: Date;
}

/** Storefront listing (STF) */
export interface Listing {
  listingId: string;
  providerPeerId: string;
  providerEpmCid?: string;
  title: string;
  description?: string;
  dataTypes: string[]; // OMM, CDM, TLE, etc.
  tags?: string[];
  coverage: DataCoverage;
  sampleCid?: string;
  sampleRecordCount?: number;
  accessType: AccessType;
  encryptionRequired: boolean;
  deliveryMethods: DeliveryMethod[];
  pricing: PricingTier[];
  acceptedPayments: PaymentMethod[];
  reputation?: ProviderReputation;
  createdAt: Date;
  updatedAt: Date;
  version: number;
  active: boolean;
  expiresAt?: Date;
  termsCid?: string;
  license?: string;
  signature?: Uint8Array;
}

/** Data access grant (ACL) */
export interface AccessGrant {
  grantId: string;
  listingId: string;
  tierName: string;
  buyerPeerId: string;
  buyerEncryptionPubkey?: Uint8Array;
  keyAlgorithm?: string;
  accessType: AccessType;
  rateLimit?: number;
  maxRecordsPerRequest?: number;
  grantedAt: Date;
  expiresAt?: Date;
  status: GrantStatus;
  paymentTxHash?: string;
  paymentMethod: PaymentMethod;
  paymentAmount: number;
  paymentCurrency: string;
  paymentChain?: string;
  nextRenewal?: Date;
  autoRenew: boolean;
  renewalCount: number;
  totalRequests: number;
  totalRecords: number;
  lastAccess?: Date;
  deliveryTopic?: string;
  providerSignature?: Uint8Array;
  providerPeerId: string;
}

/** Purchase request (PUR) */
export interface PurchaseRequest {
  requestId: string;
  listingId: string;
  tierName: string;
  buyerPeerId: string;
  buyerEncryptionPubkey?: Uint8Array;
  keyAlgorithm?: string;
  buyerEmail?: string;
  paymentMethod: PaymentMethod;
  paymentAmount: number;
  paymentCurrency: string;
  paymentTxHash?: string;
  paymentChain?: string;
  senderAddress?: string;
  confirmationBlock?: number;
  paymentIntentId?: string;
  creditsTransactionId?: string;
  status: PurchaseStatus;
  statusMessage?: string;
  createdAt: Date;
  updatedAt: Date;
  paymentDeadline?: Date;
  paymentConfirmedAt?: Date;
  grantIssuedAt?: Date;
  grantId?: string;
  providerPeerId?: string;
  providerAcknowledgedAt?: Date;
  preferredDeliveryMethod?: DeliveryMethod;
  webhookUrl?: string;
  buyerSignature?: Uint8Array;
  providerSignature?: Uint8Array;
}

/** Data quality metrics */
export interface DataQualityMetrics {
  schemaCompliance: number; // 0-100
  dataFreshness: number; // 0-100
  coverageAccuracy: number; // 0-100
  deliveryReliability: number; // 0-100
}

/** Review (REV) */
export interface Review {
  reviewId: string;
  listingId: string;
  reviewerPeerId: string;
  rating: number; // 1-5
  title?: string;
  content?: string;
  qualityMetrics?: DataQualityMetrics;
  aclGrantId?: string;
  verifiedPurchase: boolean;
  createdAt: Date;
  updatedAt: Date;
  status: ReviewStatus;
  helpfulCount: number;
  notHelpfulCount: number;
  providerResponse?: string;
  providerResponseAt?: Date;
  reviewerSignature?: Uint8Array;
}

/** Review statistics */
export interface ReviewStats {
  listingId: string;
  totalReviews: number;
  verifiedReviews: number;
  averageRating: number;
  ratingDistribution: [number, number, number, number, number]; // 1-5 star counts
  lastReviewAt?: Date;
  avgQualityMetrics?: DataQualityMetrics;
}

/** Delivery method */
export type DeliveryMethod = 'PubSubStream' | 'DirectTransfer' | 'IPFSPin' | 'WebhookPush';

/** Search query */
export interface SearchQuery {
  dataTypes?: string[];
  priceMax?: number;
  accessTypes?: AccessType[];
  spatialCoverage?: string[]; // Regions
  objectIds?: string[];
  providerPeerIds?: string[];
  searchText?: string;
  sortBy?: 'price' | 'rating' | 'updated' | 'relevance';
  sortDesc?: boolean;
  limit?: number;
  offset?: number;
}

/** Search facets */
export interface SearchFacets {
  dataTypes: Record<string, number>;
  priceRanges: Record<string, number>;
  providers: Record<string, number>;
  accessTypes: Record<string, number>;
}

/** Search result */
export interface SearchResult {
  listings: Listing[];
  total: number;
  facets: SearchFacets;
}

/** Credits balance */
export interface CreditsBalance {
  peerId: string;
  balance: number;
  pendingCredits: number;
  totalEarned: number;
  totalSpent: number;
  updatedAt: Date;
}

/** Create listing request */
export interface CreateListingRequest {
  title: string;
  description?: string;
  dataTypes: string[];
  tags?: string[];
  coverage: DataCoverage;
  sampleCid?: string;
  accessType: AccessType;
  encryptionRequired?: boolean;
  deliveryMethods: DeliveryMethod[];
  pricing: PricingTier[];
  acceptedPayments: PaymentMethod[];
  termsCid?: string;
  license?: string;
}

/** Create purchase request */
export interface CreatePurchaseRequest {
  listingId: string;
  tierName: string;
  paymentMethod: PaymentMethod;
  encryptionPubkey?: Uint8Array;
  keyAlgorithm?: string;
  preferredDeliveryMethod?: DeliveryMethod;
  webhookUrl?: string;
}

/** Create review request */
export interface CreateReviewRequest {
  listingId: string;
  rating: number;
  title?: string;
  content?: string;
  qualityMetrics?: DataQualityMetrics;
  aclGrantId?: string;
}

// --- 14.2 DHT Catalog types ---

/** DHT catalog entry */
export interface CatalogEntry {
  listingId: string;
  providerPeerId: string;
  title: string;
  dataTypes: string[];
  accessType: number;
  updatedAt: Date;
  active: boolean;
}

// --- 14.4 Payment types ---

/** Crypto payment request */
export interface CryptoPaymentRequest {
  requestId: string;
  txHash: string;
  chain: 'ethereum' | 'solana' | 'bitcoin';
  senderAddress?: string;
  amount: number;
  currency: string;
}

/** Crypto payment verification result */
export interface CryptoPaymentResult {
  verified: boolean;
  confirmationBlock?: number;
  error?: string;
}

/** Fiat gateway request */
export interface FiatGatewayRequest {
  requestId: string;
  amount: number;
  currency: string;
  buyerEmail?: string;
  description?: string;
  successUrl?: string;
  cancelUrl?: string;
}

/** Fiat gateway result */
export interface FiatGatewayResult {
  paymentIntentId: string;
  clientSecret: string;
  checkoutUrl: string;
}

/** Credits transaction */
export interface CreditsTransaction {
  transactionId: string;
  fromPeerId: string;
  toPeerId: string;
  amount: number;
  type: 'purchase' | 'refund' | 'deposit' | 'withdrawal';
  reference: string;
  createdAt: Date;
  status: string;
}

// --- 14.5 Delivery types ---

/** Delivery request */
export interface DeliveryRequest {
  grantId: string;
  listingId: string;
  buyerPeerId: string;
  method: DeliveryMethod;
  data: Uint8Array;
  encrypted: boolean;
  deliveryTopic?: string;
  webhookUrl?: string;
}

/** Delivery result */
export interface DeliveryResult {
  success: boolean;
  method: string;
  deliveredAt: number;
  bytesSent: number;
  cid?: string;
  topicId?: string;
  webhookStatus?: number;
  error?: string;
}

// --- 14.6 Dashboard types ---

/** Seller dashboard response */
export interface SellerDashboard {
  listings: Listing[];
  totalListings: number;
  activeGrants: number;
  totalEarnings: number;
  recentPurchases: PurchaseRequest[];
  trustScore?: TrustScore;
  creditsBalance: CreditsBalance;
}

/** Buyer dashboard response */
export interface BuyerDashboard {
  activeGrants: AccessGrant[];
  totalGrants: number;
  recentPurchases?: PurchaseRequest[];
  creditsBalance: CreditsBalance;
}

// --- 14.7 Trust types ---

/** Trust score for a provider */
export interface TrustScore {
  peerId: string;
  overallScore: number;
  reputationScore: number;
  uptimeScore: number;
  deliveryScore: number;
  dataQualityScore: number;
  disputeScore: number;
  tenureScore: number;
  volumeScore: number;
  escrowRequired: boolean;
  featured: boolean;
  computedAt: number;
}

/** Trust weight configuration */
export interface TrustWeights {
  reputation: number;
  uptime: number;
  delivery: number;
  dataQuality: number;
  disputes: number;
  tenure: number;
  volume: number;
}
