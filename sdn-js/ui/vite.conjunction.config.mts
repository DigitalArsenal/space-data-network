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
 * Phase 1A removed the dormant host-side credential and crypto import chain.
 * This build therefore needs no protected-module alias or stub. The
 * single-file build still hard-fails if a wasm blob is ever reintroduced.
 */

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

export default defineConfig({
  root: __dirname,
  base: './',
  plugins: [svelte()],
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
