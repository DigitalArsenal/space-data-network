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
});
