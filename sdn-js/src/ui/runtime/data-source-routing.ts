const CELESTRAK_PROVIDER_IDS = new Set([
  'space-data-network-02',
  'configured:space-data-network-02',
  'celestrak.eth',
  'configured:celestrak.eth',
]);

const CELESTRAK_SOURCE_PRIORITY_BY_STANDARD: Record<string, string[]> = {
  CAT: ['celestrak-satcat-csv', 'celestrak-satcat', 'celestrak-cat-historical'],
  MPE: ['celestrak-gp'],
  OMM: ['celestrak-gp'],
  PNM: ['celestrak-publication-log'],
  SPW: ['celestrak-space-weather'],
};

const CELESTRAK_SOURCE_PATTERNS: Array<{ pattern: RegExp; standards: string[] }> = [
  { pattern: /^celestrak-gp(?:$|-)/, standards: ['OMM', 'MPE'] },
  { pattern: /^celestrak-satcat(?:$|-)|^celestrak-cat(?:$|-)/, standards: ['CAT'] },
  { pattern: /^celestrak-space-weather(?:$|-)/, standards: ['SPW'] },
  { pattern: /^celestrak-publication-log(?:$|-)/, standards: ['PNM'] },
];

export function standardIdFromSchemaName(schemaName: string): string {
  return schemaName.split('.')[0]?.trim().toUpperCase() ?? '';
}

export function celestrakSourcePriorityForStandard(standardIdOrSchemaName: string, sourceName: string | null | undefined): number | null {
  const standardId = standardIdFromSchemaName(standardIdOrSchemaName);
  const normalizedSource = normalizeOptionalText(sourceName);
  if (!standardId || !normalizedSource) return null;

  const priorities = CELESTRAK_SOURCE_PRIORITY_BY_STANDARD[standardId] ?? [];
  const exactIndex = priorities.indexOf(normalizedSource);
  if (exactIndex >= 0) return exactIndex;

  const patternIndex = CELESTRAK_SOURCE_PATTERNS.findIndex((entry) => (
    entry.standards.includes(standardId) && entry.pattern.test(normalizedSource)
  ));
  return patternIndex >= 0 ? priorities.length + patternIndex : null;
}

export function sourceNameMatchesStandard(standardIdOrSchemaName: string, sourceName: string | null | undefined): boolean {
  return celestrakSourcePriorityForStandard(standardIdOrSchemaName, sourceName) !== null;
}

export function flatSqlSourceNameForSchema(input: {
  schemaName: string;
  providerId?: string | null;
  sourceName?: string | null;
}): string | null {
  const standardId = standardIdFromSchemaName(input.schemaName);
  const providerId = normalizeOptionalText(input.providerId);
  const requestedSourceName = normalizeOptionalText(input.sourceName);
  const celestrakProvider = isKnownCelestrakProviderId(providerId);

  if (requestedSourceName) {
    if (!celestrakProvider) return requestedSourceName;
    if (sourceNameMatchesStandard(standardId, requestedSourceName)) return requestedSourceName;
  }

  if (!celestrakProvider) return requestedSourceName ?? null;
  return CELESTRAK_SOURCE_PRIORITY_BY_STANDARD[standardId]?.[0] ?? null;
}

function isKnownCelestrakProviderId(providerId: string | null | undefined): boolean {
  const normalized = normalizeOptionalText(providerId);
  return Boolean(normalized && CELESTRAK_PROVIDER_IDS.has(normalized));
}

function normalizeOptionalText(value: string | null | undefined): string | null {
  const trimmed = value?.trim();
  return trimmed || null;
}
