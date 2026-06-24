import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const betaReleasesUrl = 'https://github.com/DigitalArsenal/space-data-network/releases/tag/v1.0.3-beta.1';
const macosArm64BundleUrl = 'https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-darwin-arm64.tar.gz';
const directReleaseAssetUrls = [
  'https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-full_1.0.3.beta.1_amd64.deb',
  'https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-full-1.0.3.beta.1-1.x86_64.rpm',
  'https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-linux-vm-1.0.3.beta.1.tar.gz',
  'https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-container-1.0.3.beta.1-linux-amd64.tar.gz',
  'https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-sdn-js-2.0.12.tgz',
  'https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-sbom.cdx.json',
  'https://github.com/DigitalArsenal/space-data-network/releases/download/v1.0.3-beta.1/spacedatanetwork-checksums.txt'
];
const requiredPhrases = [
  betaReleasesUrl,
  macosArm64BundleUrl,
  ...directReleaseAssetUrls,
  'spacedatanetwork-full',
  'spacedatanetwork-linux-vm-',
  'spacedatanetwork-container-',
  'spacedatanetwork-darwin-arm64.tar.gz',
  'spacedatanetwork-sdn-js-',
  'spacedatanetwork-sbom.cdx.json',
  'spacedatanetwork-checksums.txt',
  'dockerdigitalarsenal/space-data-network:v1.0.3-beta.1'
];
const homepageRequiredPhrases = requiredPhrases.map((phrase) =>
  phrase.replaceAll('<beta-version>', '&lt;beta-version&gt;')
);
const readmeOnlyRequiredPhrases = [
  'spacedatanetwork-<beta-version>-darwin-arm64.tar.gz',
  'spacedatanetwork-<beta-version>-windows-amd64.zip'
];

function readRepoFile(relativePath) {
  return readFileSync(join(repoRoot, relativePath), 'utf8');
}

test('README exposes beta artifacts and release links', () => {
  const readme = readRepoFile('README.md');

  for (const phrase of requiredPhrases) {
    assert.match(readme, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `README.md missing ${phrase}`);
  }
  for (const phrase of readmeOnlyRequiredPhrases) {
    assert.match(readme, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `README.md missing ${phrase}`);
  }
});

test('website downloads page exposes beta artifacts and release links', () => {
  const homepage = readRepoFile('docs/index.html');

  for (const phrase of homepageRequiredPhrases) {
    assert.match(homepage, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `docs/index.html missing ${phrase}`);
  }
  assert.doesNotMatch(homepage, /Beta channel/);
  assert.doesNotMatch(homepage, /beta-downloads-grid/);
  assert.doesNotMatch(homepage, /space-data-network-full/);
  assert.doesNotMatch(homepage, /space-data-network-edge/);
  assert.doesNotMatch(homepage, /Docker edge-relay image/);
});

test('public website and docs expose one-line installers', () => {
  for (const relativePath of ['README.md', 'docs/index.html', 'docs/docs.html']) {
    const source = readRepoFile(relativePath);

    assert.match(source, /curl -fsSL https:\/\/spacedatanetwork\.org\/install\.sh \| bash/, `${relativePath} missing macOS/Linux installer`);
    assert.match(source, /irm https:\/\/spacedatanetwork\.org\/install\.ps1 \| iex/, `${relativePath} missing Windows PowerShell installer`);
    assert.doesNotMatch(source, /digitalarsenal\.github\.io\/space-data-network/, `${relativePath} must not use old SDN GitHub Pages URLs`);
  }
});

test('website encrypted CA section names private maneuver ephemeris screening', () => {
  const homepage = readRepoFile('docs/index.html');

  assert.match(homepage, /Maneuver Ephemeris/i);
  assert.match(homepage, /without broadcasting/i);
  assert.match(homepage, /competitors/i);
  assert.match(homepage, /planned maneuvers/i);
});

test('website download cards use direct assets and keep architecture details inside links', () => {
  const homepage = readRepoFile('docs/index.html');

  for (const phrase of directReleaseAssetUrls) {
    assert.match(homepage, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `docs/index.html missing direct download ${phrase}`);
  }
  assert.doesNotMatch(homepage, /pkgs\/container/, 'Docker downloads must be direct release assets, not GitHub package pages');
  const lines = homepage.split('\n');
  for (const [index, line] of lines.entries()) {
    if (!line.includes('<div class="download-card')) {
      continue;
    }
    const cardTitleWindow = lines.slice(index, index + 14).join('\n');
    assert.doesNotMatch(
      cardTitleWindow,
      /<h3>[^<]+<\/h3>\s*<p>/,
      `download card near line ${index + 1} must not render subtitle text under its title`
    );
  }
  for (const label of [
    '64-bit Apple Silicon (ARM64)',
    '64-bit Intel/AMD (DEB)',
    '64-bit Intel/AMD (RPM)',
    '64-bit Intel/AMD VM bundle',
    '64-bit Intel/AMD Docker image'
  ]) {
    assert.match(homepage, new RegExp(label.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `docs/index.html missing link label ${label}`);
  }
});

test('installation docs show beta curl and package guidance', () => {
  const docs = readRepoFile('docs/docs.html');

  for (const phrase of ['SDN_VERSION', betaReleasesUrl, 'spacedatanetwork-full', 'spacedatanetwork-linux-vm-', 'spacedatanetwork-container-', 'spacedatanetwork-sdn-js-', 'dockerdigitalarsenal/space-data-network:v1.0.3-beta.1']) {
    assert.match(docs, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `docs/docs.html missing ${phrase}`);
  }
  assert.doesNotMatch(docs, /Beta channel/);
  assert.doesNotMatch(docs, /space-data-network-full/);
  assert.doesNotMatch(docs, /space-data-network-edge/);
  assert.doesNotMatch(docs, /spacedatanetwork-container-full-/);
  assert.doesNotMatch(docs, /spacedatanetwork-container-edge-/);
  assert.doesNotMatch(docs, /npm --prefix/);
});

test('release pipeline docs describe the beta workflow separately from production', () => {
  const releasePipeline = readRepoFile('docs/release-pipeline.md');

  for (const phrase of ['.github/workflows/beta-release-artifacts.yml', 'GitHub release', 'make_latest: true', 'spacedatanetwork-beta-manifest.json', 'dockerdigitalarsenal/space-data-network:<beta-version>']) {
    assert.match(releasePipeline, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `docs/release-pipeline.md missing ${phrase}`);
  }
  assert.doesNotMatch(releasePipeline, /space-data-network-full/);
  assert.doesNotMatch(releasePipeline, /space-data-network-edge/);
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
