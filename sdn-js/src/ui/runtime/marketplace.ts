import type { CanonicalListing } from './types';

function normalizeListingValue(value: string, fieldName: string): string {
  const normalized = value.trim();
  if (!normalized) {
    throw new Error(`${fieldName} is required`);
  }
  return normalized;
}

export function canonicalListingKey(
  input: Pick<CanonicalListing, 'pluginId' | 'version'>,
): string {
  const pluginId = normalizeListingValue(input.pluginId, 'pluginId');
  const version = normalizeListingValue(input.version, 'version');
  return `${pluginId}@${version}`;
}

export class MarketplaceIndex {
  readonly #listings = new Map<string, CanonicalListing>();

  constructor(listings: Iterable<CanonicalListing> = []) {
    for (const listing of listings) {
      this.record(listing);
    }
  }

  record(listing: CanonicalListing): CanonicalListing {
    const normalizedListing: CanonicalListing = {
      ...listing,
      pluginId: normalizeListingValue(listing.pluginId, 'pluginId'),
      version: normalizeListingValue(listing.version, 'version'),
      observedAt: listing.observedAt ?? 0,
    };
    const key = canonicalListingKey(normalizedListing);
    const existing = this.#listings.get(key);

    if (!existing || (normalizedListing.observedAt ?? 0) >= (existing.observedAt ?? 0)) {
      this.#listings.set(key, normalizedListing);
      return normalizedListing;
    }

    return existing;
  }

  get(pluginId: string, version: string): CanonicalListing | undefined {
    return this.#listings.get(canonicalListingKey({ pluginId, version }));
  }

  count(): number {
    return this.#listings.size;
  }

  values(): CanonicalListing[] {
    return [...this.#listings.values()].sort((left, right) => {
      const observedDiff = (right.observedAt ?? 0) - (left.observedAt ?? 0);
      if (observedDiff !== 0) {
        return observedDiff;
      }
      return canonicalListingKey(left).localeCompare(canonicalListingKey(right));
    });
  }
}

export function createMarketplaceIndex(
  listings: Iterable<CanonicalListing> = [],
): MarketplaceIndex {
  return new MarketplaceIndex(listings);
}
