import { describe, expect, it } from 'vitest';

import {
  buildLocalDataExplorerQuery,
  isEpochDataExplorerColumn,
  isNumericDataExplorerColumn,
  localDataExplorerSearchColumns,
  localDataExplorerCountFromResult,
} from '../../ui/src/lib/data-explorer-query';

describe('data explorer query builder', () => {
  it('pushes plaintext search and column filters into the local FlatSQL query', () => {
    const query = buildLocalDataExplorerQuery({
      standardId: 'OMM',
      page: 2,
      pageSize: 10,
      selectColumns: ['OBJECT_NAME', 'NORAD_CAT_ID'],
      searchText: 'sputnik',
      searchColumns: ['OBJECT_NAME', 'OBJECT_ID'],
      columnFilters: { OBJECT_NAME: 'vanguard' },
      filterColumns: ['OBJECT_NAME'],
    });

    expect(query.hasDatasetFilters).toBe(true);
    expect(query.rowsSql).toContain('SELECT "OBJECT_NAME", "NORAD_CAT_ID" FROM "OMM" WHERE');
    expect(query.rowsSql).toContain('CAST("OBJECT_NAME" AS TEXT) LIKE');
    expect(query.rowsSql).toContain('CAST("OBJECT_ID" AS TEXT) LIKE');
    expect(query.rowsSql).toContain('LIMIT 10 OFFSET 20');
    expect(query.countSql).toContain('SELECT COUNT(*) AS __total FROM "OMM" WHERE');
  });

  it('limits projected local explorer rows to requested columns', () => {
    const query = buildLocalDataExplorerQuery({
      standardId: 'CAT',
      page: 0,
      pageSize: 10,
      selectColumns: [
        'OBJECT_NAME',
        'OBJECT_ID',
        'NORAD_CAT_ID',
        'OBJECT_TYPE',
        'OPS_STATUS_CODE',
        'OWNER',
        'LAUNCH_SITE',
        'LAUNCH_DATE',
        'DECAY_DATE',
        'PERIOD',
      ],
    });

    expect(query.rowsSql).toBe('SELECT "OBJECT_NAME", "OBJECT_ID", "NORAD_CAT_ID", "OBJECT_TYPE", "OPS_STATUS_CODE", "OWNER", "LAUNCH_SITE", "LAUNCH_DATE", "DECAY_DATE", "PERIOD" FROM "CAT" LIMIT 10 OFFSET 0');
  });

  it('builds numeric comparison filters for numeric columns', () => {
    const query = buildLocalDataExplorerQuery({
      standardId: 'OMM',
      page: 0,
      pageSize: 10,
      columnFilters: {
        MEAN_MOTION: '> 1',
        NORAD_CAT_ID: '100..200',
      },
      filterColumns: ['MEAN_MOTION', 'NORAD_CAT_ID'],
    });

    expect(isNumericDataExplorerColumn('MEAN_MOTION')).toBe(true);
    expect(query.rowsSql).toContain('CAST("MEAN_MOTION" AS REAL) > 1');
    expect(query.rowsSql).toContain('CAST("NORAD_CAT_ID" AS REAL) BETWEEN 100 AND 200');
    expect(query.rowsSql).not.toContain('CAST("MEAN_MOTION" AS TEXT) LIKE');
  });

  it('builds EPOCH start and stop range filters from date picker values', () => {
    const query = buildLocalDataExplorerQuery({
      standardId: 'OMM',
      page: 0,
      pageSize: 10,
      columnFilters: {
        EPOCH: '2026-05-10T00:00:00.000Z..2026-05-11T00:00:00.000Z',
      },
      filterColumns: ['EPOCH'],
    });

    expect(isEpochDataExplorerColumn('EPOCH')).toBe(true);
    expect(query.rowsSql).toContain('CAST("EPOCH" AS TEXT) >= \'2026-05-10T00:00:00.000Z\'');
    expect(query.rowsSql).toContain('CAST("EPOCH" AS TEXT) < \'2026-05-11T00:00:00.000Z\'');
    expect(query.rowsSql).not.toContain('CAST("EPOCH" AS TEXT) LIKE');
  });

  it('builds one-sided EPOCH range filters', () => {
    const startOnly = buildLocalDataExplorerQuery({
      standardId: 'OMM',
      page: 0,
      pageSize: 10,
      columnFilters: { EPOCH: '2026-05-10T00:00:00.000Z..' },
      filterColumns: ['EPOCH'],
    });
    const stopOnly = buildLocalDataExplorerQuery({
      standardId: 'OMM',
      page: 0,
      pageSize: 10,
      columnFilters: { EPOCH: '..2026-05-11T00:00:00.000Z' },
      filterColumns: ['EPOCH'],
    });

    expect(startOnly.rowsSql).toContain('CAST("EPOCH" AS TEXT) >= \'2026-05-10T00:00:00.000Z\'');
    expect(startOnly.rowsSql).not.toContain('LIKE');
    expect(stopOnly.rowsSql).toContain('CAST("EPOCH" AS TEXT) < \'2026-05-11T00:00:00.000Z\'');
    expect(stopOnly.rowsSql).not.toContain('LIKE');
  });

  it('keeps plaintext search focused on meaningful text and ID columns', () => {
    expect(localDataExplorerSearchColumns('OMM', [
      'OBJECT_NAME',
      'NORAD_CAT_ID',
      'MEAN_MOTION',
      'ECCENTRICITY',
      'sourceName',
      '_data',
    ])).toEqual([
      'OBJECT_NAME',
      'NORAD_CAT_ID',
      'sourceName',
    ]);

    const query = buildLocalDataExplorerQuery({
      standardId: 'OMM',
      page: 0,
      pageSize: 10,
      searchText: 'starlink',
      searchColumns: localDataExplorerSearchColumns('OMM', [
        'OBJECT_NAME',
        'NORAD_CAT_ID',
        'MEAN_MOTION',
        'sourceName',
        '_data',
      ]),
    });

    expect(query.rowsSql).toContain('CAST("OBJECT_NAME" AS TEXT) LIKE');
    expect(query.rowsSql).toContain('CAST("NORAD_CAT_ID" AS TEXT) LIKE');
    expect(query.rowsSql).toContain('CAST("sourceName" AS TEXT) LIKE');
    expect(query.rowsSql).not.toContain('CAST("MEAN_MOTION" AS TEXT) LIKE');
    expect(query.rowsSql).not.toContain('"_data"');
  });

  it('reads filtered totals from COUNT query results', () => {
    expect(localDataExplorerCountFromResult({
      columns: ['__total'],
      rows: [[42]],
      records: [{ __total: 42 }],
    })).toBe(42);
  });
});
