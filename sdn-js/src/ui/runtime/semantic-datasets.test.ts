import { describe, expect, it } from 'vitest';

import {
  OPERATOR_COUNTRY_GROUPS_DATASET_ID,
  countryBelongsToSemanticGroup,
  findSemanticCountry,
  findSemanticCountryMention,
  semanticDatasetSummaries,
} from './semantic-datasets';

describe('semantic datasets', () => {
  it('exposes a versioned operator country grouping dataset for LLM query context', () => {
    expect(semanticDatasetSummaries()).toContainEqual(expect.objectContaining({
      id: OPERATOR_COUNTRY_GROUPS_DATASET_ID,
      version: 1,
      fields: expect.arrayContaining(['country', 'aliases', 'groups']),
      rowCount: expect.any(Number),
    }));
  });

  it('matches country aliases deterministically', () => {
    expect(findSemanticCountry('Russian Federation')?.country).toBe('Russia');
    expect(findSemanticCountry('UKRAINE')?.country).toBe('Ukraine');
    expect(findSemanticCountry('Kazakh SSR')?.country).toBe('Kazakhstan');
    expect(findSemanticCountry('not-a-country')).toBeNull();
  });

  it('matches whole phrases without short-code collisions or ambiguous country selection', () => {
    expect(findSemanticCountryMention('show us satellites')).toBeNull();
    expect(findSemanticCountryMention('Russian satellites')?.code).toBe('CIS');
    expect(findSemanticCountryMention('satellites')).toBeNull();
    expect(findSemanticCountryMention('France/Germany')?.code).toBe('FGER');
    expect(findSemanticCountryMention('France, Germany')).toBeNull();
    expect(findSemanticCountryMention('France Germany')).toBeNull();
    expect(findSemanticCountryMention('Algeria and Brazil')).toBeNull();
  });

  it('identifies former Soviet bloc members and rejects controls', () => {
    for (const country of ['Russia', 'Ukraine', 'Kazakhstan', 'Belarus']) {
      expect(countryBelongsToSemanticGroup(country, 'former_soviet_bloc')).toBe(true);
    }

    for (const country of ['United States', 'France', 'Japan']) {
      expect(countryBelongsToSemanticGroup(country, 'former_soviet_bloc')).toBe(false);
    }
  });
});
