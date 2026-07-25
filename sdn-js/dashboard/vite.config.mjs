import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { viteSingleFile } from 'vite-plugin-singlefile';
import path from 'node:path';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

/**
 * The design repo is consumed via the `spaceaware-student-sdn` file: dependency
 * (declared in sdn-js/package.json). Resolve it through its real path so vite
 * compiles the .svelte SOURCE (not a pre-bundled node_modules dep) — ZERO edits
 * to the design tree (ZIP-SYNC LAW), import-only.
 */
const designRoot = fs.realpathSync(
  path.resolve(__dirname, '../node_modules/spaceaware-student-sdn')
);

/**
 * The sdn-js status client (createNodeStatusClient) is one module carrying both
 * the REMOTE (WebSocket /ws/status) and the local HELIA assembly paths. The
 * dashboard uses REMOTE mode only; the helia path's dynamic imports
 * (edge-discovery → hd-wallet crypto, epm-resolver → libp2p) are never reached
 * at runtime. Stub them so the single-file homepage bundle stays lean instead of
 * inlining the entire libp2p/crypto graph it never executes.
 */
function stubHeliaOnlyDeps() {
  const VIRTUAL = '\0sdn-dashboard:helia-stub';
  return {
    name: 'sdn-dashboard-stub-helia-only-deps',
    enforce: 'pre',
    resolveId(id) {
      if (/(?:^|\/)(edge-discovery|epm-resolver)(?:\.[tj]s)?$/.test(id)) return VIRTUAL;
      return null;
    },
    load(id) {
      if (id === VIRTUAL) {
        return [
          'export const DEFAULT_EDGE_RELAYS = [];',
          'export const REGIONAL_FALLBACK_RELAYS = {};',
          'export function createEPMResolver() {',
          '  return { setNode() {}, resolveByPeerID: async () => null };',
          '}',
          'export default {};'
        ].join('\n');
      }
      return null;
    }
  };
}

export default defineConfig({
  root: __dirname,
  plugins: [stubHeliaOnlyDeps(), svelte(), viteSingleFile()],
  resolve: {
    alias: { 'spaceaware-student-sdn': designRoot },
    dedupe: ['svelte']
  },
  optimizeDeps: { exclude: ['spaceaware-student-sdn'] },
  build: {
    target: 'es2022',
    outDir: path.resolve(__dirname, 'dist'),
    emptyOutDir: true,
    rollupOptions: {
      input: path.resolve(__dirname, 'index.html'),
      output: { entryFileNames: 'index.js' }
    }
  }
});
