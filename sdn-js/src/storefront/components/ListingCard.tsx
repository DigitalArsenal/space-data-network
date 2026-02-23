/**
 * ListingCard component for displaying storefront listings
 *
 * This is a framework-agnostic component definition that can be
 * adapted to React, Vue, Svelte, or vanilla JS implementations.
 *
 * @example React implementation:
 * ```tsx
 * import { ListingCardProps } from '@spacedatanetwork/sdn-js/storefront/components';
 *
 * export function ListingCard({ listing, onViewSample, onSubscribe }: ListingCardProps) {
 *   return (
 *     <div className="listing-card">
 *       <h3>{listing.title}</h3>
 *       <p>{listing.description}</p>
 *       // ...
 *     </div>
 *   );
 * }
 * ```
 */

import type { Listing, PricingTier, PaymentMethod, AccessType } from '../types';

/** Props for the ListingCard component */
export interface ListingCardProps {
  /** The listing to display */
  listing: Listing;
  /** Callback when user clicks "View Sample" */
  onViewSample?: (listing: Listing) => void;
  /** Callback when user clicks "Subscribe" or "Purchase" */
  onSubscribe?: (listing: Listing, tier: PricingTier) => void;
  /** Callback when user clicks on the listing for details */
  onViewDetails?: (listing: Listing) => void;
  /** Whether to show the full description or truncated */
  expanded?: boolean;
  /** Custom CSS class name */
  className?: string;
}

/** Format a price for display */
export function formatPrice(tier: PricingTier): string {
  const amount = tier.priceAmount;
  const currency = tier.priceCurrency;

  switch (currency) {
    case 'USD':
      return `$${(amount / 100).toFixed(2)}`;
    case 'ETH':
      return `${(amount / 1e18).toFixed(4)} ETH`;
    case 'SOL':
      return `${(amount / 1e9).toFixed(4)} SOL`;
    case 'SDN_CREDITS':
      return `${amount} credits`;
    default:
      return `${amount} ${currency}`;
  }
}

/** Format access type for display */
export function formatAccessType(accessType: AccessType): string {
  switch (accessType) {
    case 0: // OneTime
      return 'One-time Purchase';
    case 1: // Subscription
      return 'Subscription';
    case 2: // Streaming
      return 'Real-time Streaming';
    case 3: // Query
      return 'Query Access';
    default:
      return 'Unknown';
  }
}

/** Format duration for display */
export function formatDuration(days: number): string {
  if (days === 0) return 'One-time';
  if (days === 1) return '1 day';
  if (days === 7) return '1 week';
  if (days === 30 || days === 31) return '1 month';
  if (days === 365 || days === 366) return '1 year';
  return `${days} days`;
}

/** Get payment method icon/label */
export function formatPaymentMethod(method: PaymentMethod): string {
  switch (method) {
    case 0: // CryptoETH
      return 'ETH';
    case 1: // CryptoSOL
      return 'SOL';
    case 2: // CryptoBTC
      return 'BTC';
    case 3: // CryptoUSDC
      return 'USDC';
    case 4: // SDNCredits
      return 'Credits';
    case 5: // FiatStripe
      return 'Card';
    case 6: // Free
      return 'Free';
    default:
      return 'Unknown';
  }
}

/** Calculate star rating display */
export function formatRating(rating: number): { full: number; half: boolean; empty: number } {
  const full = Math.floor(rating);
  const half = rating - full >= 0.5;
  const empty = 5 - full - (half ? 1 : 0);
  return { full, half, empty };
}

/**
 * Default listing card template (HTML string for reference)
 *
 * This provides a reference implementation that can be adapted to any framework.
 */
export function renderListingCardHTML(listing: Listing): string {
  const lowestPrice = listing.pricing.reduce(
    (min, tier) => (tier.priceAmount < min.priceAmount ? tier : min),
    listing.pricing[0]
  );

  const rating = listing.reputation?.averageRating ?? 0;
  const ratingDisplay = formatRating(rating);
  const stars = '★'.repeat(ratingDisplay.full) +
    (ratingDisplay.half ? '½' : '') +
    '☆'.repeat(ratingDisplay.empty);

  return `
    <div class="listing-card" data-listing-id="${listing.listingId}">
      <div class="listing-card-header">
        <h3 class="listing-title">${escapeHtml(listing.title)}</h3>
        <span class="listing-access-type">${formatAccessType(listing.accessType)}</span>
      </div>

      <div class="listing-provider">
        Provider: ${escapeHtml(listing.providerPeerId.slice(0, 12))}...
        <span class="listing-rating">${stars} (${listing.reputation?.totalRatings ?? 0})</span>
      </div>

      <div class="listing-data-types">
        ${listing.dataTypes.map((dt) => `<span class="data-type-tag">${escapeHtml(dt)}</span>`).join('')}
      </div>

      <div class="listing-coverage">
        <div class="coverage-spatial">
          ${listing.coverage.spatial.type === 'global' ? 'Global Coverage' : ''}
          ${listing.coverage.spatial.regions?.join(', ') ?? ''}
        </div>
        <div class="coverage-temporal">
          Update: ${listing.coverage.temporal.updateFrequency}
        </div>
      </div>

      <div class="listing-pricing">
        <span class="price-from">From</span>
        <span class="price-amount">${formatPrice(lowestPrice)}</span>
        <span class="price-duration">/ ${formatDuration(lowestPrice.durationDays)}</span>
      </div>

      <div class="listing-actions">
        ${listing.sampleCid ? '<button class="btn-secondary view-sample">View Sample</button>' : ''}
        <button class="btn-primary subscribe">
          ${listing.accessType === 1 ? 'Subscribe' : 'Purchase'}
        </button>
      </div>

      <div class="listing-payment-methods">
        ${listing.acceptedPayments.map((pm) => `<span class="payment-method">${formatPaymentMethod(pm)}</span>`).join('')}
      </div>
    </div>
  `;
}

/** Escape HTML entities */
function escapeHtml(text: string): string {
  const div = typeof document !== 'undefined' ? document.createElement('div') : null;
  if (div) {
    div.textContent = text;
    return div.innerHTML;
  }
  // Fallback for Node.js
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

/**
 * Default CSS styles for the listing card
 */
export const listingCardStyles = `
.listing-card {
  border: 1px solid #2a2a3a;
  border-radius: 8px;
  padding: 16px;
  background: #1a1a2e;
  color: #e0e0e0;
  max-width: 400px;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
}

.listing-card-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 8px;
}

.listing-title {
  font-size: 1.1rem;
  font-weight: 600;
  margin: 0;
  color: #ffffff;
}

.listing-access-type {
  font-size: 0.75rem;
  padding: 2px 8px;
  border-radius: 4px;
  background: #3a3a5a;
  color: #a0a0c0;
}

.listing-provider {
  font-size: 0.85rem;
  color: #a0a0a0;
  margin-bottom: 12px;
}

.listing-rating {
  color: #ffd700;
  margin-left: 8px;
}

.listing-data-types {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-bottom: 12px;
}

.data-type-tag {
  font-size: 0.75rem;
  padding: 2px 8px;
  border-radius: 4px;
  background: #2a4a6a;
  color: #8ac0ff;
}

.listing-coverage {
  font-size: 0.85rem;
  color: #b0b0b0;
  margin-bottom: 12px;
  padding: 8px;
  background: #0a0a1a;
  border-radius: 4px;
}

.listing-pricing {
  font-size: 1rem;
  margin-bottom: 12px;
}

.price-from {
  color: #808080;
  font-size: 0.85rem;
}

.price-amount {
  font-weight: 700;
  color: #00ff88;
  margin: 0 4px;
}

.price-duration {
  color: #808080;
  font-size: 0.85rem;
}

.listing-actions {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}

.listing-actions button {
  flex: 1;
  padding: 8px 16px;
  border-radius: 4px;
  border: none;
  cursor: pointer;
  font-weight: 500;
  transition: background 0.2s;
}

.btn-primary {
  background: #0066cc;
  color: white;
}

.btn-primary:hover {
  background: #0077ee;
}

.btn-secondary {
  background: #3a3a5a;
  color: #e0e0e0;
}

.btn-secondary:hover {
  background: #4a4a6a;
}

.listing-payment-methods {
  display: flex;
  gap: 8px;
  justify-content: center;
}

.payment-method {
  font-size: 0.7rem;
  padding: 2px 6px;
  border-radius: 3px;
  background: #2a2a3a;
  color: #909090;
}
`;
