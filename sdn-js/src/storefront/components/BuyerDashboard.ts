/**
 * Buyer Dashboard component utilities
 *
 * Provides rendering utilities for the buyer experience including:
 * - Active subscriptions/grants
 * - Purchase history
 * - Credits balance management
 * - Data access status
 */

import type { BuyerDashboard, AccessGrant } from '../types';
import { formatPrice } from './ListingCard';

/** Buyer dashboard render props */
export interface BuyerDashboardProps {
  dashboard: BuyerDashboard;
  onViewGrant?: (grant: AccessGrant) => void;
  onBrowse?: () => void;
  onPurchaseCredits?: () => void;
}

/** Format grant status for display */
export function formatGrantStatus(status: number): { label: string; color: string } {
  switch (status) {
    case 0: return { label: 'Active', color: '#00ff88' };
    case 1: return { label: 'Revoked', color: '#cc0000' };
    case 2: return { label: 'Expired', color: '#808080' };
    case 3: return { label: 'Suspended', color: '#cc6600' };
    case 4: return { label: 'Pending', color: '#ccaa00' };
    default: return { label: 'Unknown', color: '#808080' };
  }
}

/** Format delivery method for display */
export function formatDeliveryMethod(method: string): string {
  switch (method) {
    case 'PubSubStream': return 'Real-time Stream';
    case 'DirectTransfer': return 'Direct Transfer';
    case 'IPFSPin': return 'IPFS Pin';
    case 'WebhookPush': return 'Webhook';
    default: return method;
  }
}

/** Render buyer dashboard as HTML */
export function renderBuyerDashboardHTML(props: BuyerDashboardProps): string {
  const { dashboard } = props;

  return `
    <div class="buyer-dashboard">
      <div class="dashboard-header">
        <h2>My Data Access</h2>
        <button class="btn-primary browse-listings">Browse Marketplace</button>
      </div>

      <div class="dashboard-stats">
        <div class="stat-card">
          <div class="stat-value">${dashboard.totalGrants}</div>
          <div class="stat-label">Active Subscriptions</div>
        </div>
        <div class="stat-card">
          <div class="stat-value">${dashboard.creditsBalance?.balance ?? 0}</div>
          <div class="stat-label">Credits Balance</div>
          <button class="btn-sm purchase-credits">Purchase Credits</button>
        </div>
      </div>

      <div class="grants-section">
        <h3>Active Data Access</h3>
        ${(dashboard.activeGrants ?? []).map(grant => {
          const status = formatGrantStatus(grant.status);
          return `
            <div class="grant-card" data-grant-id="${grant.grantId}">
              <div class="grant-header">
                <span class="grant-tier">${grant.tierName}</span>
                <span class="grant-status" style="color: ${status.color}">${status.label}</span>
              </div>
              <div class="grant-details">
                <div class="grant-detail">
                  <span class="detail-label">Listing</span>
                  <span class="detail-value">${grant.listingId.slice(0, 12)}...</span>
                </div>
                <div class="grant-detail">
                  <span class="detail-label">Provider</span>
                  <span class="detail-value">${grant.providerPeerId.slice(0, 12)}...</span>
                </div>
                ${grant.deliveryTopic ? `
                <div class="grant-detail">
                  <span class="detail-label">Delivery</span>
                  <span class="detail-value">Streaming</span>
                </div>
                ` : ''}
                <div class="grant-detail">
                  <span class="detail-label">Usage</span>
                  <span class="detail-value">${grant.totalRequests} requests / ${grant.totalRecords} records</span>
                </div>
                ${grant.expiresAt ? `
                <div class="grant-detail">
                  <span class="detail-label">Expires</span>
                  <span class="detail-value">${new Date(grant.expiresAt).toLocaleDateString()}</span>
                </div>
                ` : ''}
                ${grant.autoRenew ? '<div class="auto-renew-badge">Auto-Renew</div>' : ''}
              </div>
            </div>
          `;
        }).join('')}
      </div>
    </div>
  `;
}

/** CSS styles for buyer dashboard */
export const buyerDashboardStyles = `
.buyer-dashboard {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  color: #e0e0e0;
  background: #0a0a1a;
  padding: 24px;
}

.grant-card {
  background: #1a1a2e;
  border: 1px solid #2a2a3a;
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 12px;
}

.grant-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.grant-tier {
  font-weight: 600;
  font-size: 1.1rem;
}

.grant-details {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}

.grant-detail {
  display: flex;
  flex-direction: column;
}

.detail-label {
  font-size: 0.75rem;
  color: #808080;
}

.detail-value {
  font-size: 0.9rem;
}

.auto-renew-badge {
  display: inline-block;
  background: #2a4a6a;
  color: #8ac0ff;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 0.75rem;
  margin-top: 4px;
}

.btn-sm {
  padding: 4px 12px;
  border-radius: 4px;
  border: 1px solid #3a3a5a;
  background: transparent;
  color: #a0a0c0;
  cursor: pointer;
  font-size: 0.75rem;
  margin-top: 8px;
}
`;
