import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const betaReleasesUrl = 'https://github.com/DigitalArsenal/space-data-network/releases/tag/v1.0.3-beta.1';
const macosArm64BundleUrl = 'https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-darwin-arm64.tar.gz';
const requiredPhrases = [
  'Beta channel',
  betaReleasesUrl,
  macosArm64BundleUrl,
  'spacedatanetwork-full',
  'spacedatanetwork-edge',
  'spacedatanetwork-linux-vm-',
  'spacedatanetwork-darwin-arm64.tar.gz',
  'spacedatanetwork-sdn-js-',
  'spacedatanetwork-sbom.cdx.json',
  'spacedatanetwork-checksums.txt',
  'ghcr.io/digitalarsenal/space-data-network-full:<beta-version>',
  'ghcr.io/digitalarsenal/space-data-network-edge:<beta-version>'
];
const homepageRequiredPhrases = requiredPhrases.map((phrase) =>
  phrase.replaceAll('<beta-version>', '&lt;beta-version&gt;')
);

function readRepoFile(relativePath) {
  return readFileSync(join(repoRoot, relativePath), 'utf8');
}

test('README exposes beta artifacts and release links', () => {
  const readme = readRepoFile('README.md');

  for (const phrase of requiredPhrases) {
    assert.match(readme, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `README.md missing ${phrase}`);
  }
});

test('website downloads page exposes beta artifacts and release links', () => {
  const homepage = readRepoFile('docs/index.html');

  for (const phrase of homepageRequiredPhrases) {
    assert.match(homepage, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `docs/index.html missing ${phrase}`);
  }
});

test('installation docs show beta curl and package guidance', () => {
  const docs = readRepoFile('docs/docs.html');

  for (const phrase of ['SDN_BETA_VERSION', betaReleasesUrl, 'spacedatanetwork-full', 'spacedatanetwork-linux-vm-', 'spacedatanetwork-sdn-js-']) {
    assert.match(docs, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `docs/docs.html missing ${phrase}`);
  }
});

test('release pipeline docs describe the beta workflow separately from production', () => {
  const releasePipeline = readRepoFile('docs/release-pipeline.md');

  for (const phrase of ['.github/workflows/beta-release-artifacts.yml', 'Beta channel', 'GitHub release', 'make_latest: true', 'spacedatanetwork-beta-manifest.json']) {
    assert.match(releasePipeline, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `docs/release-pipeline.md missing ${phrase}`);
  }
});

test('public docs do not route native artifacts through GitHub latest downloads', () => {
  for (const relativePath of ['README.md', 'docs/index.html', 'docs/docs.html']) {
    const source = readRepoFile(relativePath);

    assert.doesNotMatch(
      source,
      /releases\/latest\/download/,
      `${relativePath} must not use ambiguous GitHub latest native download links`
    );
  }
});
