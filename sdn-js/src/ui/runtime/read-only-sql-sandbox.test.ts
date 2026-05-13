import { describe, expect, it } from 'vitest';

import { validateReadOnlySql } from './read-only-sql-sandbox';

describe('read-only SQL sandbox', () => {
  it('accepts SELECT and WITH SELECT statements and applies bounded limits', () => {
    expect(validateReadOnlySql('SELECT * FROM OMM', { defaultLimit: 100, maxLimit: 100 }).sql)
      .toBe('SELECT * FROM OMM LIMIT 100');
    expect(validateReadOnlySql('WITH latest AS (SELECT * FROM OMM) SELECT * FROM latest', { defaultLimit: 100, maxLimit: 100 }).sql)
      .toBe('WITH latest AS (SELECT * FROM OMM) SELECT * FROM latest LIMIT 100');
    expect(validateReadOnlySql('SELECT * FROM OMM LIMIT 1000', { defaultLimit: 100, maxLimit: 250 }).sql)
      .toBe('SELECT * FROM OMM LIMIT 250');
  });

  it('rejects mutation, metadata, extension, file, network, and chained statements', () => {
    for (const sql of [
      'DELETE FROM OMM',
      'INSERT INTO OMM VALUES (1)',
      'UPDATE OMM SET OBJECT_NAME = \'x\'',
      'CREATE TABLE X(id INTEGER)',
      'DROP TABLE OMM',
      'ALTER TABLE OMM ADD COLUMN X TEXT',
      'VACUUM',
      'PRAGMA table_info(OMM)',
      'ATTACH DATABASE \'x.db\' AS x',
      'SELECT load_extension(\'x\')',
      'SELECT readfile(\'/etc/passwd\')',
      'SELECT http_get(\'https://example.invalid\')',
      'SELECT * FROM OMM; SELECT * FROM PNM',
    ]) {
      const result = validateReadOnlySql(sql);
      expect(result.ok, sql).toBe(false);
      expect(result.diagnostics.length, sql).toBeGreaterThan(0);
    }
  });

  it('does not treat forbidden words inside quoted strings or comments as SQL operations', () => {
    expect(validateReadOnlySql("SELECT 'DELETE FROM OMM' AS note FROM OMM -- DROP TABLE OMM").ok).toBe(true);
  });
});
