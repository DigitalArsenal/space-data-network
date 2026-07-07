import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';

/**
 * SpaceAware UI build (loop SDN_SPACEAWARE_UI_LOOP.md, packaging hard rule):
 * a dedicated Vite entry whose output is post-processed by
 * scripts/build-spaceaware-single-file.mjs into ONE self-contained HTML
 * artifact (all JS/CSS/fonts inlined) that is embedded into the sdn-server
 * binary (cmd/spacedatanetwork/spaceaware_ui.go) and served from memory.
 *
 * Everything is therefore forced inline here:
 * - assetsInlineLimit: huge → fonts and images become data: URIs
 * - cssCodeSplit: false + inlineDynamicImports → single CSS + single JS
 */

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

export default defineConfig({
  root: __dirname,
  base: './',
  plugins: [svelte()],
  server: {
    host: '127.0.0.1',
    port: Number.parseInt(process.env.SDN_SPACEAWARE_UI_PORT ?? '5174', 10),
    open: '/spaceaware.html',
  },
  build: {
    outDir: 'dist-spaceaware',
    emptyOutDir: true,
    assetsInlineLimit: 100_000_000,
    cssCodeSplit: false,
    modulePreload: false,
    rollupOptions: {
      input: path.resolve(__dirname, 'spaceaware.html'),
      output: {
        inlineDynamicImports: true,
      },
    },
  },
});
