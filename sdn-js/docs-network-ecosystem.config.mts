import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

export default defineConfig({
  build: {
    emptyOutDir: false,
    outDir: path.resolve(__dirname, '../docs'),
    lib: {
      entry: path.resolve(__dirname, 'src/docs/network-ecosystem-demo/index.ts'),
      formats: ['es'],
      fileName: () => 'network-ecosystem-demo.mjs',
    },
    rollupOptions: {
      output: {
        inlineDynamicImports: true,
      },
    },
    minify: true,
    sourcemap: false,
  },
});
