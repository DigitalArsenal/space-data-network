/**
 * Tests for the Storefront client
 */

import { describe, it, expect } from 'vitest';
import {
  AccessType,
  PaymentMethod,
  GrantStatus,
  PurchaseStatus,
  ReviewStatus,
} from './types';
import {
  formatPrice,
  formatAccessType,
  formatDuration,
  formatPaymentMethod,
  formatRating,
  renderListingCardHTML,
} from './components';
import type { Listing, PricingTier } from './types';

describe('Storefront Types', () => {
  it('should have correct enum values', () => {
    expect(AccessType.OneTime).toBe(0);
    expect(AccessType.Subscription).toBe(1);
    expect(AccessType.Streaming).toBe(2);
    expect(AccessType.Query).toBe(3);

    expect(PaymentMethod.CryptoETH).toBe(0);
    expect(PaymentMethod.SDNCredits).toBe(4);
    expect(PaymentMethod.Free).toBe(6);

    expect(GrantStatus.Active).toBe(0);
    expect(GrantStatus.Revoked).toBe(1);

    expect(PurchaseStatus.Pending).toBe(0);
    expect(PurchaseStatus.Completed).toBe(3);

    expect(ReviewStatus.Published).toBe(0);
  });
});

describe('Storefront Formatters', () => {
  describe('formatPrice', () => {
    it('should format USD prices', () => {
      const tier: PricingTier = {
        name: 'Basic',
        priceAmount: 4999,
        priceCurrency: 'USD',
        durationDays: 30,
      };
      expect(formatPrice(tier)).toBe('$49.99');
    });

    it('should format ETH prices', () => {
      const tier: PricingTier = {
        name: 'Pro',
        priceAmount: 100000000000000000, // 0.1 ETH
        priceCurrency: 'ETH',
        durationDays: 30,
      };
      expect(formatPrice(tier)).toBe('0.1000 ETH');
    });

    it('should format SDN credits', () => {
      const tier: PricingTier = {
        name: 'Basic',
        priceAmount: 500,
        priceCurrency: 'SDN_CREDITS',
        durationDays: 30,
      };
      expect(formatPrice(tier)).toBe('500 credits');
    });
  });

  describe('formatAccessType', () => {
    it('should format access types', () => {
      expect(formatAccessType(AccessType.OneTime)).toBe('One-time Purchase');
      expect(formatAccessType(AccessType.Subscription)).toBe('Subscription');
      expect(formatAccessType(AccessType.Streaming)).toBe('Real-time Streaming');
      expect(formatAccessType(AccessType.Query)).toBe('Query Access');
    });
  });

  describe('formatDuration', () => {
    it('should format durations', () => {
      expect(formatDuration(0)).toBe('One-time');
      expect(formatDuration(1)).toBe('1 day');
      expect(formatDuration(7)).toBe('1 week');
      expect(formatDuration(30)).toBe('1 month');
      expect(formatDuration(365)).toBe('1 year');
      expect(formatDuration(14)).toBe('14 days');
    });
  });

  describe('formatPaymentMethod', () => {
    it('should format payment methods', () => {
      expect(formatPaymentMethod(PaymentMethod.CryptoETH)).toBe('ETH');
      expect(formatPaymentMethod(PaymentMethod.SDNCredits)).toBe('Credits');
      expect(formatPaymentMethod(PaymentMethod.Free)).toBe('Free');
    });
  });

  describe('formatRating', () => {
    it('should calculate star display', () => {
      expect(formatRating(4.5)).toEqual({ full: 4, half: true, empty: 0 });
      expect(formatRating(3.2)).toEqual({ full: 3, half: false, empty: 2 });
      expect(formatRating(5)).toEqual({ full: 5, half: false, empty: 0 });
      expect(formatRating(1)).toEqual({ full: 1, half: false, empty: 4 });
    });
  });
});

describe('Listing Card', () => {
  it('should render listing card HTML', () => {
    const listing: Listing = {
      listingId: 'test-123',
      providerPeerId: '12D3KooWTestPeer',
      title: 'LEO Conjunction Data',
      description: 'Real-time conjunction data',
      dataTypes: ['CDM', 'TCA'],
      coverage: {
        spatial: {
          type: 'region',
          regions: ['LEO'],
        },
        temporal: {
          updateFrequency: 'realtime',
        },
      },
      accessType: AccessType.Subscription,
      encryptionRequired: true,
      deliveryMethods: ['PubSubStream'],
      pricing: [
        {
          name: 'Basic',
          priceAmount: 4900,
          priceCurrency: 'USD',
          durationDays: 30,
        },
      ],
      acceptedPayments: [PaymentMethod.CryptoETH, PaymentMethod.SDNCredits],
      createdAt: new Date(),
      updatedAt: new Date(),
      version: 1,
      active: true,
      reputation: {
        totalSales: 150,
        averageRating: 4.2,
        totalRatings: 45,
        uptimePercentage: 99.5,
        avgDeliveryLatencyMs: 120,
        disputeCount: 0,
        providerSince: new Date('2024-01-01'),
      },
    };

    const html = renderListingCardHTML(listing);

    expect(html).toContain('LEO Conjunction Data');
    expect(html).toContain('Subscription');
    expect(html).toContain('CDM');
    expect(html).toContain('TCA');
    expect(html).toContain('$49.00');
    expect(html).toContain('ETH');
    expect(html).toContain('Credits');
  });
});

describe('Storefront Client Configuration', () => {
  it('should export StorefrontClient', async () => {
    const { StorefrontClient } = await import('./client');
    expect(StorefrontClient).toBeDefined();
  });

  it('should export createStorefrontClient', async () => {
    const { createStorefrontClient } = await import('./client');
    expect(createStorefrontClient).toBeDefined();
  });

  it('should create client with config', async () => {
    const { createStorefrontClient } = await import('./client');
    const client = createStorefrontClient({
      apiBaseUrl: 'http://localhost:5001/api',
      peerId: 'test-peer-id',
    });
    expect(client).toBeDefined();
  });
});

// --- Phase 14.2: Discovery types ---
describe('Catalog Entry types', () => {
  it('should define CatalogEntry interface', () => {
    const entry: import('./types').CatalogEntry = {
      listingId: 'listing-1',
      providerPeerId: 'provider-1',
      title: 'Test Data',
      dataTypes: ['CDM'],
      accessType: AccessType.Subscription,
      updatedAt: new Date(),
      active: true,
    };
    expect(entry.listingId).toBe('listing-1');
    expect(entry.active).toBe(true);
  });
});

// --- Phase 14.4: Payment types ---
describe('Payment types', () => {
  it('should define CryptoPaymentRequest', () => {
    const req: import('./types').CryptoPaymentRequest = {
      requestId: 'purchase-1',
      txHash: '0xabc123',
      chain: 'ethereum',
      amount: 4900,
      currency: 'ETH',
    };
    expect(req.chain).toBe('ethereum');
  });

  it('should define FiatGatewayResult', () => {
    const result: import('./types').FiatGatewayResult = {
      paymentIntentId: 'pi_123',
      clientSecret: 'secret_123',
      checkoutUrl: 'https://checkout.stripe.com/pay/pi_123',
    };
    expect(result.paymentIntentId).toBe('pi_123');
  });

  it('should define CreditsTransaction', () => {
    const tx: import('./types').CreditsTransaction = {
      transactionId: 'tx-1',
      fromPeerId: 'buyer-1',
      toPeerId: 'provider-1',
      amount: 500,
      type: 'purchase',
      reference: 'purchase-1',
      createdAt: new Date(),
      status: 'completed',
    };
    expect(tx.type).toBe('purchase');
    expect(tx.amount).toBe(500);
  });
});

// --- Phase 14.5: Delivery types ---
describe('Delivery types', () => {
  it('should define DeliveryResult', () => {
    const result: import('./types').DeliveryResult = {
      success: true,
      method: 'PubSubStream',
      deliveredAt: Date.now(),
      bytesSent: 1024,
      topicId: '/sdn/data/listing-1/buyer-1',
    };
    expect(result.success).toBe(true);
    expect(result.method).toBe('PubSubStream');
  });
});

// --- Phase 14.6: Dashboard components ---
describe('Seller Dashboard', () => {
  it('should export formatEarnings', async () => {
    const { formatEarnings } = await import('./components/SellerDashboard');
    expect(formatEarnings(4900)).toBe('$49.00');
    expect(formatEarnings(500, 'SDN_CREDITS')).toBe('500 credits');
  });

  it('should export formatTrustScore', async () => {
    const { formatTrustScore } = await import('./components/SellerDashboard');

    const excellent = formatTrustScore({
      peerId: 'test',
      overallScore: 85,
      reputationScore: 90,
      uptimeScore: 99,
      deliveryScore: 80,
      dataQualityScore: 85,
      disputeScore: 95,
      tenureScore: 60,
      volumeScore: 70,
      escrowRequired: false,
      featured: true,
      computedAt: Date.now(),
    });
    expect(excellent.label).toBe('Excellent');
    expect(excellent.tier).toBe('Trusted');
    expect(excellent.percentage).toBe(85);

    const low = formatTrustScore({
      peerId: 'new',
      overallScore: 15,
      reputationScore: 0,
      uptimeScore: 0,
      deliveryScore: 0,
      dataQualityScore: 0,
      disputeScore: 50,
      tenureScore: 0,
      volumeScore: 0,
      escrowRequired: true,
      featured: false,
      computedAt: Date.now(),
    });
    expect(low.label).toBe('Unrated');
    expect(low.tier).toBe('Unverified');
  });

  it('should render seller dashboard HTML', async () => {
    const { renderSellerDashboardHTML } = await import('./components/SellerDashboard');
    const html = renderSellerDashboardHTML({
      dashboard: {
        listings: [{
          listingId: 'l1',
          providerPeerId: 'provider-1',
          title: 'LEO CDM Data',
          dataTypes: ['CDM'],
          coverage: { spatial: { type: 'region' }, temporal: { updateFrequency: 'realtime' } },
          accessType: AccessType.Subscription,
          encryptionRequired: true,
          deliveryMethods: ['PubSubStream'],
          pricing: [{ name: 'Basic', priceAmount: 4900, priceCurrency: 'USD', durationDays: 30 }],
          acceptedPayments: [PaymentMethod.SDNCredits],
          createdAt: new Date(),
          updatedAt: new Date(),
          version: 1,
          active: true,
        }],
        totalListings: 1,
        activeGrants: 5,
        totalEarnings: 50000,
        recentPurchases: [],
        trustScore: {
          peerId: 'provider-1',
          overallScore: 75,
          reputationScore: 80,
          uptimeScore: 95,
          deliveryScore: 70,
          dataQualityScore: 75,
          disputeScore: 90,
          tenureScore: 40,
          volumeScore: 60,
          escrowRequired: false,
          featured: false,
          computedAt: Date.now(),
        },
        creditsBalance: {
          peerId: 'provider-1',
          balance: 5000,
          pendingCredits: 0,
          totalEarned: 50000,
          totalSpent: 0,
          updatedAt: new Date(),
        },
      },
    });

    expect(html).toContain('Seller Dashboard');
    expect(html).toContain('LEO CDM Data');
    expect(html).toContain('$500.00'); // totalEarnings
    expect(html).toContain('5000'); // credits balance
    expect(html).toContain('75%'); // trust score
  });
});

describe('Buyer Dashboard', () => {
  it('should export formatGrantStatus', async () => {
    const { formatGrantStatus } = await import('./components/BuyerDashboard');
    expect(formatGrantStatus(0)).toEqual({ label: 'Active', color: '#00ff88' });
    expect(formatGrantStatus(1)).toEqual({ label: 'Revoked', color: '#cc0000' });
    expect(formatGrantStatus(2)).toEqual({ label: 'Expired', color: '#808080' });
    expect(formatGrantStatus(3)).toEqual({ label: 'Suspended', color: '#cc6600' });
    expect(formatGrantStatus(4)).toEqual({ label: 'Pending', color: '#ccaa00' });
  });

  it('should export formatDeliveryMethod', async () => {
    const { formatDeliveryMethod } = await import('./components/BuyerDashboard');
    expect(formatDeliveryMethod('PubSubStream')).toBe('Real-time Stream');
    expect(formatDeliveryMethod('DirectTransfer')).toBe('Direct Transfer');
    expect(formatDeliveryMethod('IPFSPin')).toBe('IPFS Pin');
    expect(formatDeliveryMethod('WebhookPush')).toBe('Webhook');
  });

  it('should render buyer dashboard HTML', async () => {
    const { renderBuyerDashboardHTML } = await import('./components/BuyerDashboard');
    const html = renderBuyerDashboardHTML({
      dashboard: {
        activeGrants: [{
          grantId: 'g1',
          listingId: 'listing-123456789',
          tierName: 'Pro',
          buyerPeerId: 'buyer-123456789',
          accessType: AccessType.Streaming,
          grantedAt: new Date(),
          status: 0, // Active
          paymentMethod: PaymentMethod.SDNCredits,
          paymentAmount: 19900,
          paymentCurrency: 'USD',
          autoRenew: true,
          renewalCount: 3,
          totalRequests: 150,
          totalRecords: 5000,
          providerPeerId: 'provider-123456789',
          deliveryTopic: '/sdn/data/listing-1/buyer-1',
        }],
        totalGrants: 1,
        creditsBalance: {
          peerId: 'buyer-1',
          balance: 2500,
          pendingCredits: 0,
          totalEarned: 0,
          totalSpent: 7500,
          updatedAt: new Date(),
        },
      },
    });

    expect(html).toContain('My Data Access');
    expect(html).toContain('Pro');
    expect(html).toContain('Active');
    expect(html).toContain('150 requests / 5000 records');
    expect(html).toContain('Auto-Renew');
    expect(html).toContain('2500'); // credits balance
  });
});

// --- Phase 14.7: Trust Score types ---
describe('Trust Score types', () => {
  it('should define TrustScore', () => {
    const score: import('./types').TrustScore = {
      peerId: 'provider-1',
      overallScore: 82.5,
      reputationScore: 90,
      uptimeScore: 99.9,
      deliveryScore: 85,
      dataQualityScore: 80,
      disputeScore: 95,
      tenureScore: 50,
      volumeScore: 70,
      escrowRequired: false,
      featured: true,
      computedAt: Date.now(),
    };
    expect(score.overallScore).toBe(82.5);
    expect(score.featured).toBe(true);
    expect(score.escrowRequired).toBe(false);
  });

  it('should define TrustWeights', () => {
    const weights: import('./types').TrustWeights = {
      reputation: 0.30,
      uptime: 0.15,
      delivery: 0.15,
      dataQuality: 0.15,
      disputes: 0.10,
      tenure: 0.05,
      volume: 0.10,
    };
    const total = weights.reputation + weights.uptime + weights.delivery +
      weights.dataQuality + weights.disputes + weights.tenure + weights.volume;
    expect(total).toBe(1.0);
  });
});
