import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

function readRepoFile(relativePath) {
  return readFileSync(join(repoRoot, relativePath), 'utf8');
}

function assertContainsAll(relativePath, phrases) {
  const source = readRepoFile(relativePath);
  for (const phrase of phrases) {
    assert.match(
      source,
      new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')),
      `${relativePath} missing ${phrase}`
    );
  }
}

test('public install docs use spacedatanetwork.org one-liners without GitHub CLI or old Pages URLs', () => {
  for (const relativePath of ['README.md', 'docs/index.html', 'docs/docs.html']) {
    const source = readRepoFile(relativePath);

    assert.match(source, /curl -fsSL https:\/\/spacedatanetwork\.org\/install\.sh \| bash/);
    assert.match(source, /irm https:\/\/spacedatanetwork\.org\/install\.ps1 \| iex/);
    assert.doesNotMatch(source, /\bgh\b/, `${relativePath} must not require GitHub CLI`);
    assert.doesNotMatch(source, /digitalarsenal\.github\.io\/space-data-network/);
    assert.doesNotMatch(source, /older builds?/i);
  }
});

test('README and full docs describe CLI lifecycle, remove, search, identity, and update parity', () => {
  const requiredPhrases = [
    'spacedatanetwork start',
    'spacedatanetwork stop',
    'spacedatanetwork restart',
    'spacedatanetwork service status',
    'spacedatanetwork service install',
    'spacedatanetwork service uninstall',
    'spacedatanetwork remove --dry-run',
    'spacedatanetwork remove --purge-data',
    'spacedatanetwork search providers celestrak --schema OMM',
    'spacedatanetwork search standards OMM --format json',
    'spacedatanetwork search data --schema CAT --provider-id space-data-network-02 --format csv',
    'spacedatanetwork identity wizard',
    'spacedatanetwork identity export --format flatbuffer --output epm.fbs',
    'spacedatanetwork identity export --format qrcode',
    'text, JSON, CSV, FlatBuffer, and QR code',
    'spacedatanetwork update check',
    'spacedatanetwork update stage',
    'spacedatanetwork update apply',
    'sdn.spaceaware.io/api/v1/updates'
  ];

  assertContainsAll('README.md', requiredPhrases);
  assertContainsAll('docs/docs.html', requiredPhrases);
});

test('website and docs call out encrypted maneuver ephemeris without broadcasting maneuver intent', () => {
  for (const relativePath of ['README.md', 'docs/index.html', 'docs/docs.html']) {
    const source = readRepoFile(relativePath);

    assert.match(source, /encrypted conjunction assessment/i, `${relativePath} missing encrypted CA`);
    assert.match(source, /maneuver ephemeris/i, `${relativePath} missing maneuver ephemeris`);
    assert.match(source, /without broadcasting/i, `${relativePath} missing non-broadcast language`);
    assert.match(source, /competitors/i, `${relativePath} missing competitor disclosure language`);
  }
});

test('release pipeline docs describe Desktop artifacts and public live-DHT proof gates', () => {
  assertContainsAll('docs/release-pipeline.md', [
    'space-data-network-desktop-<desktop-version>-mac.dmg',
    'space-data-network-desktop-setup-<desktop-version>-windows-x64.exe',
    'space-data-network-desktop-<desktop-version>-linux-x86_64.AppImage',
    'public IPFS Kademlia DHT',
    '300 seconds',
    'Linux Docker',
    'macOS',
    'Windows',
    'peer discovery',
    'identity exchange',
    'provider search',
    'data search',
    'retrieval/query'
  ]);
});

test('public docs do not use stale release timing or legacy migration framing', () => {
  for (const relativePath of ['README.md', 'docs/index.html', 'docs/docs.html']) {
    const source = readRepoFile(relativePath);

    assert.doesNotMatch(source, /old broker/i, `${relativePath} must not mention old broker flows`);
    assert.doesNotMatch(source, /legacy broker/i, `${relativePath} must not mention legacy broker flows`);
    assert.doesNotMatch(source, /legacy .*path/i, `${relativePath} must not frame first-version docs as legacy paths`);
    assert.doesNotMatch(source, /shipping .* by end of/i, `${relativePath} must not contain stale planned-release timing`);
    assert.doesNotMatch(source, /by end of February 2026/i, `${relativePath} must not contain stale February 2026 timing`);
  }
});

test('root package exposes a release parity test command', () => {
  const pkg = JSON.parse(readRepoFile('package.json'));

  assert.equal(pkg.scripts['test:release'], 'node --test deployment/release/*.test.mjs');
});
