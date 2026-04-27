import fs from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { describe, expect, it } from 'vitest';

const DIST_INDEX_PATH = path.resolve(__dirname, '../dist/index.mjs');
const DIST_UI_INDEX_PATH = path.resolve(__dirname, '../dist/ui/index.mjs');
const DIST_STOREFRONT_INDEX_PATH = path.resolve(__dirname, '../dist/storefront/index.mjs');
const DIST_CLI_INDEX_PATH = path.resolve(__dirname, '../dist/cli/index.mjs');
const DIST_CHUNKS_PATH = path.resolve(__dirname, '../dist/chunks');
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

  it('ships a canonical bundled UI subpath entry without bare module specifiers', async () => {
    const source = await fs.readFile(DIST_UI_INDEX_PATH, 'utf8');
    expect(source.length).toBeGreaterThan(0);
    expect(collectBareSpecifiers(source)).toEqual([]);
  });

  it('ships a canonical bundled storefront subpath entry without bare module specifiers', async () => {
    const source = await fs.readFile(DIST_STOREFRONT_INDEX_PATH, 'utf8');
    expect(source.length).toBeGreaterThan(0);
    expect(collectBareSpecifiers(source)).toEqual([]);
  });

  it('shares bundled chunks between the root and UI entries to avoid duplicated browser runtime code', async () => {
    const [rootSource, uiSource, chunkNames] = await Promise.all([
      fs.readFile(DIST_INDEX_PATH, 'utf8'),
      fs.readFile(DIST_UI_INDEX_PATH, 'utf8'),
      fs.readdir(DIST_CHUNKS_PATH),
    ]);

    expect(chunkNames.some((name) => name.endsWith('.mjs'))).toBe(true);
    expect(rootSource).toContain('./chunks/');
    expect(uiSource).toContain('./chunks/');
  });

  it('exports the canonical root, UI, and storefront subpath surfaces from package.json', async () => {
    const packageJson = JSON.parse(await fs.readFile(PACKAGE_JSON_PATH, 'utf8'));

    expect(packageJson.exports?.['.']?.import).toBe('./dist/index.mjs');
    expect(packageJson.exports?.['./ui']?.import).toBe('./dist/ui/index.mjs');
    expect(packageJson.exports?.['./ui']?.types).toBe('./dist/ui/index.d.ts');
    expect(packageJson.exports?.['./storefront']?.import).toBe('./dist/storefront/index.mjs');
    expect(packageJson.exports?.['./storefront']?.types).toBe('./dist/storefront/index.d.ts');
    expect(packageJson.bin?.sdn).toBe('dist/cli/index.mjs');
    expect(
      Object.keys(packageJson.scripts ?? {}).some((name) =>
        name.includes('runtime-browser'),
      ),
    ).toBe(false);
  });

  it('includes the UI build step in the package build and publish lifecycle', async () => {
    const packageJson = JSON.parse(await fs.readFile(PACKAGE_JSON_PATH, 'utf8'));

    expect(packageJson.scripts?.build).toContain('build:ui');
    expect(packageJson.scripts?.prepublishOnly).toBe('npm run build');
  });

  it('ships a Node CLI entrypoint for global npm installs', async () => {
    const source = await fs.readFile(DIST_CLI_INDEX_PATH, 'utf8');

    expect(source.startsWith('#!/usr/bin/env node')).toBe(true);
    expect(source).toContain('wallet');
    expect(source).toContain('module');
  });

  it(
    'imports the built canonical root entry successfully',
    { timeout: 60_000 },
    async () => {
    const runtime = await import(pathToFileURL(DIST_INDEX_PATH).href);

    expect(typeof runtime.SDNNode?.create).toBe('function');
    expect(typeof runtime.getFlatSQLWASIPath).toBe('function');
    expect(runtime.mountWalletUI).toBeUndefined();
    },
  );

  it(
    'imports the built canonical UI subpath entry successfully',
    { timeout: 60_000 },
    async () => {
    const runtime = await import(pathToFileURL(DIST_UI_INDEX_PATH).href);

    expect(typeof runtime.mountWalletUI).toBe('function');
    expect(typeof runtime.ObservedPeerIndex).toBe('function');
    },
  );

  it(
    'imports the built canonical storefront subpath entry successfully',
    { timeout: 60_000 },
    async () => {
    const runtime = await import(pathToFileURL(DIST_STOREFRONT_INDEX_PATH).href);

    expect(typeof runtime.createStorefrontClient).toBe('function');
    expect(runtime.PaymentMethod.SDNCredits).toBe(4);
    expect(runtime.GrantStatus.Active).toBe(0);
    },
  );
});
