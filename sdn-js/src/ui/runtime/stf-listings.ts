import * as flatbuffers from 'flatbuffers';
import { STF } from 'spacedatastandards.org/lib/js/REC/STF.js';
import { accessCategory } from 'spacedatastandards.org/lib/js/STF/accessCategory.js';
import { paymentMethod } from 'spacedatastandards.org/lib/js/STF/paymentMethod.js';

import type { CanonicalListing, ListingPaymentModel, ListingStatus } from './types';

export interface DecodeCanonicalStfListingOptions {
  observedAt?: number;
}

const textDecoder = new TextDecoder();

export function decodeCanonicalStfListing(
  bytes: Uint8Array,
  options: DecodeCanonicalStfListingOptions = {},
): CanonicalListing {
  const listing = STF.getRootAsSTF(new flatbuffers.ByteBuffer(bytes));
  const unpacked = listing.unpack();
  const dataTypes = normalizeStringList(unpacked.DATA_TYPES) ?? [];
  const pricing = normalizePricing(unpacked.PRICING);
  const acceptedPaymentMethods = normalizeAcceptedPayments(unpacked.ACCEPTED_PAYMENTS);
  const paymentModel = decodePaymentModel(unpacked.ACCESS_TYPE, pricing, acceptedPaymentMethods);

  return {
    listingKind: 'data',
    pluginId: normalizeRequiredString(unpacked.LISTING_ID, 'listingId'),
    version: `${updatedAtMillis(unpacked.UPDATED_AT) || updatedAtMillis(unpacked.CREATED_AT) || 0}`,
    name: normalizeRequiredString(unpacked.TITLE, 'title'),
    description: normalizeOptionalString(unpacked.DESCRIPTION),
    publisherPeerId: normalizeRequiredString(unpacked.PROVIDER_PEER_ID, 'providerPeerId'),
    observedAt: options.observedAt ?? updatedAtMillis(unpacked.UPDATED_AT) ?? updatedAtMillis(unpacked.CREATED_AT) ?? 0,
    status: decodeListingStatus(unpacked.ACTIVE),
    standardsUsed: dataTypes.length ? dataTypes : undefined,
    paymentModel,
    priceUsdCents: lowestUsdCents(pricing),
    subscriptionPeriodDays: firstSubscriptionDays(pricing),
    acceptedPaymentMethods,
    requiredScope: `data:${normalizeRequiredString(unpacked.LISTING_ID, 'listingId')}:query`,
    providerEpmCid: normalizeOptionalString(unpacked.PROVIDER_EPM_CID),
    sampleCid: normalizeOptionalString(unpacked.SAMPLE_CID),
    accessType: accessCategory[unpacked.ACCESS_TYPE],
    encryptionRequired: Boolean(unpacked.ENCRYPTION_REQUIRED),
  };
}

function decodeListingStatus(active: boolean): ListingStatus {
  return active ? 'public' : 'retired';
}

function decodePaymentModel(
  accessType: accessCategory,
  pricing: NormalizedPricingTier[],
  acceptedPaymentMethods?: string[],
): ListingPaymentModel {
  if (acceptedPaymentMethods?.includes(paymentMethod[paymentMethod.Free]) && pricing.length === 0) {
    return 'free';
  }
  if (accessType === accessCategory.Subscription || pricing.some((tier) => tier.durationDays > 0)) {
    return 'subscription';
  }
  return pricing.length > 0 ? 'one-time' : 'free';
}

interface NormalizedPricingTier {
  amount: number;
  currency?: string;
  durationDays: number;
}

function normalizePricing(pricing: Array<{
  PRICE_AMOUNT?: bigint;
  PRICE_CURRENCY?: string | Uint8Array | null;
  DURATION_DAYS?: number;
}> | null | undefined): NormalizedPricingTier[] {
  return pricing?.map((tier) => ({
    amount: Number(tier.PRICE_AMOUNT ?? 0n),
    currency: normalizeOptionalString(tier.PRICE_CURRENCY),
    durationDays: Number(tier.DURATION_DAYS ?? 0),
  })) ?? [];
}

function normalizeAcceptedPayments(payments: Array<paymentMethod | null> | null | undefined): string[] | undefined {
  const normalized = payments
    ?.map((method) => typeof method === 'number' ? paymentMethod[method] : undefined)
    .filter((method): method is string => Boolean(method));
  return normalized && normalized.length > 0 ? normalized : undefined;
}

function lowestUsdCents(pricing: NormalizedPricingTier[]): number | undefined {
  const usdPrices = pricing
    .filter((tier) => tier.amount > 0 && (tier.currency ?? '').toUpperCase() === 'USD')
    .map((tier) => tier.amount);
  return usdPrices.length > 0 ? Math.min(...usdPrices) : undefined;
}

function firstSubscriptionDays(pricing: NormalizedPricingTier[]): number | undefined {
  return pricing.find((tier) => tier.durationDays > 0)?.durationDays;
}

function updatedAtMillis(value: bigint | number | null | undefined): number | undefined {
  const timestamp = Number(value ?? 0);
  return timestamp > 0 ? timestamp * 1000 : undefined;
}

function normalizeRequiredString(
  value: string | Uint8Array | null | undefined,
  fieldName: string,
): string {
  const normalized = normalizeOptionalString(value);
  if (!normalized) {
    throw new Error(`${fieldName} is required`);
  }
  return normalized;
}

function normalizeOptionalString(
  value: string | Uint8Array | null | undefined,
): string | undefined {
  const normalized = (
    typeof value === 'string'
      ? value
      : value instanceof Uint8Array
        ? textDecoder.decode(value)
        : ''
  ).trim();
  return normalized ? normalized : undefined;
}

function normalizeStringList(
  values: (string | Uint8Array | null | undefined)[] | null | undefined,
): string[] | undefined {
  const normalized = values
    ?.map((value) => normalizeOptionalString(value))
    .filter((value): value is string => Boolean(value));

  return normalized && normalized.length > 0 ? normalized : undefined;
}
