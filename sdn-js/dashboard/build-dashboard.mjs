#!/usr/bin/env node
/*
 * Reproducible build of the SDN Node Status $APP homepage.
 *
 *   1. vite + viteSingleFile  →  ONE self-contained dist/index.html
 *      (inline script + inline style; /fonts/*.woff2 + /ws/status stay as
 *      same-origin references the node serves).
 *   2. Compute the sha256 of every inline <script> so the served page runs
 *      under a STRICT CSP (script-src 'self' — no 'unsafe-inline'): the exact
 *      appSurfaceCSP-grade policy Iris set, plus the script hash(es).
 *   3. Emit the artifact + its CSP next to the Go embed:
 *        sdn-server/cmd/spacedatanetwork/embedded/dashboard.html
 *        sdn-server/cmd/spacedatanetwork/embedded/dashboard.csp
 *      Go go:embed's both, so the CSP always matches the shipped bytes.
 *
 * Run: `node dashboard/build-dashboard.mjs` from sdn-js (or npm run build:dashboard).
 */
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const distHtml = path.resolve(__dirname, 'dist/index.html');
const embedDir = path.resolve(__dirname, '../../sdn-server/cmd/spacedatanetwork/embedded');
const outHtml = path.join(embedDir, 'dashboard.html');
const outCsp = path.join(embedDir, 'dashboard.csp');

// --- 1. Build ---------------------------------------------------------------
const viteBin = path.resolve(__dirname, '../node_modules/vite/bin/vite.js');
const build = spawnSync(process.execPath, [viteBin, 'build'], {
  cwd: __dirname,
  stdio: 'inherit'
});
if (build.status !== 0) {
  console.error('[build-dashboard] vite build failed');
  process.exit(build.status ?? 1);
}

// --- 2. Hash the inline scripts --------------------------------------------
const html = fs.readFileSync(distHtml, 'utf8');
const inlineScript = /<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/gi;
const hashes = [];
let m;
while ((m = inlineScript.exec(html)) !== null) {
  const body = m[1];
  if (body.trim().length === 0) continue;
  const digest = createHash('sha256').update(body, 'utf8').digest('base64');
  hashes.push(`'sha256-${digest}'`);
}
if (hashes.length === 0) {
  console.error('[build-dashboard] no inline scripts found — CSP would block nothing to run');
  process.exit(1);
}

// --- 3. Compose the CSP -----------------------------------------------------
// appSurfaceCSP-grade (Iris ruling in graph/tasks/nst-dashboard-serve.md), with
// the built page's inline-script hash(es) so a strict script-src 'self' can run
// the single-file bundle. Fonts/WS/records are same-origin ('self'); wss:/https:
// let the same file connect to a remote node's feed when opened cross-origin.
const csp = [
  "default-src 'self'",
  "base-uri 'none'",
  "object-src 'none'",
  "frame-ancestors 'none'",
  "form-action 'none'",
  `script-src 'self' 'wasm-unsafe-eval' ${hashes.join(' ')}`,
  "worker-src 'self' blob:",
  "connect-src 'self' wss: https:",
  "style-src 'self' 'unsafe-inline'",
  "font-src 'self' data:",
  "img-src 'self' data:"
].join('; ');

// --- 4. Emit ----------------------------------------------------------------
fs.mkdirSync(embedDir, { recursive: true });
fs.writeFileSync(outHtml, html);
fs.writeFileSync(outCsp, csp + '\n');

console.log(`[build-dashboard] wrote ${path.relative(process.cwd(), outHtml)} (${html.length} bytes)`);
console.log(`[build-dashboard] wrote ${path.relative(process.cwd(), outCsp)} (${hashes.length} inline-script hash(es))`);
console.log(`[build-dashboard] CSP: ${csp}`);
