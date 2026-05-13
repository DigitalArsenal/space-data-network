import { describe, expect, it } from 'vitest';

import { createDeterministicLocalLlmQueryAdapter } from './llm-query-adapter';
import type { LocalLlmQueryContext } from './llm-query-context';

describe('local LLM query adapter boundary', () => {
  it('returns visible read-only SQL from local context without raw bytes or transport credentials', async () => {
    const adapter = createDeterministicLocalLlmQueryAdapter();
    const seen: LocalLlmQueryContext[] = [];
    adapter.onContext = (context) => seen.push(context);

    const draft = await adapter.draftSql({
      ask: 'find all OMMs for satellites that belong to former soviet block nations that have periods greater than 1 day',
      context: {
        schema: {
          standardId: 'OMM',
          schemaName: 'OMM.fbs',
          tableName: 'OMM',
          columns: ['OBJECT_NAME', 'NORAD_CAT_ID', 'PERIOD', 'COUNTRY'],
        },
        source: {
          dataSourceId: 'configured:space-data-network-02',
          providerName: 'CelesTrak Provider',
          providerId: 'space-data-network-02',
          sourceName: 'celestrak-gp',
        },
        queryProfile: 'dataset-publication-offset-v1',
        queryProfiles: [],
        semanticDatasets: [{ id: 'operator_country_groups_v1', version: 1, fields: ['country', 'aliases', 'groups'], rowCount: 18 }],
        sampleRows: [{ OBJECT_NAME: 'SAT', COUNTRY: 'Russia', dataBytes: new Uint8Array([1]) }],
        limits: { maxRows: 100, maxBytes: 64_000, timeoutMs: 5_000 },
      },
    });

    expect(draft.sql).toMatch(/^SELECT /);
    expect(draft.sql).toContain('LIMIT 100');
    expect(draft.rationale).toMatch(/local/i);
    expect(JSON.stringify(seen)).not.toContain('dataBytes');
    expect(JSON.stringify(seen)).not.toContain('candidateAddrs');
    expect(JSON.stringify(seen)).not.toContain('gatewayUrl');
  });
});
