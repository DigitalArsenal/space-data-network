import { describe, expect, it } from 'vitest';

import { createDeterministicLocalLlmQueryAdapter } from './llm-query-adapter';
import type { LocalLlmQueryContext } from './llm-query-context';

function queryContext(options: {
  standardId?: string;
  tableName?: string;
  columns: string[];
}): LocalLlmQueryContext {
  const standardId = options.standardId ?? options.tableName ?? 'CAT';
  return {
    schema: {
      standardId,
      schemaName: `${standardId}.fbs`,
      tableName: options.tableName ?? standardId,
      columns: options.columns,
    },
    source: {
      dataSourceId: 'local:test',
      providerName: 'Local test provider',
    },
    queryProfile: 'dataset-publication-offset-v1',
    queryProfiles: [],
    semanticDatasets: [],
    sampleRows: [],
    limits: { maxRows: 100, maxBytes: 64_000, timeoutMs: 5_000 },
  };
}

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

  it.each([
    ['Algeria', 3],
    ['BRAZ', 16],
    ['United States', 120],
  ])('filters real CAT data for the individual owner %s', async (country, owner) => {
    const adapter = createDeterministicLocalLlmQueryAdapter();
    const draft = await adapter.draftSql({
      ask: `show catalog objects owned by ${country}`,
      context: queryContext({ columns: ['OBJECT_NAME', 'OWNER'] }),
    });

    expect(draft.sql).toBe(`SELECT * FROM CAT WHERE OWNER = ${owner} LIMIT 100`);
  });

  it('uses fixed, escaped metadata values for genuine string COUNTRY columns', async () => {
    const adapter = createDeterministicLocalLlmQueryAdapter();
    const algeria = await adapter.draftSql({
      ask: 'show records from Algeria',
      context: queryContext({ standardId: 'COMPAT', tableName: 'records', columns: ['COUNTRY'] }),
    });
    const bangladesh = await adapter.draftSql({
      ask: 'show records from Bangladesh',
      context: queryContext({ standardId: 'COMPAT', tableName: 'records', columns: ['COUNTRY'] }),
    });

    expect(algeria.sql).toBe("SELECT * FROM records WHERE COUNTRY = 'Algeria' LIMIT 100");
    expect(bangladesh.sql).toBe(
      "SELECT * FROM records WHERE COUNTRY = 'People''s Republic of Bangladesh' LIMIT 100",
    );
  });

  it('never interpolates adversarial ask text into generated SQL', async () => {
    const adapter = createDeterministicLocalLlmQueryAdapter();
    const draft = await adapter.draftSql({
      ask: "show records from Algeria'; DROP TABLE CAT; --",
      context: queryContext({ columns: ['OWNER'] }),
    });

    expect(draft.sql).toBe('SELECT * FROM CAT WHERE OWNER = 3 LIMIT 100');
    expect(draft.sql.toLowerCase()).not.toContain('drop');
  });

  it('keeps the safe fallback for unrelated, ambiguous, or unsupported-schema asks', async () => {
    const adapter = createDeterministicLocalLlmQueryAdapter();
    const unrelated = await adapter.draftSql({
      ask: 'show active payloads',
      context: queryContext({ columns: ['OBJECT_NAME', 'OWNER'] }),
    });
    const ambiguous = await adapter.draftSql({
      ask: 'show Algeria and Brazil',
      context: queryContext({ columns: ['OWNER'] }),
    });
    const realOmm = await adapter.draftSql({
      ask: 'show objects from Algeria',
      context: queryContext({ standardId: 'OMM', columns: ['OBJECT_NAME', 'MEAN_MOTION'] }),
    });
    const stringOwner = await adapter.draftSql({
      ask: 'show channels owned by Algeria',
      context: queryContext({ standardId: 'CHN', columns: ['NAME', 'OWNER'] }),
    });

    expect(unrelated.sql).toBe('SELECT * FROM CAT LIMIT 100');
    expect(ambiguous.sql).toBe('SELECT * FROM CAT LIMIT 100');
    expect(realOmm.sql).toBe('SELECT * FROM OMM LIMIT 100');
    expect(stringOwner.sql).toBe('SELECT * FROM CHN LIMIT 100');
  });
});
