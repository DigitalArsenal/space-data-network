import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const scriptPath = join(repoRoot, 'deployment/release/assemble-beta-release-artifacts.sh');

function writeFixture(root, relativePath, contents) {
  const filePath = join(root, relativePath);
  mkdirSync(dirname(filePath), { recursive: true });
  writeFileSync(filePath, contents);
}

test('assembles beta release files, manifest, body, and checksums', () => {
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-beta-release-'));
  const distDir = join(tempRoot, 'dist');
  const releaseDir = join(distDir, 'release');

  writeFixture(distDir, 'packages/spacedatanetwork-full_1.0.3~beta.42_amd64.deb', 'full deb');
  writeFixture(distDir, 'packages/spacedatanetwork-edge_1.0.3~beta.42_amd64.rpm', 'edge rpm');
  writeFixture(distDir, 'linux-vm/spacedatanetwork-linux-vm-1.0.3~beta.42.tar.gz', 'linux vm');
  writeFixture(distDir, 'container-images/spacedatanetwork-container-1.0.3~beta.42-linux-amd64.tar.gz', 'container');
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-linux-amd64.tar.gz', 'cli');
  writeFixture(distDir, 'sdn-js/spacedatanetwork-sdn-js-2.0.12.tgz', 'sdn js');
  writeFixture(distDir, 'sbom/spacedatanetwork-sbom.cdx.json', '{"bomFormat":"CycloneDX"}');
  writeFixture(distDir, 'ipfs/ipfs-deployment.json', '{"cid":"bafyfixture"}');
  writeFixture(distDir, 'container-digests.json', '{"images":[]}');

  execFileSync('bash', [scriptPath], {
    cwd: repoRoot,
    env: {
      ...process.env,
      DIST_DIR: distDir,
      RELEASE_DIR: releaseDir,
      VERSION: '1.0.3-beta.42',
      RELEASE_TAG: 'v1.0.3-beta.42',
      GITHUB_SHA: '0123456789abcdef0123456789abcdef01234567'
    },
    stdio: 'pipe'
  });

  const releasedFiles = [
    'spacedatanetwork-full_1.0.3.beta.42_amd64.deb',
    'spacedatanetwork-edge_1.0.3.beta.42_amd64.rpm',
    'spacedatanetwork-linux-vm-1.0.3.beta.42.tar.gz',
    'spacedatanetwork-container-1.0.3.beta.42-linux-amd64.tar.gz',
    'spacedatanetwork-1.0.3-beta.42-linux-amd64.tar.gz',
    'spacedatanetwork-sdn-js-2.0.12.tgz',
    'spacedatanetwork-sbom.cdx.json',
    'ipfs-deployment.json',
    'container-digests.json',
    'spacedatanetwork-beta-manifest.json',
    'spacedatanetwork-checksums.txt',
    'SDN-BETA-RELEASE.md'
  ];

  for (const fileName of releasedFiles) {
    assert.doesNotThrow(() => readFileSync(join(releaseDir, fileName)), `${fileName} should be released`);
  }

  const manifest = JSON.parse(readFileSync(join(releaseDir, 'spacedatanetwork-beta-manifest.json'), 'utf8'));
  assert.equal(manifest.releaseTag, 'v1.0.3-beta.42');
  assert.equal(manifest.version, '1.0.3-beta.42');
  assert.equal(manifest.channel, 'beta');
  assert.equal(manifest.commit, '0123456789abcdef0123456789abcdef01234567');
  assert(manifest.artifacts.some((artifact) => artifact.name === 'spacedatanetwork-linux-vm-1.0.3.beta.42.tar.gz'));
  assert(manifest.artifacts.some((artifact) => artifact.name === 'spacedatanetwork-container-1.0.3.beta.42-linux-amd64.tar.gz'));
  assert(manifest.artifacts.some((artifact) => artifact.name === 'spacedatanetwork-1.0.3-beta.42-linux-amd64.tar.gz'));

  const releaseBody = readFileSync(join(releaseDir, 'SDN-BETA-RELEASE.md'), 'utf8');
  assert.match(releaseBody, /Space Data Network v1\.0\.3-beta\.42 Beta/);
  assert.match(releaseBody, /spacedatanetwork-sdn-js-2\.0\.12\.tgz/);
  assert.match(releaseBody, /digitalarsenal\/space-data-network:<beta-version>/);
  assert.match(releaseBody, /spacedatanetwork-container-1\.0\.3\.beta\.42-linux-amd64\.tar\.gz/);
  assert.match(releaseBody, /spacedatanetwork-1\.0\.3-beta\.42-linux-amd64\.tar\.gz/);
  assert.doesNotMatch(releaseBody, /space-data-network-full/);
  assert.doesNotMatch(releaseBody, /space-data-network-edge/);

  const checksums = readFileSync(join(releaseDir, 'spacedatanetwork-checksums.txt'), 'utf8');
  assert.match(checksums, /spacedatanetwork-linux-vm-1\.0\.3\.beta\.42\.tar\.gz/);
  assert.match(checksums, /spacedatanetwork-container-1\.0\.3\.beta\.42-linux-amd64\.tar\.gz/);
  assert.match(checksums, /spacedatanetwork-1\.0\.3-beta\.42-linux-amd64\.tar\.gz/);
  assert.doesNotMatch(checksums, /spacedatanetwork-checksums\.txt/);
});
