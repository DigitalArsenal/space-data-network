import fs from 'node:fs/promises';
import { execFile } from 'node:child_process';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { promisify } from 'node:util';
import { describe, expect, it } from 'vitest';

const DIST_INDEX_PATH = path.resolve(__dirname, '../dist/index.mjs');
const DIST_UI_INDEX_PATH = path.resolve(__dirname, '../dist/ui/index.mjs');
const DIST_STOREFRONT_INDEX_PATH = path.resolve(__dirname, '../dist/storefront/index.mjs');
const DIST_PATH = path.resolve(__dirname, '../dist');
const PACKAGE_JSON_PATH = path.resolve(__dirname, '../package.json');
const execFileAsync = promisify(execFile);

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

function collectRelativeModuleSpecifiers(source: string): string[] {
  const withoutComments = source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '');
  const specifiers = new Set<string>();
  const patterns = [
    /^\s*(?:import|export)\s+(?:[^("'`].*?\s+from\s+)?['"]([^'"]+)['"]/gm,
    /\bimport\(\s*['"]([^'"]+)['"]\s*\)/g,
  ];

  for (const pattern of patterns) {
    for (const match of withoutComments.matchAll(pattern)) {
      const specifier = match[1];
      if (specifier.startsWith('./') || specifier.startsWith('../')) {
        specifiers.add(specifier);
      }
    }
  }

  return [...specifiers].sort();
}

async function collectDistModulePaths(dir = DIST_PATH): Promise<string[]> {
  const entries = await fs.readdir(dir, { withFileTypes: true });
  const paths = await Promise.all(
    entries.map(async (entry) => {
      const entryPath = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        return collectDistModulePaths(entryPath);
      }
      return entry.name.endsWith('.mjs') ? [entryPath] : [];
    }),
  );
  return paths.flat().sort();
}

describe('sdn-js package build', () => {
  it('ships a canonical root entry without unresolved package imports', async () => {
    const source = await fs.readFile(DIST_INDEX_PATH, 'utf8');
    expect(source.length).toBeGreaterThan(0);
    expect(collectBareSpecifiers(source)).toEqual([]);
  });

  it('ships a canonical UI subpath entry without unresolved package imports', async () => {
    const source = await fs.readFile(DIST_UI_INDEX_PATH, 'utf8');
    expect(source.length).toBeGreaterThan(0);
    expect(collectBareSpecifiers(source)).toEqual([]);
  });

  it('ships a canonical storefront subpath entry without unresolved package imports', async () => {
    const source = await fs.readFile(DIST_STOREFRONT_INDEX_PATH, 'utf8');
    expect(source.length).toBeGreaterThan(0);
    expect(collectBareSpecifiers(source)).toEqual([]);
  });

  it('inlines package imports across every published browser module', async () => {
    const modules = await collectDistModulePaths();
    const offenders: Record<string, string[]> = {};

    for (const modulePath of modules) {
      const source = await fs.readFile(modulePath, 'utf8');
      const bareSpecifiers = collectBareSpecifiers(source);
      if (bareSpecifiers.length > 0) {
        offenders[path.relative(DIST_PATH, modulePath)] = bareSpecifiers;
      }
    }

    expect(modules.length).toBeGreaterThan(0);
    expect(offenders).toEqual({});
  });

  it('ships self-contained browser entry bundles without shared JS chunks', async () => {
    const modules = await collectDistModulePaths();
    const relativeImports: Record<string, string[]> = {};

    for (const modulePath of modules) {
      const source = await fs.readFile(modulePath, 'utf8');
      const specifiers = collectRelativeModuleSpecifiers(source);
      if (specifiers.length > 0) {
        relativeImports[path.relative(DIST_PATH, modulePath)] = specifiers;
      }
    }

    await expect(fs.access(path.resolve(DIST_PATH, 'chunks'))).rejects.toThrow();
    expect(modules.map((modulePath) => path.relative(DIST_PATH, modulePath))).toEqual([
      'astro/index.mjs',
      'index.mjs',
      'storefront/index.mjs',
      'ui/index.mjs',
    ]);
    expect(relativeImports).toEqual({});
  });

  it('exports the canonical root, UI, and storefront subpath surfaces from package.json', async () => {
    const packageJson = JSON.parse(await fs.readFile(PACKAGE_JSON_PATH, 'utf8'));

    expect(packageJson.exports?.['.']?.import).toBe('./dist/index.mjs');
    expect(packageJson.exports?.['./ui']?.import).toBe('./dist/ui/index.mjs');
    expect(packageJson.exports?.['./ui']?.types).toBe('./dist/ui/index.d.ts');
    expect(packageJson.exports?.['./storefront']?.import).toBe('./dist/storefront/index.mjs');
    expect(packageJson.exports?.['./storefront']?.types).toBe('./dist/storefront/index.d.ts');
    expect(packageJson.exports?.['./astro']?.import).toBe('./dist/astro/index.mjs');
    expect(packageJson.exports?.['./astro']?.types).toBe('./dist/astro/index.d.ts');
    expect(
      Object.keys(packageJson.scripts ?? {}).some((name) =>
        name.includes('runtime-browser'),
      ),
    ).toBe(false);
    expect(packageJson.scripts?.prepublishOnly).not.toContain('--prefix');
  });

  it('keeps the full UI build separate from the package publish build', async () => {
    const packageJson = JSON.parse(await fs.readFile(PACKAGE_JSON_PATH, 'utf8'));

    expect(packageJson.scripts?.build).toContain('build:ui');
    expect(packageJson.scripts?.buildPackage ?? packageJson.scripts?.['build:package']).toContain('build:core');
    expect(packageJson.scripts?.prepublishOnly).toBe(
      'npm run check:versions && npm run build:package',
    );
  });

  it(
    'imports the built root entry under Node when global WebSocket is absent',
    { timeout: 60_000 },
    async () => {
      const script = `
        const previousWebSocket = globalThis.WebSocket;
        delete globalThis.WebSocket;
        try {
          await import(${JSON.stringify(pathToFileURL(DIST_INDEX_PATH).href)} + '?no-global-websocket=' + Date.now());
        } finally {
          if (previousWebSocket !== undefined) {
            globalThis.WebSocket = previousWebSocket;
          }
        }
      `;

      const { stderr } = await execFileAsync(process.execPath, ['--input-type=module', '--eval', script], {
        cwd: path.resolve(__dirname, '..'),
        timeout: 60_000,
      });

      expect(stderr).not.toContain('ReferenceError: WebSocket is not defined');
    },
  );

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
    expect(runtime.PaymentMethod.SDNCredits).toBe(3);
    expect(runtime.PaymentMethod.FiatStripe).toBe(4);
    expect(runtime.PaymentMethod.Free).toBe(5);
    expect('CryptoUSDC' in runtime.PaymentMethod).toBe(false);
    expect(runtime.GrantStatus.Active).toBe(0);
    },
  );
});
