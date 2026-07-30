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

// --- 0. Owner-ruled copy, BEFORE the build ----------------------------------
// Scans the design source. Fails on a new offender, or on a change in the count
// of a recorded one, naming both sides. See check-owner-ruled-copy.mjs for why
// this is a build step and not a manual one.
function guard(mode) {
  const r = spawnSync(process.execPath, [path.join(__dirname, 'check-owner-ruled-copy.mjs'), mode], {
    cwd: __dirname,
    stdio: 'inherit'
  });
  if (r.status !== 0) process.exit(r.status ?? 1);
}
guard('--sources');

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

// The import map is an INLINE script too (contract §11.4): hd-wallet-ui's
// modules import the bare specifier "hd-wallet-wasm" and only the map can
// resolve it. Its hash is in `hashes` above by construction — this asserts it,
// because a map dropped by a future bundler change would break sign-in at
// runtime with a CSP refusal and nothing else.
const importMap = /<script[^>]*type=["']importmap["'][^>]*>([\s\S]*?)<\/script>/i.exec(html);
if (!importMap) {
  console.error('[build-dashboard] the hd-wallet-wasm import map is missing from the built page');
  process.exit(1);
}
// A cache-busting query (?v=2.0.29 on a restage) is fine; an absolute URL is
// not. The point of the check is the ORIGIN, not the exact string.
if (!/"hd-wallet-wasm"\s*:\s*"\/wallet-wasm\/runtime\/index\.mjs(\?[^"]*)?"/.test(importMap[1])) {
  console.error('[build-dashboard] the import map must resolve hd-wallet-wasm to this node, not an external origin');
  process.exit(1);
}
const mapHash = `'sha256-${createHash('sha256').update(importMap[1], 'utf8').digest('base64')}'`;
if (!hashes.includes(mapHash)) {
  console.error('[build-dashboard] the import map hash is not in script-src — it would be blocked');
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

// Buffer.byteLength, NOT html.length: a JS string's length counts UTF-16 code
// units, and this page is full of multi-byte characters (· › ‹ … ⚙ ◉ ◍), so
// html.length under-reports the artifact by ~216 bytes. Byte provenance is the
// discipline this whole surface is verified with — a build step that prints a
// byte count must print the real one, or it sends the next agent hunting a
// phantom delta between "what the build said" and what the node serves.
console.log(
  `[build-dashboard] wrote ${path.relative(process.cwd(), outHtml)} (${Buffer.byteLength(html, 'utf8')} bytes,` +
    ` sha256 ${createHash('sha256').update(html, 'utf8').digest('hex')})`
);
console.log(`[build-dashboard] wrote ${path.relative(process.cwd(), outCsp)} (${hashes.length} inline-script hash(es))`);
console.log(`[build-dashboard] CSP: ${csp}`);

// --- 5. Owner-ruled copy, AFTER the build -----------------------------------
// The source scan can pass while the artifact still carries the copy — via a
// consumer-side rule, an inlined vendor chunk, or a path the scan does not
// cover. This is the scope that actually proves it did not reach the user, so
// it runs on the emitted bytes, last.
guard('--artifact');
