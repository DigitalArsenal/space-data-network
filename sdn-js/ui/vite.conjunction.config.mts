import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

/**
 * CONJUNCTION-only ship build (loop SDN_SPACEAWARE_UI_LOOP.md Phase C, task
 * C1). A dedicated Vite entry — separate from the full-app
 * `vite.spaceaware.config.mts` — whose output is post-processed by
 * scripts/build-conjunction-single-file.mjs into ONE self-contained HTML
 * artifact (all JS/CSS/fonts inlined) that carries ONLY the conjunction
 * experience: the reused `ConjunctionView` + its `lib/conjunction-data*`
 * layers + the shared groups store, wrapped in a minimal header-strip chrome
 * (`ConjunctionApp.svelte`). No console rail, no descoped screens, no login.
 *
 * Inlining knobs mirror the spaceaware build:
 * - assetsInlineLimit: huge → fonts become data: URIs
 * - cssCodeSplit: false + inlineDynamicImports → single CSS + single JS
 *
 * hd-wallet stub (bundle-size guard): this ship keeps NO session flow, so the
 * hd-wallet wasm glue (~5 MB) must never enter the bundle. It is only reachable
 * as a dead static import: `conjunction-data.ts` → `node-data.ts` →
 * `lib/console.ts` → `lib/login.ts` (for `networkStatusFromHealth` /
 * `parseHealthResponse`) → `lib/auth/local-wallet.ts` (for the unused
 * `LocalWalletError` class) → `src/crypto/hd-wallet.ts` → `hd-wallet-wasm`.
 * Rollup can (and here does) tree-shake the unused `LocalWalletError` binding,
 * but to make the exclusion deterministic — and independent of future edits to
 * that shared, dormant chain — the `hd-wallet-wasm` package is aliased to an
 * empty stub for THIS build only. The conjunction app never calls any wallet
 * crypto, so nothing at runtime touches the stub; the single-file build then
 * asserts (a hard audit that FAILS the build) that no wasm blob is embedded.
 */

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

export default defineConfig({
  root: __dirname,
  base: './',
  plugins: [svelte()],
  resolve: {
    alias: [
      {
        find: /^hd-wallet-wasm$/,
        replacement: path.resolve(__dirname, 'shims/hd-wallet-wasm-empty.ts'),
      },
    ],
  },
  server: {
    host: '127.0.0.1',
    port: Number.parseInt(process.env.SDN_CONJUNCTION_UI_PORT ?? '5175', 10),
    open: '/conjunction.html',
  },
  build: {
    outDir: 'dist-conjunction',
    emptyOutDir: true,
    assetsInlineLimit: 100_000_000,
    cssCodeSplit: false,
    modulePreload: false,
    rollupOptions: {
      input: path.resolve(__dirname, 'conjunction.html'),
      output: {
        inlineDynamicImports: true,
      },
    },
  },
});
