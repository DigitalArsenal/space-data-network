// Build the WS9.3 page bundle (esbuild, from inside sdn-js so bare imports
// resolve) and serve it with COOP/COEP headers. Copies the wallet wasm next to
// the bundle so the emscripten loader can fetch it relative to the page.
//
// stdout: SERVE_READY http://127.0.0.1:<port>
import { build } from 'esbuild';
import http from 'node:http';
import { readFileSync, copyFileSync, mkdirSync } from 'node:fs';
import { dirname, join, extname } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const outDir = join(here, 'dist');
mkdirSync(outDir, { recursive: true });

await build({
  entryPoints: [join(here, 'entry.mjs')],
  bundle: true,
  format: 'esm',
  platform: 'browser',
  outfile: join(outDir, 'bundle.js'),
  define: { 'process.env.NODE_ENV': '"production"', global: 'globalThis' },
  loader: { '.wasm': 'file' },
  logLevel: 'silent',
});
copyFileSync(join(here, 'index.html'), join(outDir, 'index.html'));
for (const wasm of ['hd-wallet.wasm', 'hd-wallet-wasi.wasm']) {
  copyFileSync(join(here, '../../node_modules/hd-wallet-wasm/dist', wasm), join(outDir, wasm));
}

const types = {
  '.html': 'text/html',
  '.js': 'text/javascript',
  '.wasm': 'application/wasm',
  '.json': 'application/json',
};
const server = http.createServer((req, res) => {
  const path = req.url.split('?')[0];
  const file = join(outDir, path === '/' ? 'index.html' : path.slice(1));
  try {
    const body = readFileSync(file);
    res.writeHead(200, {
      'content-type': types[extname(file)] ?? 'application/octet-stream',
      'cross-origin-opener-policy': 'same-origin',
      'cross-origin-embedder-policy': 'require-corp',
    });
    res.end(body);
  } catch {
    res.writeHead(404);
    res.end('not found');
  }
});
server.listen(0, '127.0.0.1', () => {
  console.log(`SERVE_READY http://127.0.0.1:${server.address().port}`);
});
