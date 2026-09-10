import fs from 'node:fs/promises';
import { execFile } from 'node:child_process';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { promisify } from 'node:util';
import { describe, expect, it } from 'vitest';

const DIST_INDEX_PATH = path.resolve(__dirname, '../dist/index.mjs');
const DIST_HTTP_PATH = path.resolve(__dirname, '../dist/transport/http.mjs');
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
  it('imports a self-contained HTTP reader within a 64 KiB JavaScript budget', async () => {
    const source = await fs.readFile(DIST_HTTP_PATH, 'utf8');
    expect(Buffer.byteLength(source)).toBeLessThanOrEqual(64 * 1024);
    expect(collectBareSpecifiers(source)).toEqual([]);
    expect(collectRelativeModuleSpecifiers(source)).toEqual([]);
    const { stdout, stderr } = await execFileAsync(process.execPath, [
      '--input-type=module', '--eval',
      `const { HttpTransport, iterateSizePrefixedFrameStream } = await import('@spacedatanetwork/sdn-js/http');
       console.log(typeof HttpTransport, typeof iterateSizePrefixedFrameStream);`,
    ], { cwd: path.resolve(__dirname, '..'), timeout: 10_000 });
    expect(stdout.trim()).toBe('function function');
    expect(stderr).toBe('');
    await fs.access(path.resolve(DIST_PATH, 'transport/http.d.ts'));
  });

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
      'index.mjs',
      'status/index.mjs',
      'storefront/index.mjs',
      'transport/http.mjs',
      'ui/index.mjs',
    ]);
    expect(relativeImports).toEqual({});
  });

  it('exports the canonical root, UI, and storefront subpath surfaces from package.json', async () => {
    const packageJson = JSON.parse(await fs.readFile(PACKAGE_JSON_PATH, 'utf8'));

    expect(packageJson.exports?.['.']?.import).toBe('./dist/index.mjs');
    expect(packageJson.exports?.['./http']?.import).toBe('./dist/transport/http.mjs');
    expect(packageJson.exports?.['./http']?.types).toBe('./dist/transport/http.d.ts');
    expect(packageJson.exports?.['./ui']?.import).toBe('./dist/ui/index.mjs');
    expect(packageJson.exports?.['./ui']?.types).toBe('./dist/ui/index.d.ts');
    expect(packageJson.exports?.['./status']?.import).toBe('./dist/status/index.mjs');
    expect(packageJson.exports?.['./status']?.types).toBe('./dist/status/index.d.ts');
    expect(packageJson.exports?.['./storefront']?.import).toBe('./dist/storefront/index.mjs');
    expect(packageJson.exports?.['./storefront']?.types).toBe('./dist/storefront/index.d.ts');
    expect(
      Object.keys(packageJson.scripts ?? {}).some((name) =>
        name.includes('runtime-browser'),
      ),
    ).toBe(false);
    expect(packageJson.scripts?.prepublishOnly).not.toContain('--prefix');
  });

  /**
   * THE ASTRO FLOOR, INVERTED (3.0.0, graph task
   * `sdn-js-astro-ships-a-js-propagator`).
   *
   * Until 2.0.19 this file asserted that `./astro` EXISTED. It shipped a
   * JavaScript SGP4/SDP4 propagator (satellite.js), GMST and ECI->ECEF
   * rotations, golden-section TCA refinement and a Foster probability-of-
   * collision integral — as published SDK API, with satellite.js's own WASM
   * runtime deliberately shimmed out so the browser bundle took the pure-JS
   * path. OWNER LAW 2026-08-09, verbatim: "There is no JS physics at all.
   * Everything we are doing is through WASM space data module SDK no
   * exceptions."
   *
   * Deleting the export is only half the job; the other half is making the
   * deletion load-bearing, so the same contract line that used to demand the
   * propagator now forbids it. Propagation is `propagator/sgp4`, frames/time
   * are `foundation/frames` + `foundation/time`, and screening/Pc is
   * `analysis/conjunction-assessment` — all reached through the
   * space-data-module-sdk browser harness, never re-implemented here.
   */
  it('publishes NO astro subpath and depends on no JS propagator', async () => {
    const packageJson = JSON.parse(await fs.readFile(PACKAGE_JSON_PATH, 'utf8'));

    expect(packageJson.exports?.['./astro']).toBeUndefined();
    expect(Object.keys(packageJson.exports ?? {})).toEqual([
      '.',
      './http',
      './ui',
      './status',
      './storefront',
    ]);
    expect(packageJson.dependencies?.['satellite.js']).toBeUndefined();
    expect(packageJson.devDependencies?.['satellite.js']).toBeUndefined();

    // Removing a published export is a MAJOR break, and 2.0.19 is on npm with
    // `dist/astro/index.mjs` inside its tarball.
    expect(Number.parseInt(String(packageJson.version).split('.')[0], 10)).toBeGreaterThanOrEqual(3);

    // No published byte may carry the JS propagator, under any name.
    const modules = await collectDistModulePaths();
    const offenders: string[] = [];
    for (const modulePath of modules) {
      const source = await fs.readFile(modulePath, 'utf8');
      if (/twoline2satrec|json2satrec|\bsatrec\b|gstime\s*\(/.test(source)) {
        offenders.push(path.relative(DIST_PATH, modulePath));
      }
    }
    expect(offenders).toEqual([]);

    // And no source file may re-import it.
    await expect(fs.access(path.resolve(__dirname, 'astro'))).rejects.toThrow();
    await expect(
      fs.access(path.resolve(__dirname, 'shims/satellite-wasm-disabled.ts')),
    ).rejects.toThrow();
  });

  it('builds only the package core (UI apps removed — owner clean slate 2026-07-24)', async () => {
    const packageJson = JSON.parse(await fs.readFile(PACKAGE_JSON_PATH, 'utf8'));

    expect(packageJson.scripts?.build).toContain('build:core');
    expect(packageJson.scripts?.build).not.toContain('build:ui');
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

  it('keeps FlatSQL WASI URL resolution lazy for downstream browser bundles', async () => {
    const source = await fs.readFile(path.resolve(__dirname, 'flatsql.ts'), 'utf8');
    const dist = await fs.readFile(DIST_INDEX_PATH, 'utf8');

    expect(source).toContain("const DEFAULT_WASI_RELATIVE_PATH = '../wasm/flatsql-wasi.wasm'");
    expect(source).toContain('function getDefaultWASIURL()');
    expect(source).not.toContain("const DEFAULT_WASI_URL = new URL('../wasm/flatsql-wasi.wasm', import.meta.url)");
    expect(dist).not.toMatch(/DEFAULT_WASI_URL\s*=\s*new URL\([^)]*import_meta/u);
  });

  it('normalizes persisted libp2p peer metadata byte views before validation', async () => {
    const dist = await fs.readFile(DIST_INDEX_PATH, 'utf8');
    const normalizerReferences = dist.match(/normalizePeerMetadataValue/g) ?? [];

    expect(dist.includes('function normalizePeerMetadataValue')).toBe(true);
    expect(normalizerReferences.length).toBeGreaterThanOrEqual(4);
    expect(dist).not.toMatch(
      /if \(!\(value\d* instanceof Uint8Array\)\) \{\n\s+throw new InvalidParametersError\d*\("Metadata value must be a Uint8Array"\)/u,
    );
  });

  it(
    'imports the built canonical root entry successfully',
    { timeout: 60_000 },
    async () => {
    const runtime = await import(pathToFileURL(DIST_INDEX_PATH).href);

    expect(typeof runtime.SDNNode?.create).toBe('function');
    expect(typeof runtime.createHeliaSDNNode).toBe('function');
    expect(typeof runtime.fetchCIDBytesFromHelia).toBe('function');
    expect(typeof runtime.getFlatSQLWASIPath).toBe('function');
    expect(runtime.mountWalletUI).toBeUndefined();
    },
  );

  it(
    'imports the built canonical UI subpath entry successfully',
    { timeout: 60_000 },
    async () => {
    const runtime = await import(pathToFileURL(DIST_UI_INDEX_PATH).href);

    expect(runtime.mountWalletUI).toBeUndefined();
    expect(runtime.createAccountMenuController).toBeUndefined();
    expect(runtime.installWalletStorageDiskMirror).toBeUndefined();
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
