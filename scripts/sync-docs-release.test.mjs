import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { advertisedStableVersions, isReleaseTag, releaseLiterals, rewrite } from './sync-docs-release.mjs';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const SCRIPT = resolve(ROOT, 'scripts/sync-docs-release.mjs');
const DL = 'https://github.com/DigitalArsenal/space-data-network/releases/download';

// Every spelling the site uses, plus two numbers that are not ours (WasmEdge
// and a module version) and must never move.
function page(tag) {
  const bare = tag.replace(/^v/, '');
  const dotted = bare.replace('-beta.', '.beta.');
  return [
    `<p class="eyebrow">${tag}</p>`,
    `<a href="${DL}/${tag}/space-data-network-desktop-${bare}-mac-arm64.dmg">`,
    `<a href="${DL}/${tag}/spacedatanetwork-${bare}-darwin-arm64.tar.gz">`,
    `<a href="${DL}/${tag}/spacedatanetwork-container-${dotted}-linux-amd64.tar.gz">`,
    `<pre><code>docker run dockerdigitalarsenal/space-data-network:${tag}</code></pre>`,
    `<code>SDN_VERSION=${tag}</code>`,
    `It describes ${tag}.`,
    'WasmEdge 0.16.4 and module 1.2.0 are not release numbers.',
  ].join('\n');
}

test('a release tag is a beta or a stable vX.Y.Z', () => {
  for (const tag of ['v1.1.0', 'v1.0.5-beta.70', 'v10.20.30']) assert.equal(isReleaseTag(tag), true, tag);
  for (const tag of ['1.1.0', 'v1.1', 'v1.1.0-rc.1', 'v1.1.0-alpha.1', 'v1.1.0-beta', 'latest', '']) {
    assert.equal(isReleaseTag(tag), false, tag);
  }
});

test('a beta site moves to a stable release', () => {
  assert.equal(rewrite(page('v1.0.5-beta.70'), 'v1.1.0'), page('v1.1.0'));
});

test('a stable site moves to the next stable release', () => {
  assert.equal(rewrite(page('v1.1.0'), 'v1.1.1'), page('v1.1.1'));
});

test('a stable site moves to a beta, with the container asset dotted again', () => {
  assert.equal(rewrite(page('v1.1.0'), 'v1.1.1-beta.2'), page('v1.1.1-beta.2'));
});

test('beta to beta is what it always was', () => {
  assert.equal(rewrite(page('v1.0.5-beta.70'), 'v1.0.5-beta.71'), page('v1.0.5-beta.71'));
});

test('a site already on the release is unchanged, so --check passes', () => {
  for (const tag of ['v1.0.5-beta.70', 'v1.1.0']) assert.equal(rewrite(page(tag), tag), page(tag));
});

test('a stable version counts as advertised only where only a release can stand', () => {
  assert.deepEqual(advertisedStableVersions(page('v1.1.0')), ['1.1.0']);
  assert.deepEqual(advertisedStableVersions('WasmEdge 0.16.4, module 1.2.0, Kubo v0.43.1'), []);
  // The same number elsewhere in prose is left alone when no release names it.
  assert.equal(rewrite('module 1.2.0', 'v1.1.0'), 'module 1.2.0');
});

test('the stale report names stable literals too', () => {
  assert.deepEqual(releaseLiterals(page('v1.1.0')).sort(), ['1.1.0', 'v1.1.0']);
  assert.deepEqual(releaseLiterals(page('v1.0.5-beta.70')).sort(), ['1.0.5-beta.70', '1.0.5.beta.70', 'v1.0.5-beta.70']);
});

test('the real pages survive beta -> stable -> stable with no stale literal left', () => {
  for (const rel of ['docs/index.html', 'docs/onboarding.html', 'docs/INSTALL.md']) {
    const text = readFileSync(resolve(ROOT, rel), 'utf8');
    const literals = releaseLiterals(text);
    if (literals.length === 0) continue;
    const stable = rewrite(text, 'v9.8.7');
    assert.doesNotMatch(stable, /\d+\.\d+\.\d+[-.]beta[-.]\d+/, `${rel} kept a beta literal`);
    assert.deepEqual(advertisedStableVersions(stable), stable.includes('/releases/download/') || stable.includes('space-data-network:') ? ['9.8.7'] : []);
    const next = rewrite(stable, 'v9.8.8');
    assert.doesNotMatch(next, /(?<![0-9A-Za-z.])v?9\.8\.7(?![0-9])/, `${rel} kept 9.8.7`);
    assert.equal(rewrite(next, 'v9.8.8'), next, `${rel} is not stable under a repeat sync`);
  }
});

test('the script refuses a tag that is neither beta nor stable, and importing it runs nothing', () => {
  const r = spawnSync(process.execPath, [SCRIPT, '--tag', 'v1.1.0-rc.1'], { cwd: ROOT, encoding: 'utf8' });
  assert.equal(r.status, 1);
  assert.match(r.stderr, /not a release tag: v1\.1\.0-rc\.1/);
});
