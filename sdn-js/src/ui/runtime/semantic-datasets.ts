export interface SemanticCountryGroupRow {
  country: string;
  aliases: string[];
  groups: string[];
}

export interface SemanticDatasetSummary {
  id: string;
  version: number;
  fields: string[];
  rowCount: number;
}

export const OPERATOR_COUNTRY_GROUPS_DATASET_ID = 'operator_country_groups_v1';

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

export function semanticDatasetSummaries(): SemanticDatasetSummary[] {
  return [{
    id: OPERATOR_COUNTRY_GROUPS_DATASET_ID,
    version: 1,
    fields: ['country', 'aliases', 'groups'],
    rowCount: OPERATOR_COUNTRY_GROUPS_V1.length,
  }];
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
