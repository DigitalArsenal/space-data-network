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
  results?: Array<{
    data_base64?: string;
    timestamp?: string;
  }>;
}

interface StorefrontListingResponse {
  listings?: StorefrontListing[];
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

  const storefrontResponse = await fetchImpl(
    `${normalizedBaseUrl}/api/storefront/listings`,
    { credentials: 'include' },
  );

  if (storefrontResponse.ok) {
    const payload = await storefrontResponse.json() as StorefrontListingResponse;
    const listings = (payload.listings ?? [])
      .map((listing) => decodeStorefrontListing(listing))
      .filter((listing): listing is CanonicalListing => Boolean(listing));

    if (listings.length > 0) {
      return listings;
    }
  }

  if (storefrontResponse.status !== 404) {
    throw new Error(`storefront listing query failed (${storefrontResponse.status})`);
  }

  const plgResponse = await fetchImpl(
    `${normalizedBaseUrl}/api/v1/data/query/PLG?include_data=true&format=json&limit=25`,
    { credentials: 'include' },
  );

  if (!plgResponse.ok) {
    throw new Error(`listing query failed (${plgResponse.status})`);
  }

  const payload = await plgResponse.json() as PlgQueryResponse;
  return (payload.results ?? [])
    .map((entry) => entry.data_base64 ? decodeCanonicalPlgListing(base64ToBytes(entry.data_base64), {
      observedAt: entry.timestamp ? Date.parse(entry.timestamp) : Date.now(),
    }) : null)
    .filter((listing): listing is CanonicalListing => Boolean(listing));
}

function decodeStorefrontListing(listing: StorefrontListing): CanonicalListing | null {
  const pluginId = listing.listing_id?.trim();
  if (!pluginId) {
    return null;
  }

  const name = listing.title?.trim();
  const description = listing.description?.trim();

  return {
    pluginId,
    version: normalizeStorefrontVersion(listing.version),
    name: name || pluginId,
    description: description || undefined,
    publisherPeerId: listing.provider_peer_id?.trim() || undefined,
    observedAt: parseObservedAt(listing.updated_at, listing.created_at),
    status: decodeStorefrontStatus(listing.active),
    tags: normalizeTags(listing.tags),
  };
}

function normalizeStorefrontVersion(value: number | string | undefined): string {
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

function normalizeTags(tags: string[] | undefined): string[] | undefined {
  const normalized = tags
    ?.map((tag) => tag.trim())
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
