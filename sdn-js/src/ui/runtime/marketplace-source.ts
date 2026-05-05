import { decodeCanonicalPlgListing, inferStandardsUsed } from './plg-listings';
import { decodeCanonicalStfListing } from './stf-listings';
import type { CanonicalListing, CanonicalProtectedDelivery, ListingPaymentModel, ListingStatus } from './types';

export interface MarketplaceFetchLikeResponse {
  ok: boolean;
  status: number;
  json(): Promise<unknown>;
}

export type MarketplaceFetchLike = (
  input: string,
  init?: RequestInit,
) => Promise<MarketplaceFetchLikeResponse>;

interface PlgQueryResponse {
  results?: unknown;
}

interface ModuleDeliveryListingResponse {
  results?: unknown;
}

interface StorefrontListingResponse {
  listings?: unknown;
}

interface StorefrontListing {
  listing_id?: string;
  listing_kind?: string;
  provider_peer_id?: string;
  title?: string;
  description?: string;
  data_types?: unknown;
  tags?: string[];
  updated_at?: string;
  created_at?: string;
  version?: number | string;
  active?: boolean;
  sample_cid?: string;
  access_type?: unknown;
  encryption_required?: boolean;
  pricing?: unknown;
  accepted_payments?: unknown;
  protected_delivery?: unknown;
}

export async function loadMarketplaceListingsFromServer(
  baseUrl: string,
  fetchImpl: MarketplaceFetchLike = fetch,
): Promise<CanonicalListing[]> {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '');
  const listings: CanonicalListing[] = [];
  const moduleDeliveryResponse = await fetchImpl(
    `${normalizedBaseUrl}/api/module-delivery/listings`,
    { credentials: 'include' },
  );

  if (moduleDeliveryResponse.ok) {
    const payload = asRecord(await moduleDeliveryResponse.json()) as ModuleDeliveryListingResponse | null;
    listings.push(...normalizeFlatbufferQueryResults(payload?.results)
      .map((entry) => entry.data_base64 ? decodeCanonicalPlgListing(base64ToBytes(entry.data_base64), {
        observedAt: entry.timestamp ? Date.parse(entry.timestamp) : Date.now(),
      }) : null)
      .filter((listing): listing is CanonicalListing => Boolean(listing)));
    return [
      ...listings,
      ...await loadStfListings(normalizedBaseUrl, fetchImpl),
    ];
  }

  let storefrontError: Error | null = null;

  const storefrontResponse = await fetchImpl(
    `${normalizedBaseUrl}/api/storefront/listings`,
    { credentials: 'include' },
  );

  if (storefrontResponse.ok) {
    const payload = asRecord(await storefrontResponse.json()) as StorefrontListingResponse | null;
    listings.push(...normalizeStorefrontListings(payload?.listings)
      .map((listing) => decodeStorefrontListing(listing))
      .filter((listing): listing is CanonicalListing => Boolean(listing)));
    return [
      ...listings,
      ...await loadStfListings(normalizedBaseUrl, fetchImpl),
    ];
  } else if (storefrontResponse.status !== 404) {
    storefrontError = new Error(`storefront listing query failed (${storefrontResponse.status})`);
  }

  const plgResponse = await fetchImpl(
    `${normalizedBaseUrl}/api/v1/data/query/PLG?include_data=true&format=json&limit=25`,
    { credentials: 'include' },
  );

  if (!plgResponse.ok) {
    if (plgResponse.status === 404) {
      if (storefrontResponse.ok || storefrontResponse.status === 404) {
        return [];
      }
      throw storefrontError ?? new Error(`listing query failed (${plgResponse.status})`);
    }
    throw new Error(`listing query failed (${plgResponse.status})`);
  }

  const payload = asRecord(await plgResponse.json()) as PlgQueryResponse | null;
  listings.push(...normalizeFlatbufferQueryResults(payload?.results)
    .map((entry) => entry.data_base64 ? decodeCanonicalPlgListing(base64ToBytes(entry.data_base64), {
      observedAt: entry.timestamp ? Date.parse(entry.timestamp) : Date.now(),
    }) : null)
    .filter((listing): listing is CanonicalListing => Boolean(listing)));

  return [
    ...listings,
    ...await loadStfListings(normalizedBaseUrl, fetchImpl),
  ];
}

function decodeStorefrontListing(listing: unknown): CanonicalListing | null {
  if (!isRecord(listing)) {
    return null;
  }

  const pluginId = pickTrimmedString(listing, 'listing_id');
  if (!pluginId) {
    return null;
  }

  const name = pickTrimmedString(listing, 'title');
  const description = pickTrimmedString(listing, 'description');
  const dataTypes = normalizeTags(listing.data_types) ?? [];
  const tags = normalizeTags(listing.tags);
  const protectedDelivery = decodeProtectedDelivery(listing.protected_delivery);
  const primaryTier = normalizePricingTiers(listing.pricing)[0];
  const paymentModel = primaryTier ? decodeStorefrontPaymentModel(primaryTier) : undefined;

  const decoded: CanonicalListing = {
    listingKind: listing.listing_kind === 'wasm_module' ? 'module' : 'data',
    pluginId,
    version: normalizeStorefrontVersion(listing.version),
    name: name || pluginId,
    description: description || undefined,
    publisherPeerId: pickTrimmedString(listing, 'provider_peer_id') || undefined,
    observedAt: parseObservedAt(
      pickTrimmedString(listing, 'updated_at'),
      pickTrimmedString(listing, 'created_at'),
    ),
    status: decodeStorefrontStatus(typeof listing.active === 'boolean' ? listing.active : undefined),
    tags,
    paymentModel,
    priceUsdCents: primaryTier?.priceCurrency === 'USD' ? primaryTier.priceAmount : undefined,
    subscriptionPeriodDays: paymentModel === 'subscription' ? primaryTier?.durationDays : undefined,
    acceptedPaymentMethods: normalizePaymentMethods(listing.accepted_payments),
    requiredScope: protectedDelivery?.grantScope,
    standardsUsed: uniqueSorted([
      ...dataTypes,
      ...(inferStandardsUsed(pluginId, name, description, tags) ?? []),
    ]),
    sampleCid: pickTrimmedString(listing, 'sample_cid') || undefined,
    accessType: decodeStorefrontAccessType(listing.access_type),
    encryptionRequired: typeof listing.encryption_required === 'boolean' ? listing.encryption_required : undefined,
    protectedDelivery,
  };
  return pruneUndefined(decoded);
}

interface StorefrontPricingTier {
  priceAmount: number;
  priceCurrency: string;
  durationDays: number;
}

function normalizePricingTiers(value: unknown): StorefrontPricingTier[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .filter(isRecord)
    .map((tier) => ({
      priceAmount: numberField(tier, 'price_amount') ?? numberField(tier, 'priceAmount') ?? 0,
      priceCurrency: pickTrimmedString(tier, 'price_currency') || pickTrimmedString(tier, 'priceCurrency') || '',
      durationDays: numberField(tier, 'duration_days') ?? numberField(tier, 'durationDays') ?? 0,
    }));
}

function decodeStorefrontPaymentModel(tier: StorefrontPricingTier | undefined): ListingPaymentModel {
  if (!tier || tier.priceAmount <= 0) {
    return 'free';
  }
  return tier.durationDays > 0 ? 'subscription' : 'one-time';
}

function normalizePaymentMethods(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) {
    return undefined;
  }
  const methods = value
    .map((method) => {
      if (typeof method === 'string' && method.trim()) {
        return method.trim();
      }
      if (typeof method === 'number') {
        return paymentMethodLabel(method);
      }
      return '';
    })
    .filter(Boolean);
  return methods.length > 0 ? methods : undefined;
}

function paymentMethodLabel(method: number): string {
  switch (method) {
    case 0:
      return 'Crypto_ETH';
    case 1:
      return 'Crypto_SOL';
    case 2:
      return 'Crypto_BTC';
    case 3:
      return 'SDN_Credits';
    case 4:
      return 'Fiat_Stripe';
    case 5:
      return 'Free';
    default:
      return `Payment_${method}`;
  }
}

function decodeStorefrontAccessType(value: unknown): string | undefined {
  switch (value) {
    case 0:
      return 'one-time';
    case 1:
      return 'subscription';
    case 2:
      return 'streaming';
    case 3:
      return 'query';
    default:
      return typeof value === 'string' && value.trim() ? value.trim() : undefined;
  }
}

function decodeProtectedDelivery(value: unknown): CanonicalProtectedDelivery | undefined {
  if (!isRecord(value)) {
    return undefined;
  }
  const protectedDelivery: CanonicalProtectedDelivery = {
    encryptedCid: pickTrimmedString(value, 'encrypted_cid') || pickTrimmedString(value, 'encryptedCid'),
    manifestCid: pickTrimmedString(value, 'manifest_cid') || pickTrimmedString(value, 'manifestCid'),
    contentHash: pickTrimmedString(value, 'content_hash') || pickTrimmedString(value, 'contentHash'),
    contentKeyId: pickTrimmedString(value, 'content_key_id') || pickTrimmedString(value, 'contentKeyId'),
    licenseModuleId: pickTrimmedString(value, 'license_module_id') || pickTrimmedString(value, 'licenseModuleId'),
    moduleId: pickTrimmedString(value, 'module_id') || pickTrimmedString(value, 'moduleId'),
    moduleVersion: pickTrimmedString(value, 'module_version') || pickTrimmedString(value, 'moduleVersion'),
    requiredScopes: normalizeTags(value.required_scopes) ?? normalizeTags(value.requiredScopes),
    grantScope: pickTrimmedString(value, 'grant_scope') || pickTrimmedString(value, 'grantScope'),
    deliveryProtocol: pickTrimmedString(value, 'delivery_protocol') || pickTrimmedString(value, 'deliveryProtocol'),
  };
  return Object.values(protectedDelivery).some((entry) => Array.isArray(entry) ? entry.length > 0 : Boolean(entry))
    ? protectedDelivery
    : undefined;
}

async function loadStfListings(
  normalizedBaseUrl: string,
  fetchImpl: MarketplaceFetchLike,
): Promise<CanonicalListing[]> {
  const response = await fetchImpl(
    `${normalizedBaseUrl}/api/v1/data/query/STF?include_data=true&format=json&limit=25`,
    { credentials: 'include' },
  );

  if (!response.ok) {
    if (response.status === 404) {
      return [];
    }
    throw new Error(`STF listing query failed (${response.status})`);
  }

  const payload = asRecord(await response.json()) as PlgQueryResponse | null;
  return normalizeFlatbufferQueryResults(payload?.results)
    .map((entry) => entry.data_base64 ? decodeCanonicalStfListing(base64ToBytes(entry.data_base64), {
      observedAt: entry.timestamp ? Date.parse(entry.timestamp) : Date.now(),
    }) : null)
    .filter((listing): listing is CanonicalListing => Boolean(listing));
}

function normalizeStorefrontVersion(value: unknown): string {
  if (typeof value === 'string' && value.trim()) {
    return value.trim();
  }
  if (typeof value === 'number' && Number.isFinite(value)) {
    return String(value);
  }
  return 'storefront';
}

function decodeStorefrontStatus(active: boolean | undefined): ListingStatus {
  return active === false ? 'retired' : 'public';
}

function normalizeTags(tags: unknown): string[] | undefined {
  if (!Array.isArray(tags)) {
    return undefined;
  }
  const normalized = tags
    .filter((tag): tag is string => typeof tag === 'string')
    .map((tag) => tag.trim())
    .filter((tag) => Boolean(tag));
  return normalized && normalized.length > 0 ? normalized : undefined;
}

function numberField(payload: Record<string, unknown>, key: string): number | undefined {
  const value = payload[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

function uniqueSorted(values: Array<string | undefined>): string[] | undefined {
  const normalized = [...new Set(values
    .filter((value): value is string => typeof value === 'string')
    .map((value) => value.trim())
    .filter(Boolean))]
    .sort((left, right) => left.localeCompare(right));
  return normalized.length > 0 ? normalized : undefined;
}

function parseObservedAt(updatedAt?: string, createdAt?: string): number {
  const timestamp = updatedAt || createdAt;
  if (!timestamp) {
    return Date.now();
  }
  const parsed = Date.parse(timestamp);
  return Number.isFinite(parsed) ? parsed : Date.now();
}

function base64ToBytes(value: string): Uint8Array {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/');
  const padded = normalized.padEnd(normalized.length + ((4 - (normalized.length % 4)) % 4), '=');
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
}

function normalizeStorefrontListings(value: unknown): StorefrontListing[] {
  return Array.isArray(value) ? value.filter((entry): entry is StorefrontListing => isRecord(entry)) : [];
}

function normalizeFlatbufferQueryResults(value: unknown): Array<{
  data_base64?: string;
  timestamp?: string;
}> {
  return Array.isArray(value) ? value.filter((entry): entry is {
    data_base64?: string;
    timestamp?: string;
  } => isRecord(entry)) : [];
}

function pickTrimmedString(
  payload: Record<string, unknown>,
  key: string,
): string | undefined {
  const value = payload[key];
  return typeof value === 'string' && value.trim() ? value.trim() : undefined;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return isRecord(value) ? value : null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object';
}

function pruneUndefined<T extends object>(value: T): T {
  for (const key of Object.keys(value)) {
    const record = value as Record<string, unknown>;
    if (record[key] === undefined) {
      delete record[key];
    }
  }
  return value;
}
