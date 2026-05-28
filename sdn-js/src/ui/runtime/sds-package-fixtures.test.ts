import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { describe, expect, it } from 'vitest';

const RUNTIME_SDS_FIXTURE_TESTS = [
  'local-flatsql.test.ts',
  'worker-sync-status-flatbuffer.test.ts',
];
const require = createRequire(import.meta.url);

describe('SDS test fixtures', () => {
  it('loads schemas and generated bindings from the package dependency', () => {
    const offenders = RUNTIME_SDS_FIXTURE_TESTS.filter((fileName) => {
      const source = readFileSync(new URL(fileName, import.meta.url), 'utf8');
      return source.includes('../../../../../spacedatastandards.org/');
    });

    expect(offenders).toEqual([]);
  });

  it('resolves SDS fixtures in single-repository CI checkouts', () => {
    for (const specifier of [
      'spacedatastandards.org/schema/CAT/main.fbs',
      'spacedatastandards.org/schema/OMM/main.fbs',
      'spacedatastandards.org/schema/PNM/main.fbs',
      'spacedatastandards.org/schema/DSS/main.fbs',
      'spacedatastandards.org/lib/js/DSS/DSS.js',
    ]) {
      const fixture = readFileSync(require.resolve(specifier), 'utf8');
      expect(fixture.length).toBeGreaterThan(0);
    }
  });
});
