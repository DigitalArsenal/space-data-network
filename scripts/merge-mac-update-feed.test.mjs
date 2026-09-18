import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import { filesBlock, mergeFeeds, urls } from './merge-mac-update-feed.mjs';

const SCRIPT = resolve(dirname(fileURLToPath(import.meta.url)), 'merge-mac-update-feed.mjs');

// Verbatim shape of what electron-builder 26 wrote for v1.0.5-beta.69, which
// is the release that shipped the x64-only feed this script exists to prevent.
const X64 = `version: 1.0.5-beta.69
files:
  - url: space-data-network-desktop-1.0.5-beta.69-mac-x64.zip
    sha512: wl4Ytbzvk8mjniDOHC7rZO9fMWyGwbgUxPrC/W35Qir=
    size: 273761137
  - url: space-data-network-desktop-1.0.5-beta.69-mac-x64.dmg
    sha512: hm/IxE0YwgOHeTPtjwj8gipdnpnR4vX8uK0qVGa87XY=
    size: 283084387
path: space-data-network-desktop-1.0.5-beta.69-mac-x64.zip
sha512: wl4Ytbzvk8mjniDOHC7rZO9fMWyGwbgUxPrC/W35Qir=
releaseDate: '2026-09-18T15:50:35.986Z'
`;

const ARM64 = `version: 1.0.5-beta.69
files:
  - url: space-data-network-desktop-1.0.5-beta.69-mac-arm64.zip
    sha512: AAA0Ytbzvk8mjniDOHC7rZO9fMWyGwbgUxPrC/W35Qir=
    size: 258060594
  - url: space-data-network-desktop-1.0.5-beta.69-mac-arm64.dmg
    sha512: BBB/IxE0YwgOHeTPtjwj8gipdnpnR4vX8uK0qVGa87XY=
    size: 267364118
path: space-data-network-desktop-1.0.5-beta.69-mac-arm64.zip
sha512: AAA0Ytbzvk8mjniDOHC7rZO9fMWyGwbgUxPrC/W35Qir=
releaseDate: '2026-09-18T15:49:02.000Z'
`;

test('the merged feed lists every file from both architectures', () => {
  const merged = mergeFeeds(X64, [ARM64]);
  assert.deepEqual(urls(merged).sort(), [...urls(X64), ...urls(ARM64)].sort());
});

test('an arm64 entry is present, which is what electron-updater filters on', () => {
  const merged = mergeFeeds(X64, [ARM64]);
  assert.ok(urls(merged).some((url) => url.includes('arm64')));
});

test('the legacy top-level path stays on the x64 build', () => {
  const merged = mergeFeeds(X64, [ARM64]);
  assert.match(merged, /^path: space-data-network-desktop-1\.0\.5-beta\.69-mac-x64\.zip$/m);
  assert.doesNotMatch(merged, /^path: .*arm64/m);
});

test('each file keeps its own sha512 and size', () => {
  const merged = mergeFeeds(X64, [ARM64]);
  assert.match(merged, /mac-arm64\.zip\n\s+sha512: AAA0Ytbzvk8mjniDOHC7rZO9fMWyGwbgUxPrC\/W35Qir=\n\s+size: 258060594/);
  assert.match(merged, /mac-x64\.dmg\n\s+sha512: hm\/IxE0YwgOHeTPtjwj8gipdnpnR4vX8uK0qVGa87XY=\n\s+size: 283084387/);
});

test('merging is idempotent — a feed that already has both arches is unchanged', () => {
  const merged = mergeFeeds(X64, [ARM64]);
  assert.equal(mergeFeeds(merged, [ARM64]), merged);
});

test('filesBlock stops at the next top-level key', () => {
  const block = filesBlock(X64);
  assert.equal(block.length, 6);
  assert.ok(block.every((line) => /^\s/.test(line)));
});

test('the CLI writes latest-mac.yml, removes the per-arch inputs, and reports', () => {
  const dir = mkdtempSync(join(tmpdir(), 'macfeed-'));
  writeFileSync(join(dir, 'latest-mac-x64.yml'), X64);
  writeFileSync(join(dir, 'latest-mac-arm64.yml'), ARM64);
  const result = spawnSync(process.execPath, [SCRIPT, dir], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(readdirSync(dir), ['latest-mac.yml']);
  assert.equal(urls(readFileSync(join(dir, 'latest-mac.yml'), 'utf8')).length, 4);
});

test('an x64-only set fails rather than shipping the beta.69 feed again', () => {
  const dir = mkdtempSync(join(tmpdir(), 'macfeed-'));
  writeFileSync(join(dir, 'latest-mac-x64.yml'), X64);
  const result = spawnSync(process.execPath, [SCRIPT, dir], { encoding: 'utf8' });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /no arm64 file/);
});

test('no channel file at all is a failure, not a quiet no-op', () => {
  const dir = mkdtempSync(join(tmpdir(), 'macfeed-'));
  const result = spawnSync(process.execPath, [SCRIPT, dir], { encoding: 'utf8' });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /would ship no macOS update feed/);
});

test('an already-merged feed with no per-arch parts is left alone', () => {
  const dir = mkdtempSync(join(tmpdir(), 'macfeed-'));
  const merged = mergeFeeds(X64, [ARM64]);
  writeFileSync(join(dir, 'latest-mac.yml'), merged);
  const result = spawnSync(process.execPath, [SCRIPT, dir], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(join(dir, 'latest-mac.yml'), 'utf8'), merged);
});
