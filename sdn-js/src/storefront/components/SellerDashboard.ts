/**
 * Seller Dashboard component utilities
 *
 * Provides rendering utilities for the seller dashboard including:
 * - Listing management summary
 * - Sales analytics
 * - Active grants overview
 * - Earnings display
 * - Trust score visualization
 */

import type { SellerDashboard, TrustScore, Listing } from '../types';
import { formatAccessType } from './ListingCard';

/** Seller dashboard render props */
export interface SellerDashboardProps {
  dashboard: SellerDashboard;
  onCreateListing?: () => void;
  onViewListing?: (listing: Listing) => void;
  onWithdraw?: () => void;
}

/** Format earnings for display */
export function formatEarnings(amount: number, currency = 'USD'): string {
  if (currency === 'USD') {
    return `$${(amount / 100).toFixed(2)}`;
  }
  if (currency === 'SDN_CREDITS') {
    return `${amount} credits`;
  }
  return `${amount} ${currency}`;
}

/** Format a trust score for display */
export function formatTrustScore(score: TrustScore): {
  label: string;
  percentage: number;
  color: string;
  tier: string;
} {
  const percentage = Math.round(score.overallScore);
  let label: string;
  let color: string;
  let tier: string;

  if (percentage >= 80) {
    label = 'Excellent';
    color = '#00ff88';
    tier = 'Trusted';
  } else if (percentage >= 60) {
    label = 'Good';
    color = '#88cc00';
    tier = 'Standard';
  } else if (percentage >= 40) {
    label = 'Fair';
    color = '#ccaa00';
    tier = 'Developing';
  } else if (percentage >= 20) {
    label = 'Low';
    color = '#cc6600';
    tier = 'New';
  } else {
    label = 'Unrated';
    color = '#cc0000';
    tier = 'Unverified';
  }

  return { label, percentage, color, tier };
}

/** Render seller dashboard as HTML */
export function renderSellerDashboardHTML(props: SellerDashboardProps): string {
  const { dashboard } = props;
  const trustDisplay = dashboard.trustScore
    ? formatTrustScore(dashboard.trustScore)
    : null;

  return `
    <div class="seller-dashboard">
      <div class="dashboard-header">
        <h2>Seller Dashboard</h2>
        <button class="btn-primary create-listing">Create Listing</button>
      </div>

      <div class="dashboard-stats">
        <div class="stat-card">
          <div class="stat-value">${dashboard.totalListings}</div>
          <div class="stat-label">Active Listings</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${dashboard.activeGrants}</div>
          <div class="stat-label">Active Grants</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${formatEarnings(dashboard.totalEarnings)}</div>
          <div class="stat-label">Total Earnings</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${dashboard.creditsBalance?.balance ?? 0}</div>
          <div class="stat-label">Credits Balance</div>
        </div>
      </div>

      ${trustDisplay ? `
      <div class="trust-score-section">
        <h3>Trust Score</h3>
        <div class="trust-score-bar">
          <div class="trust-score-fill" style="width: ${trustDisplay.percentage}%; background: ${trustDisplay.color}"></div>
        </div>
        <div class="trust-score-label">
          <span class="trust-tier">${trustDisplay.tier}</span>
          <span class="trust-percentage">${trustDisplay.percentage}%</span>
          <span class="trust-label">${trustDisplay.label}</span>
          ${dashboard.trustScore?.featured ? '<span class="featured-badge">Featured</span>' : ''}
          ${dashboard.trustScore?.escrowRequired ? '<span class="escrow-badge">Escrow Required</span>' : ''}
        </div>
      </div>
      ` : ''}

      <div class="listings-section">
        <h3>Your Listings</h3>
        ${dashboard.listings.map(listing => `
          <div class="listing-row" data-listing-id="${listing.listingId}">
            <span class="listing-row-title">${escapeHtml(listing.title)}</span>
            <span class="listing-row-type">${formatAccessType(listing.accessType)}</span>
            <span class="listing-row-data">${listing.dataTypes.join(', ')}</span>
            <span class="listing-row-status ${listing.active ? 'active' : 'inactive'}">
              ${listing.active ? 'Active' : 'Inactive'}
            </span>
          </div>
        `).join('')}
      </div>

      <div class="recent-purchases-section">
        <h3>Recent Purchases</h3>
        ${(dashboard.recentPurchases ?? []).map(purchase => `
          <div class="purchase-row">
            <span class="purchase-buyer">${purchase.buyerPeerId?.slice(0, 12) ?? 'Unknown'}...</span>
            <span class="purchase-tier">${purchase.tierName}</span>
            <span class="purchase-amount">${formatEarnings(purchase.paymentAmount)}</span>
            <span class="purchase-status status-${purchase.status}">${purchaseStatusLabel(purchase.status)}</span>
          </div>
        `).join('')}
      </div>
    </div>
  `;
}

function purchaseStatusLabel(status: number): string {
  const labels = ['Pending', 'Payment Detected', 'Confirmed', 'Completed', 'Failed', 'Cancelled', 'Refund Requested', 'Refunded', 'Expired'];
  return labels[status] ?? 'Unknown';
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

/** CSS styles for seller dashboard */
export const sellerDashboardStyles = `
.seller-dashboard {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  color: #e0e0e0;
  background: #0a0a1a;
  padding: 24px;
}

.dashboard-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 24px;
}

.dashboard-stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 16px;
  margin-bottom: 24px;
}

.stat-card {
  background: #1a1a2e;
  border: 1px solid #2a2a3a;
  border-radius: 8px;
  padding: 16px;
  text-align: center;
}

.stat-value {
  font-size: 1.5rem;
  font-weight: 700;
  color: #00ff88;
}

.stat-label {
  font-size: 0.85rem;
  color: #808080;
  margin-top: 4px;
}

.trust-score-section {
  background: #1a1a2e;
  border: 1px solid #2a2a3a;
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 24px;
}

.trust-score-bar {
  height: 8px;
  background: #2a2a3a;
  border-radius: 4px;
  overflow: hidden;
  margin: 8px 0;
}

.trust-score-fill {
  height: 100%;
  border-radius: 4px;
  transition: width 0.3s ease;
}

.trust-score-label {
  display: flex;
  gap: 12px;
  align-items: center;
  font-size: 0.85rem;
}

.trust-tier { font-weight: 600; }
.trust-percentage { color: #a0a0a0; }
.featured-badge {
  background: #ffd700;
  color: #000;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 0.75rem;
  font-weight: 600;
}
.escrow-badge {
  background: #cc6600;
  color: #fff;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 0.75rem;
}

.listing-row, .purchase-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px;
  border-bottom: 1px solid #2a2a3a;
}

.listing-row-status.active { color: #00ff88; }
.listing-row-status.inactive { color: #cc0000; }
`;
