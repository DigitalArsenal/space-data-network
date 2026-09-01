import legacyCountrySchema from 'spacedatastandards.org/lib/fbjson/LCC/main.fb.schema.json';

export interface SemanticCountryGroupRow {
  country: string;
  aliases: string[];
  groups: string[];
  code?: string;
  value?: number;
}

export interface SemanticDatasetSummary {
  id: string;
  version: number;
  fields: string[];
  rowCount: number;
}

export const OPERATOR_COUNTRY_GROUPS_DATASET_ID = 'operator_country_groups_v1';
export const LEGACY_OWNER_COUNTRIES_DATASET_ID = 'legacy_owner_countries_v1';

export const OPERATOR_COUNTRY_GROUPS_V1: SemanticCountryGroupRow[] = [
  row('Russia', ['Russian Federation', 'USSR', 'Soviet Union', 'RU'], ['former_soviet_bloc', 'former_ussr']),
  row('Ukraine', ['Ukrainian SSR', 'UA'], ['former_soviet_bloc', 'former_ussr']),
  row('Kazakhstan', ['Kazakh SSR', 'Republic of Kazakhstan', 'KZ'], ['former_soviet_bloc', 'former_ussr']),
  row('Belarus', ['Byelorussian SSR', 'Republic of Belarus', 'BY'], ['former_soviet_bloc', 'former_ussr']),
  row('Armenia', ['Armenian SSR', 'AM'], ['former_soviet_bloc', 'former_ussr']),
  row('Azerbaijan', ['Azerbaijan SSR', 'AZ'], ['former_soviet_bloc', 'former_ussr']),
  row('Estonia', ['Estonian SSR', 'EE'], ['former_soviet_bloc', 'former_ussr', 'baltic']),
  row('Georgia', ['Georgian SSR', 'GE'], ['former_soviet_bloc', 'former_ussr']),
  row('Kyrgyzstan', ['Kirghiz SSR', 'Kyrgyz Republic', 'KG'], ['former_soviet_bloc', 'former_ussr']),
  row('Latvia', ['Latvian SSR', 'LV'], ['former_soviet_bloc', 'former_ussr', 'baltic']),
  row('Lithuania', ['Lithuanian SSR', 'LT'], ['former_soviet_bloc', 'former_ussr', 'baltic']),
  row('Moldova', ['Moldavian SSR', 'Republic of Moldova', 'MD'], ['former_soviet_bloc', 'former_ussr']),
  row('Tajikistan', ['Tajik SSR', 'TJ'], ['former_soviet_bloc', 'former_ussr']),
  row('Turkmenistan', ['Turkmen SSR', 'TM'], ['former_soviet_bloc', 'former_ussr']),
  row('Uzbekistan', ['Uzbek SSR', 'UZ'], ['former_soviet_bloc', 'former_ussr']),
  row('United States', ['USA', 'United States of America', 'US'], ['control_non_member']),
  row('France', ['French Republic', 'FR'], ['control_non_member']),
  row('Japan', ['JP'], ['control_non_member']),
];

interface LegacyOwnerMetadata {
  value: number;
  description: string;
}

interface CountryOverlay {
  country?: string;
  aliases?: string[];
}

const LEGACY_OWNER_METADATA = (legacyCountrySchema as unknown as {
  definitions: {
    legacyCountryCode: {
      'x-flatbuffer-enum-values': Record<string, LegacyOwnerMetadata>;
    };
  };
}).definitions.legacyCountryCode['x-flatbuffer-enum-values'];

// The SDS descriptions are the canonical display values. This small overlay
// adds the common country names people actually type without duplicating enum
// ordinals in application code. CIS is the legacy catalogue owner for Russia.
const COUNTRY_OVERLAYS: Record<string, CountryOverlay> = {
  ARM: { aliases: ['Armenia', 'Armenian SSR'] },
  AZER: { aliases: ['Azerbaijan SSR'] },
  BELA: { aliases: ['Byelorussian SSR', 'Republic of Belarus'] },
  BGD: { aliases: ['Bangladesh'] },
  BHUT: { aliases: ['Bhutan'] },
  CIS: {
    country: 'Russia',
    aliases: ['Russian', 'Russian Federation', 'USSR', 'Soviet Union', 'Commonwealth of Independent States'],
  },
  CRI: { aliases: ['Costa Rica'] },
  CZCH: { aliases: ['Czech Republic', 'Czechia', 'Czechoslovakia'] },
  DJI: { aliases: ['Djibouti'] },
  EST: { aliases: ['Estonian SSR'] },
  FR: { aliases: ['French Republic'] },
  GHA: { aliases: ['Ghana'] },
  KEN: { aliases: ['Kenya'] },
  KAZ: { aliases: ['Kazakh SSR', 'Republic of Kazakhstan'] },
  LKA: { aliases: ['Sri Lanka'] },
  LTU: { aliases: ['Lithuanian SSR'] },
  MCO: { aliases: ['Monaco'] },
  MDA: { aliases: ['Moldova', 'Moldavian SSR'] },
  MMR: { aliases: ['Myanmar', 'Burma'] },
  NKOR: { country: 'North Korea', aliases: ['Democratic People\'s Republic of Korea', 'DPRK'] },
  NPL: { aliases: ['Nepal'] },
  PRC: { country: 'China', aliases: ['People\'s Republic of China'] },
  PRY: { aliases: ['Paraguay'] },
  QAT: { aliases: ['Qatar'] },
  ROC: { country: 'Taiwan', aliases: ['Republic of China'] },
  RP: { country: 'Philippines', aliases: ['Republic of the Philippines'] },
  RWA: { aliases: ['Rwanda'] },
  SDN: { aliases: ['Sudan'] },
  SKOR: { country: 'South Korea', aliases: ['Republic of Korea'] },
  TUN: { aliases: ['Tunisia'] },
  UKR: { aliases: ['Ukrainian SSR'] },
  US: { aliases: ['USA', 'United States of America'] },
  VAT: { aliases: ['Vatican City'] },
  ZWE: { aliases: ['Zimbabwe'] },
};

export const LEGACY_OWNER_COUNTRIES_V1: SemanticCountryGroupRow[] = Object.entries(
  LEGACY_OWNER_METADATA,
)
  .sort(([, left], [, right]) => left.value - right.value)
  .map(([code, metadata]) => {
    const overlay = COUNTRY_OVERLAYS[code];
    return {
      country: overlay?.country ?? metadata.description,
      aliases: uniqueStrings([...(overlay?.aliases ?? []), code]),
      groups: [],
      code,
      value: metadata.value,
    };
  });

export function semanticDatasetSummaries(): SemanticDatasetSummary[] {
  return [
    {
      id: OPERATOR_COUNTRY_GROUPS_DATASET_ID,
      version: 1,
      fields: ['country', 'aliases', 'groups'],
      rowCount: OPERATOR_COUNTRY_GROUPS_V1.length,
    },
    {
      id: LEGACY_OWNER_COUNTRIES_DATASET_ID,
      version: 1,
      fields: ['country', 'aliases', 'code', 'value'],
      rowCount: LEGACY_OWNER_COUNTRIES_V1.length,
    },
  ];
}

export function findSemanticCountry(value: string | null | undefined): SemanticCountryGroupRow | null {
  const normalized = normalizeSemanticToken(value);
  if (!normalized) return null;
  return OPERATOR_COUNTRY_GROUPS_V1.find((candidate) => {
    if (normalizeSemanticToken(candidate.country) === normalized) return true;
    return candidate.aliases.some((alias) => normalizeSemanticToken(alias) === normalized);
  }) ?? null;
}

export function countryBelongsToSemanticGroup(country: string | null | undefined, groupId: string): boolean {
  const row = findSemanticCountry(country);
  if (!row) return false;
  const normalizedGroup = normalizeSemanticToken(groupId);
  return row.groups.some((group) => normalizeSemanticToken(group) === normalizedGroup);
}

/**
 * Find one unambiguous country/owner mention in a natural-language ask.
 *
 * Phrases match whole normalized tokens. Longer composite owners such as
 * "France/Germany" suppress their contained country matches, while two
 * separate countries remain ambiguous and return null. Short SDS codes only
 * match uppercase source tokens, so ordinary words such as "us" and "it" do
 * not become country filters.
 */
export function findSemanticCountryMention(value: string | null | undefined): SemanticCountryGroupRow | null {
  const sourceTokens = tokenizeSemanticText(value);
  if (sourceTokens.length === 0) return null;

  const candidates = [...LEGACY_OWNER_COUNTRIES_V1, ...OPERATOR_COUNTRY_GROUPS_V1];
  const matches: CountryMentionMatch[] = [];
  for (const candidate of candidates) {
    const terms = uniqueStrings([candidate.country, ...candidate.aliases]);
    for (const term of terms) {
      const termTokens = tokenizeSemanticText(term);
      if (termTokens.length === 0) continue;
      const isShortCode = term.length <= 4 && /^[A-Z0-9]+$/.test(term);
      for (let start = 0; start <= sourceTokens.length - termTokens.length; start += 1) {
        if (!tokensMatchAt(sourceTokens, termTokens, start, isShortCode)) continue;
        matches.push({
          candidate,
          start,
          end: start + termTokens.length,
          tokenCount: termTokens.length,
          characterCount: termTokens.reduce((sum, token) => sum + token.normalized.length, 0),
        });
      }
    }
  }
  if (matches.length === 0) return null;

  matches.sort((left, right) =>
    right.tokenCount - left.tokenCount ||
    right.characterCount - left.characterCount ||
    Number(Boolean(right.candidate.code)) - Number(Boolean(left.candidate.code))
  );

  const retained: CountryMentionMatch[] = [];
  for (const match of matches) {
    const contained = retained.some((higherRanked) =>
      higherRanked.start <= match.start && higherRanked.end >= match.end
    );
    if (!contained) retained.push(match);
  }

  const distinct = new Map<string, SemanticCountryGroupRow>();
  for (const match of retained) {
    const key = match.candidate.code ?? `semantic:${normalizeSemanticToken(match.candidate.country)}`;
    distinct.set(key, match.candidate);
  }
  return distinct.size === 1 ? distinct.values().next().value ?? null : null;
}

export function normalizeSemanticToken(value: string | null | undefined): string {
  return String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '');
}

function row(country: string, aliases: string[], groups: string[]): SemanticCountryGroupRow {
  return { country, aliases, groups };
}

interface SemanticTextToken {
  normalized: string;
  source: string;
  separatorAfter: string;
}

interface CountryMentionMatch {
  candidate: SemanticCountryGroupRow;
  start: number;
  end: number;
  tokenCount: number;
  characterCount: number;
}

function tokenizeSemanticText(value: string | null | undefined): SemanticTextToken[] {
  const text = String(value ?? '');
  const matches = [...text.matchAll(/[A-Za-z0-9]+/g)];
  return matches.map((match, index) => {
    const source = match[0];
    const end = (match.index ?? 0) + source.length;
    const nextStart = matches[index + 1]?.index ?? text.length;
    return {
      source,
      normalized: source.toLowerCase(),
      separatorAfter: text.slice(end, nextStart),
    };
  });
}

function tokensMatchAt(
  source: SemanticTextToken[],
  term: SemanticTextToken[],
  start: number,
  requireExactCase: boolean,
): boolean {
  for (let offset = 0; offset < term.length; offset += 1) {
    const sourceToken = source[start + offset];
    const termToken = term[offset];
    if (!sourceToken || !termToken) return false;
    if (requireExactCase) {
      if (sourceToken.source !== termToken.source) return false;
    } else if (sourceToken.normalized !== termToken.normalized) {
      return false;
    }
    // Preserve the slash that distinguishes a joint catalogue owner such as
    // France/Germany from an ask that independently names France and Germany.
    if (termToken.separatorAfter.includes('/') && !sourceToken.separatorAfter.includes('/')) {
      return false;
    }
  }
  return true;
}

function uniqueStrings(values: string[]): string[] {
  return [...new Set(values.filter(Boolean))];
}
