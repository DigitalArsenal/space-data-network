/**
 * Storefront/Marketplace module for Space Data Network
 *
 * This module provides the client-side implementation for the SDN data marketplace,
 * enabling users to:
 *
 * - Browse and search data listings from providers
 * - Purchase data access using crypto, SDN credits, or fiat
 * - Manage subscriptions and access grants
 * - Review and rate data providers
 *
 * @example
 * ```typescript
 * import { createStorefrontClient, AccessType, PaymentMethod } from '@sdn/storefront';
 *
 * const client = createStorefrontClient({
 *   apiBaseUrl: 'https://api.example.com',
 *   peerId: 'my-peer-id',
 * });
 *
 * // Search for orbital data
 * const results = await client.searchListings({
 *   dataTypes: ['OMM', 'OEM'],
 *   accessTypes: [AccessType.Subscription],
 * });
 *
 * // Purchase access
 * const purchase = await client.createPurchase({
 *   listingId: results.listings[0].listingId,
 *   tierName: 'Pro',
 *   paymentMethod: PaymentMethod.SDNCredits,
 * });
 * ```
 */

// Types
export {
  AccessType,
  PaymentMethod,
  GrantStatus,
  PurchaseStatus,
  ReviewStatus,
} from './types';

export type {
  SpatialCoverage,
  TemporalCoverage,
  DataCoverage,
  ListingKind,
  ProtectedDelivery,
  PricingTier,
  ProviderReputation,
  Listing,
  AccessGrant,
  PurchaseRequest,
  DataQualityMetrics,
  Review,
  ReviewStats,
  DeliveryMethod,
  SearchQuery,
  SearchFacets,
  SearchResult,
  CreditsBalance,
  CreateListingRequest,
  CreatePurchaseRequest,
  CreateReviewRequest,
  CatalogEntry,
  CryptoPaymentRequest,
  CryptoPaymentResult,
  CreateCryptoIntentRequest,
  CryptoBuyerIntent,
  SubmitCryptoPaymentRequest,
  FiatGatewayRequest,
  FiatGatewayResult,
  CreditsTransaction,
  ManualDevPaymentConfirmation,
  ManualDevPaymentResult,
  PaymentAuditEvent,
  DeliveryRequest,
  DeliveryResult,
  SellerDashboard,
  BuyerDashboard,
  TrustScore,
  TrustWeights,
} from './types';

// Client
export {
  StorefrontClient,
  createStorefrontClient,
  createStorefrontLibp2pPubSubAdapter,
} from './client';

export type {
  DeliveryTopicMessage,
  DeliveryTopicSubscription,
  StorefrontClientConfig,
  StorefrontEvents,
  StorefrontLibp2pPubSub,
  StorefrontPubSub,
} from './client';

// UI Components and utilities
export {
  formatPrice,
  formatAccessType,
  formatDuration,
  formatPaymentMethod,
  formatRating,
  renderListingCardHTML,
  listingCardStyles,
  formatEarnings,
  formatTrustScore,
  renderSellerDashboardHTML,
  sellerDashboardStyles,
  formatGrantStatus,
  formatDeliveryMethod,
  renderBuyerDashboardHTML,
  buyerDashboardStyles,
} from './components';

export type {
  ListingCardProps,
  SellerDashboardProps,
  BuyerDashboardProps,
} from './components';
