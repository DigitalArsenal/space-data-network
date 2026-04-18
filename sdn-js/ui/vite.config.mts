import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '..');

export default defineConfig({
  root: __dirname,
  resolve: {
    alias: [
      {
        find: '@sds',
        replacement: path.resolve(packageRoot, 'node_modules/spacedatastandards.org'),
      },
      {
        find: /^\.\/sdn-plugin\.mjs$/,
        replacement: path.resolve(__dirname, 'shims/hd-wallet-sdn-plugin.mjs'),
      },
      {
        find: /^\.\/sdn-plugin-manifest-source\.mjs$/,
        replacement: path.resolve(__dirname, 'shims/hd-wallet-sdn-plugin-manifest-source.mjs'),
      },
    ],
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes('node_modules')) {
            return undefined;
          }
          if (
            id.includes('/libp2p/') ||
            id.includes('/helia/') ||
            id.includes('/@libp2p/') ||
            id.includes('/@chainsafe/') ||
            id.includes('/multiformats/') ||
            id.includes('/@multiformats/')
          ) {
            return 'network';
          }
          return undefined;
        },
      },
    },
  },
});
