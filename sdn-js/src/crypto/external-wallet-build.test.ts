/**
 * The externalised-wallet build target must be reproducible from a clean
 * checkout of THIS package.
 *
 * History (graph task `sdn-js-dist-not-reproducible-from-gitlink`): the only
 * artifact that satisfied an embedder's publish guard was produced by that
 * embedder REWRITING `scripts/build-package-entry.mjs` on disk around the
 * build. Nothing committed here could produce it, so `npm run build:core` and
 * the published tarball were both rejected by the guard and no clean checkout
 * could build the Pages artifact. These tests pin the committed target.
 */

import path from 'node:path';
import { mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { build } from 'esbuild';
import { afterEach, describe, expect, it } from 'vitest';

const packageRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const walletBuildModuleUrl = pathToFileURL(
  path.join(packageRoot, 'scripts', 'wallet', 'external-wallet-build.mjs'),
).href;
const adapterModuleUrl = pathToFileURL(
  path.join(packageRoot, 'scripts', 'wallet', 'external-hd-wallet-adapter.mjs'),
).href;

const walletBuild = await import(/* @vite-ignore */ walletBuildModuleUrl);

// Mirrors OrbPro scripts/hd-wallet-runtime-guard.mjs — the guard this target
// exists to satisfy. Kept as literals so a drift in either side shows up here.
const FULL_RUNTIME_MARKERS = [
  'HD Wallet WASM - JavaScript ES6 Wrapper',
  'walletOriginCapabilitiesByModule',
  '_hd_get_supported_curves',
  '_hd_mnemonic_generate',
];
const EMBEDDED_WALLET_RUNTIME_PATTERN =
  /(?:^|[/])(?:node_modules\/)?hd-wallet-wasm\/dist\/hd-wallet\.js|\bHDWalletWasm\s*=\s*\(\s*\(\s*\)\s*=>/m;

function embeddedRuntimeReason(source: string): string | null {
  if (EMBEDDED_WALLET_RUNTIME_PATTERN.test(source)) {
    return 'embedded full runtime';
  }
  for (const marker of FULL_RUNTIME_MARKERS) {
    if (source.includes(marker)) {
      return `full runtime marker ${marker}`;
    }
  }
  return null;
}

async function bundleWalletEntry(plugins: unknown[]): Promise<string> {
  const result = await build({
    absWorkingDir: packageRoot,
    entryPoints: [path.join(packageRoot, 'src/crypto/hd-wallet.ts')],
    bundle: true,
    write: false,
    format: 'esm',
    platform: 'browser',
    target: 'es2022',
    logLevel: 'silent',
    mainFields: ['browser', 'module', 'main'],
    conditions: ['browser', 'import', 'module'],
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    plugins: plugins as any,
  });
  return result.outputFiles[0].text;
}

describe('external wallet build target', () => {
  it('inlines the wallet runtime by default', async () => {
    const source = await bundleWalletEntry([]);
    expect(embeddedRuntimeReason(source)).not.toBeNull();
    expect(source.length).toBeGreaterThan(4_000_000);
  });

  it('externalises the wallet runtime through the committed adapter', async () => {
    const mode = await walletBuild.resolveWalletBuildMode({
      packageRoot,
      argv: ['--external-wallet'],
      env: {},
    });
    expect(mode.external).toBe(true);
    expect(mode.adapterPath).toBe(
      walletBuild.defaultExternalWalletAdapterPath(packageRoot),
    );

    const source = await bundleWalletEntry(mode.plugins);
    expect(embeddedRuntimeReason(source)).toBeNull();
    expect(source.length).toBeLessThan(200_000);
  });

  it('reads the mode from the environment as well as the flag', () => {
    expect(walletBuild.isExternalWalletBuild({ argv: [], env: {} })).toBe(false);
    expect(
      walletBuild.isExternalWalletBuild({ argv: [], env: { SDN_JS_EXTERNAL_WALLET: '1' } }),
    ).toBe(true);
    expect(walletBuild.isExternalWalletBuild({ argv: ['--external-wallet'], env: {} })).toBe(
      true,
    );
  });

  it('lets an embedder supply its own adapter without editing this package', async () => {
    const adapterPath = path.join(packageRoot, 'scripts', 'wallet', 'external-hd-wallet-adapter.mjs');
    const resolved = walletBuild.resolveExternalWalletAdapterPath({
      packageRoot,
      env: { SDN_JS_HD_WALLET_ADAPTER: 'scripts/wallet/external-hd-wallet-adapter.mjs' },
    });
    expect(resolved).toBe(adapterPath);
  });

  it('loads embedder-contributed esbuild plugins from the environment', async () => {
    // Inside the package (an embedder points at a path in its own checkout);
    // node_modules/.cache is ignored by git and reachable by the test runner.
    const cacheRoot = path.join(packageRoot, 'node_modules', '.cache');
    await mkdir(cacheRoot, { recursive: true });
    const directory = await realpath(await mkdtemp(path.join(cacheRoot, 'sdn-js-plugin-')));
    try {
      const modulePath = path.join(directory, 'plugin.mjs');
      await writeFile(
        modulePath,
        'export function createEsbuildPlugin() { return { name: "embedder", setup() {} }; }\n',
      );
      const plugins = await walletBuild.loadExtraEsbuildPlugins({
        packageRoot,
        env: { SDN_JS_ESBUILD_PLUGIN_MODULES: modulePath },
      });
      expect(plugins.map((plugin: { name: string }) => plugin.name)).toEqual(['embedder']);
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });

  it('declares the named build script', async () => {
    const manifest = JSON.parse(
      await readFile(path.join(packageRoot, 'package.json'), 'utf8'),
    );
    expect(manifest.scripts['build:browser-external-wallet']).toBe(
      'SDN_JS_EXTERNAL_WALLET=1 npm run build:core',
    );
  });
});

describe('external wallet adapter', () => {
  afterEach(async () => {
    const adapter = await import(/* @vite-ignore */ adapterModuleUrl);
    adapter.unregisterHdWalletProvider();
    delete (globalThis as Record<string, unknown>)[adapter.HD_WALLET_RUNTIME_URL_GLOBAL];
  });

  it('names the node staging path when no runtime is available', async () => {
    const adapter = await import(/* @vite-ignore */ adapterModuleUrl);
    expect(adapter.DEFAULT_STAGED_RUNTIME_URL).toBe('/wallet-wasm/runtime/index.mjs');
    expect(() => adapter.Curve.ED25519).toThrow(/\/wallet-wasm\/runtime\/index\.mjs/u);
  });

  it('fails closed when the staged runtime cannot be loaded', async () => {
    const adapter = await import(/* @vite-ignore */ adapterModuleUrl);
    (globalThis as Record<string, unknown>)[adapter.HD_WALLET_RUNTIME_URL_GLOBAL] =
      pathToFileURL(path.join(packageRoot, 'scripts', 'wallet', 'does-not-exist.mjs')).href;
    await expect(adapter.createHDWallet()).rejects.toThrow(/does-not-exist\.mjs/u);
  });

  it('uses a host-installed provider without touching the staged runtime', async () => {
    const adapter = await import(/* @vite-ignore */ adapterModuleUrl);
    const wallet = { installed: true };
    adapter.registerHdWalletProvider({
      createHDWallet: async () => wallet,
      Curve: { ED25519: 1 },
      Language: { ENGLISH: 0 },
    });
    expect(adapter.Curve.ED25519).toBe(1);
    expect(adapter.Language.ENGLISH).toBe(0);
    await expect(adapter.createHDWallet()).resolves.toBe(wallet);
  });

  it('refuses a provider that cannot initialize a wallet', async () => {
    const adapter = await import(/* @vite-ignore */ adapterModuleUrl);
    expect(() => adapter.registerHdWalletProvider({ Curve: {} })).toThrow(/createHDWallet/u);
  });
});
