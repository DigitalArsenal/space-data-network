import { SCHEMA_DESCRIPTIONS, type SchemaName } from '../../schemas';

import type { CanonicalListing } from './types';

export interface StoreAuthorResult {
  kind: 'author';
  key: string;
  name: string;
  handle?: string;
  peerId?: string;
  moduleCount: number;
  pluginIds: string[];
  standardsUsed: string[];
}

export interface StorePluginResult {
  kind: 'plugin';
  key: string;
  listing: CanonicalListing;
  publisherLabel: string;
  standardsUsed: string[];
}

export interface StoreDataResult {
  kind: 'data';
  key: string;
  standard: string;
  description: string;
  moduleCount: number;
  pluginIds: string[];
  publisherNames: string[];
}

export interface StoreSearchResults {
  authors: StoreAuthorResult[];
  plugins: StorePluginResult[];
  data: StoreDataResult[];
}

export function searchStoreListings(
  listings: CanonicalListing[],
  query = '',
): StoreSearchResults {
  const normalizedQuery = normalizeSearch(query);
  const authorMap = new Map<string, StoreAuthorResult>();
  const dataMap = new Map<string, StoreDataResult>();

  const plugins = listings
    .map((listing) => {
      const publisherLabel = listing.publisherName ?? listing.publisherHandle ?? listing.publisherPeerId ?? 'Unknown publisher';
      const standardsUsed = uniqueSortedStrings(listing.standardsUsed ?? []);

      const authorKey = normalizeKey(listing.publisherName ?? listing.publisherHandle ?? listing.publisherPeerId ?? 'unknown');
      const existingAuthor = authorMap.get(authorKey);
      if (existingAuthor) {
        existingAuthor.moduleCount += 1;
        existingAuthor.pluginIds.push(listing.pluginId);
        existingAuthor.standardsUsed = uniqueSortedStrings([
          ...existingAuthor.standardsUsed,
          ...standardsUsed,
        ]);
      } else {
        authorMap.set(authorKey, {
          kind: 'author',
          key: authorKey,
          name: listing.publisherName ?? listing.publisherHandle ?? listing.publisherPeerId ?? 'Unknown publisher',
          handle: listing.publisherHandle,
          peerId: listing.publisherPeerId,
          moduleCount: 1,
          pluginIds: [listing.pluginId],
          standardsUsed,
        });
      }

      for (const standard of standardsUsed) {
        const existingData = dataMap.get(standard);
        const publisherName = listing.publisherName ?? listing.publisherHandle ?? listing.publisherPeerId ?? 'Unknown publisher';
        if (existingData) {
          existingData.moduleCount += 1;
          existingData.pluginIds = uniqueSortedStrings([...existingData.pluginIds, listing.pluginId]);
          existingData.publisherNames = uniqueSortedStrings([...existingData.publisherNames, publisherName]);
        } else {
          dataMap.set(standard, {
            kind: 'data',
            key: standard,
            standard,
            description: describeStandard(standard),
            moduleCount: 1,
            pluginIds: [listing.pluginId],
            publisherNames: [publisherName],
          });
        }
      }

      return {
        kind: 'plugin' as const,
        key: `${listing.pluginId}@${listing.version}`,
        listing,
        publisherLabel,
        standardsUsed,
      };
    })
    .filter((result) => matchesStoreQuery(result, normalizedQuery))
    .sort((left, right) => {
      const observedDiff = (right.listing.observedAt ?? 0) - (left.listing.observedAt ?? 0);
      if (observedDiff !== 0) {
        return observedDiff;
      }
      return left.key.localeCompare(right.key);
    });

  const authors = [...authorMap.values()]
    .filter((result) => matchesStoreQuery(result, normalizedQuery))
    .sort((left, right) => left.name.localeCompare(right.name));

  const data = [...dataMap.values()]
    .filter((result) => matchesStoreQuery(result, normalizedQuery))
    .sort((left, right) => left.standard.localeCompare(right.standard));

  return { authors, plugins, data };
}

function matchesStoreQuery(
  result: StoreAuthorResult | StorePluginResult | StoreDataResult,
  normalizedQuery: string,
): boolean {
  if (!normalizedQuery) {
    return true;
  }

  switch (result.kind) {
    case 'author':
      return normalizeSearch([
        result.name,
        result.handle,
        result.peerId,
        result.pluginIds.join(' '),
        result.standardsUsed.join(' '),
      ].filter(Boolean).join(' ')).includes(normalizedQuery);
    case 'plugin':
      return normalizeSearch([
        result.listing.pluginId,
        result.listing.version,
        result.listing.name,
        result.listing.description,
        result.listing.tagline,
        result.publisherLabel,
        result.listing.publisherHandle,
        result.listing.publisherPeerId,
        (result.listing.tags ?? []).join(' '),
        result.standardsUsed.join(' '),
      ].filter(Boolean).join(' ')).includes(normalizedQuery);
    case 'data':
      return normalizeSearch([
        result.standard,
        result.description,
        result.pluginIds.join(' '),
        result.publisherNames.join(' '),
      ].join(' ')).includes(normalizedQuery);
  }
}

function describeStandard(standard: string): string {
  const schemaKey = `${standard}.fbs` as SchemaName;
  return SCHEMA_DESCRIPTIONS[schemaKey] ?? `${standard} linked SDS data`;
}

function normalizeSearch(value: string): string {
  return value.trim().toLowerCase();
}

function normalizeKey(value: string): string {
  return normalizeSearch(value) || 'unknown';
}

function uniqueSortedStrings(values: string[]): string[] {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))].sort((left, right) => left.localeCompare(right));
}
