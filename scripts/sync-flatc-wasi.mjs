#!/usr/bin/env node
// Copies the published flatc-wasi.wasm (npm alias "flatc-wasi" ->
// flatc-wasm@<wasm tag>) into sdn-server/internal/wasm, where the node embeds
// it, after checking the package's own sha256 digest. --check compares
// without writing and exits non-zero on drift.
//
//   npm install && npm run sync:flatc-wasi
//
// Then set FlatcWasiPackage / FlatcWasiSHA256 in internal/wasm/flatc.go to
// what this prints; TestEmbeddedFlatcArtifact holds them together.

import { createHash } from 'node:crypto';
import { copyFileSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const pkgDir = join(root, 'node_modules', 'flatc-wasi');
const source = join(pkgDir, 'dist', 'flatc-wasi.wasm');
const target = join(root, 'sdn-server', 'internal', 'wasm', 'flatc-wasi.wasm');
const check = process.argv.includes('--check');

const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');

let pkg;
let bytes;
try {
  pkg = JSON.parse(readFileSync(join(pkgDir, 'package.json'), 'utf8'));
  bytes = readFileSync(source);
} catch (err) {
  console.error(`flatc-wasi is not installed (${err.message}); run npm install at the repo root`);
  process.exit(1);
}

const digest = sha256(bytes);
const published = readFileSync(`${source}.sha256`, 'utf8').trim().split(/\s+/)[0];
if (digest !== published) {
  console.error(`${source}: sha256 ${digest} does not match the package digest ${published}`);
  process.exit(1);
}

const label = `${pkg.name}@${pkg.version}`;
if (check) {
  let embedded = '';
  try {
    embedded = sha256(readFileSync(target));
  } catch {
    embedded = '(missing)';
  }
  if (embedded !== digest) {
    console.error(`embedded flatc-wasi.wasm ${embedded} != ${label} ${digest}; run npm run sync:flatc-wasi`);
    process.exit(1);
  }
  console.log(`embedded flatc-wasi.wasm matches ${label} (${digest})`);
} else {
  copyFileSync(source, target);
  console.log(`copied ${label} dist/flatc-wasi.wasm -> sdn-server/internal/wasm/flatc-wasi.wasm`);
  console.log(`FlatcWasiPackage = "${label}"`);
  console.log(`FlatcWasiSHA256  = "${digest}"`);
}
