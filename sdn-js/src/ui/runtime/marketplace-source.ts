import { decodeCanonicalPlgListing } from './plg-listings';
import type { CanonicalListing, ListingStatus } from './types';

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

interface StorefrontListingResponse {
  listings?: unknown;
}

interface StorefrontListing {
  listing_id?: string;
  provider_peer_id?: string;
  title?: string;
  description?: string;
  tags?: string[];
  updated_at?: string;
  created_at?: string;
  version?: number | string;
  active?: boolean;
}

export async function loadMarketplaceListingsFromServer(
  baseUrl: string,
  fetchImpl: MarketplaceFetchLike = fetch,
): Promise<CanonicalListing[]> {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '');
  let storefrontError: Error | null = null;

  const storefrontResponse = await fetchImpl(
    `${normalizedBaseUrl}/api/storefront/listings`,
    { credentials: 'include' },
  );

  if (storefrontResponse.ok) {
    const payload = asRecord(await storefrontResponse.json()) as StorefrontListingResponse | null;
    return normalizeStorefrontListings(payload?.listings)
      .map((listing) => decodeStorefrontListing(listing))
      .filter((listing): listing is CanonicalListing => Boolean(listing));
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
  return normalizePlgQueryResults(payload?.results)
    .map((entry) => entry.data_base64 ? decodeCanonicalPlgListing(base64ToBytes(entry.data_base64), {
      observedAt: entry.timestamp ? Date.parse(entry.timestamp) : Date.now(),
    }) : null)
    .filter((listing): listing is CanonicalListing => Boolean(listing));
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

  return {
    pluginId,
    version: normalizeStorefrontVersion(listing.version),
    name: name || pluginId,
    description: description || undefined,
    publisherPeerId: pickTrimmedString(listing, 'provider_peer_id') || undefined,
    observedAt: parseObservedAt(
      pickTrimmedString(listing, 'updated_at'),
      pickTrimmedString(listing, 'created_at'),
    ),
    status: decodeStorefrontStatus(listing.active),
    tags: normalizeTags(listing.tags),
  };
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

function normalizePlgQueryResults(value: unknown): Array<{
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
