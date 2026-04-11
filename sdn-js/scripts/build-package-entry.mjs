import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, '..');

await build({
  absWorkingDir: packageRoot,
  entryPoints: [path.join(packageRoot, 'src/index.ts')],
  outfile: path.join(packageRoot, 'dist/index.mjs'),
  bundle: true,
  format: 'esm',
  platform: 'browser',
  target: 'es2022',
  sourcemap: false,
  logLevel: 'info',
  mainFields: ['browser', 'module', 'main'],
  conditions: ['browser', 'import', 'module'],
  loader: {
    '.wasm': 'file',
  },
});
