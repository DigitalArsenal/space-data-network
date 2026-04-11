import fs from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { describe, expect, it } from 'vitest';

const DIST_INDEX_PATH = path.resolve(__dirname, '../dist/index.mjs');
const PACKAGE_JSON_PATH = path.resolve(__dirname, '../package.json');

function collectBareSpecifiers(source: string): string[] {
  const withoutComments = source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '');
  const specifiers = new Set<string>();
  const patterns = [
    /^\s*import\s+(?:[^("'`].*?\s+from\s+)?['"]([^'"]+)['"]/gm,
    /\bimport\(\s*['"]([^'"]+)['"]\s*\)/g,
  ];

  for (const pattern of patterns) {
    for (const match of withoutComments.matchAll(pattern)) {
      const specifier = match[1];
      if (
        !specifier.startsWith('.') &&
        !specifier.startsWith('/') &&
        !specifier.startsWith('data:') &&
        !specifier.startsWith('file:') &&
        !specifier.startsWith('http:') &&
        !specifier.startsWith('https:')
      ) {
        specifiers.add(specifier);
      }
    }
  }

  return [...specifiers].sort();
}

describe('sdn-js package build', () => {
  it('ships a canonical bundled root entry without bare module specifiers', async () => {
    const source = await fs.readFile(DIST_INDEX_PATH, 'utf8');
    expect(source.length).toBeGreaterThan(0);
    expect(collectBareSpecifiers(source)).toEqual([]);
  });

  it('exports only the canonical root browser surface from package.json', async () => {
    const packageJson = JSON.parse(await fs.readFile(PACKAGE_JSON_PATH, 'utf8'));

    expect(packageJson.exports?.['.']?.import).toBe('./dist/index.mjs');
    expect(packageJson.exports?.['./runtime-browser']).toBeUndefined();
    expect(
      Object.keys(packageJson.scripts ?? {}).some((name) =>
        name.includes('runtime-browser'),
      ),
    ).toBe(false);
  });

  it('imports the built canonical root entry successfully', async () => {
    const runtime = await import(pathToFileURL(DIST_INDEX_PATH).href);

    expect(typeof runtime.SDNNode?.create).toBe('function');
    expect(typeof runtime.getFlatSQLWASIPath).toBe('function');
  });
});
