import { describe, expect, it } from 'vitest';

import { buildEpochProfileSql } from './epoch-query-sql';

describe('epoch profile SQL builder', () => {
  it('builds half-open OMM day and window queries', () => {
    expect(buildEpochProfileSql({
      standardId: 'OMM',
      profile: 'epoch.day',
      day: '2026-05-11',
      limit: 10,
    })).toBe("SELECT * FROM OMM WHERE EPOCH >= '2026-05-11T00:00:00Z' AND EPOCH < '2026-05-12T00:00:00Z' ORDER BY EPOCH ASC, NORAD_CAT_ID ASC LIMIT 10");

    expect(buildEpochProfileSql({
      standardId: 'OMM',
      profile: 'epoch.window',
      from: '2026-05-11T00:00',
      to: '2026-05-12T00:00',
      entityId: '25544',
      limit: 25,
    })).toBe("SELECT * FROM OMM WHERE EPOCH >= '2026-05-11T00:00:00Z' AND EPOCH < '2026-05-12T00:00:00Z' AND NORAD_CAT_ID = 25544 ORDER BY EPOCH ASC, NORAD_CAT_ID ASC LIMIT 25");
  });

  it('builds fill-policy and coverage queries for local OMM data', () => {
    expect(buildEpochProfileSql({
      standardId: 'OMM',
      profile: 'epoch.nearest',
      at: '2026-05-11T12:00',
      maxDeltaSeconds: 86400,
      limit: 5,
    })).toContain("ROW_NUMBER() OVER (PARTITION BY NORAD_CAT_ID ORDER BY ABS(strftime('%s', EPOCH) - strftime('%s', '2026-05-11T12:00:00Z')) ASC");

    expect(buildEpochProfileSql({
      standardId: 'OMM',
      profile: 'epoch.coverage',
      from: '2026-05-01T00:00',
      to: '2026-06-01T00:00',
      limit: 31,
    })).toBe("SELECT substr(EPOCH, 1, 10) AS epoch_day, COUNT(*) AS row_count, MIN(EPOCH) AS oldest_epoch, MAX(EPOCH) AS newest_epoch FROM OMM WHERE EPOCH IS NOT NULL AND EPOCH >= '2026-05-01T00:00:00Z' AND EPOCH < '2026-06-01T00:00:00Z' GROUP BY epoch_day ORDER BY epoch_day DESC LIMIT 31");
  });

  it('rejects unsupported standards and invalid dates', () => {
    expect(() => buildEpochProfileSql({
      standardId: 'PNM',
      profile: 'epoch.day',
      day: '2026-05-11',
    })).toThrow('Epoch profile SQL is currently available for OMM');

    expect(() => buildEpochProfileSql({
      standardId: 'OMM',
      profile: 'epoch.day',
      day: 'May 11',
    })).toThrow('expected YYYY-MM-DD');
  });
});
