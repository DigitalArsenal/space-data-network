import { describe, expect, it } from 'vitest';

import { buildLocalLlmQueryContext } from './llm-query-context';

describe('local LLM query context', () => {
  it('builds bounded context from schema, source identity, query profiles, semantic datasets, and sample rows', () => {
    const context = buildLocalLlmQueryContext({
      standardId: 'OMM',
      schemaName: 'OMM.fbs',
      tableName: 'OMM',
      columns: ['OBJECT_NAME', 'OBJECT_ID', 'NORAD_CAT_ID', 'EPOCH', 'MEAN_MOTION', '_data', 'signature'],
      queryProfile: 'epoch.day',
      source: {
        dataSourceId: 'configured:space-data-network-02',
        datastoreKey: 'sdn-ds-v1-history',
        providerName: 'CelesTrak Provider',
        providerPeerId: '16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4',
        providerPublicKey: '90aa23ea4ff2d68cf8cb8155135fe5a25b580ec805e835aabb0e8905ffb2c3b2',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
      },
      sampleRows: [{
        OBJECT_NAME: 'STARLINK-6292',
        OBJECT_ID: '2023-078J',
        NORAD_CAT_ID: 56775,
        EPOCH: '2026-05-10T10:45:31Z',
        _data: new Uint8Array([1, 2, 3]),
        signature: 'opaque-signature',
      }],
    });

    expect(context.schema.columns).toEqual(['OBJECT_NAME', 'OBJECT_ID', 'NORAD_CAT_ID', 'EPOCH', 'MEAN_MOTION']);
    expect(context.source).toEqual(expect.objectContaining({
      providerName: 'CelesTrak Provider',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
    }));
    expect(context.queryProfiles.map((profile) => profile.id)).toEqual(expect.arrayContaining([
      'epoch.day',
      'epoch.window',
      'epoch.as_of',
      'epoch.forward',
      'epoch.nearest',
      'epoch.coverage',
    ]));
    expect(context.semanticDatasets).toContainEqual(expect.objectContaining({
      id: 'operator_country_groups_v1',
      version: 1,
    }));
    expect(context.sampleRows).toEqual([{
      OBJECT_NAME: 'STARLINK-6292',
      OBJECT_ID: '2023-078J',
      NORAD_CAT_ID: 56775,
      EPOCH: '2026-05-10T10:45:31Z',
    }]);
  });

  it('caps sample rows at ten', () => {
    const context = buildLocalLlmQueryContext({
      standardId: 'OMM',
      schemaName: 'OMM.fbs',
      tableName: 'OMM',
      columns: ['NORAD_CAT_ID'],
      queryProfile: 'dataset-publication-offset-v1',
      source: { dataSourceId: 'local', providerName: 'Local Desktop' },
      sampleRows: Array.from({ length: 12 }, (_, index) => ({ NORAD_CAT_ID: index })),
    });

    expect(context.sampleRows).toHaveLength(10);
    expect(context.limits.maxRows).toBe(100);
  });
});
