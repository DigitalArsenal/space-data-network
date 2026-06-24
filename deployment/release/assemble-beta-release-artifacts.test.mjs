import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { generateKeyPairSync } from 'node:crypto';
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
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-linux-arm64.tar.gz', 'cli');
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-darwin-amd64.tar.gz', 'cli');
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-darwin-arm64.tar.gz', 'cli');
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-windows-amd64.zip', 'cli');
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
    'spacedatanetwork-1.0.3-beta.42-linux-arm64.tar.gz',
    'spacedatanetwork-1.0.3-beta.42-darwin-amd64.tar.gz',
    'spacedatanetwork-1.0.3-beta.42-darwin-arm64.tar.gz',
    'spacedatanetwork-1.0.3-beta.42-windows-amd64.zip',
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
  assert(manifest.artifacts.some((artifact) => artifact.name === 'spacedatanetwork-1.0.3-beta.42-linux-arm64.tar.gz'));
  assert(manifest.artifacts.some((artifact) => artifact.name === 'spacedatanetwork-1.0.3-beta.42-darwin-amd64.tar.gz'));
  assert(manifest.artifacts.some((artifact) => artifact.name === 'spacedatanetwork-1.0.3-beta.42-darwin-arm64.tar.gz'));
  assert(manifest.artifacts.some((artifact) => artifact.name === 'spacedatanetwork-1.0.3-beta.42-windows-amd64.zip'));

  const releaseBody = readFileSync(join(releaseDir, 'SDN-BETA-RELEASE.md'), 'utf8');
  assert.match(releaseBody, /Space Data Network v1\.0\.3-beta\.42 Beta/);
  assert.match(releaseBody, /spacedatanetwork-sdn-js-2\.0\.12\.tgz/);
  assert.match(releaseBody, /digitalarsenal\/space-data-network:<beta-version>/);
  assert.match(releaseBody, /spacedatanetwork-container-1\.0\.3\.beta\.42-linux-amd64\.tar\.gz/);
  assert.match(releaseBody, /spacedatanetwork-1\.0\.3-beta\.42-linux-amd64\.tar\.gz/);
  assert.match(releaseBody, /spacedatanetwork-1\.0\.3-beta\.42-windows-amd64\.zip/);
  assert.doesNotMatch(releaseBody, /space-data-network-full/);
  assert.doesNotMatch(releaseBody, /space-data-network-edge/);

  const checksums = readFileSync(join(releaseDir, 'spacedatanetwork-checksums.txt'), 'utf8');
  assert.match(checksums, /spacedatanetwork-linux-vm-1\.0\.3\.beta\.42\.tar\.gz/);
  assert.match(checksums, /spacedatanetwork-container-1\.0\.3\.beta\.42-linux-amd64\.tar\.gz/);
  assert.match(checksums, /spacedatanetwork-1\.0\.3-beta\.42-linux-amd64\.tar\.gz/);
  assert.match(checksums, /spacedatanetwork-1\.0\.3-beta\.42-windows-amd64\.zip/);
  assert.doesNotMatch(checksums, /spacedatanetwork-checksums\.txt/);
});

test('fails when a required CLI release artifact is missing', () => {
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-beta-release-missing-cli-'));
  const distDir = join(tempRoot, 'dist');
  const releaseDir = join(distDir, 'release');

  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-linux-amd64.tar.gz', 'cli');
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-linux-arm64.tar.gz', 'cli');
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-darwin-amd64.tar.gz', 'cli');
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-darwin-arm64.tar.gz', 'cli');

  assert.throws(() => {
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
  }, /missing required CLI release artifact: spacedatanetwork-1\.0\.3-beta\.42-windows-amd64\.zip/);
});

test('assembles signed CLI update feed artifacts when signing key is configured', () => {
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-beta-release-feed-'));
  const distDir = join(tempRoot, 'dist');
  const releaseDir = join(distDir, 'release');
  const { privateKey } = generateKeyPairSync('ed25519');
  const privateKeyPem = privateKey.export({ type: 'pkcs8', format: 'pem' });

  for (const artifact of [
    'cli/spacedatanetwork-1.0.3-beta.42-linux-amd64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-linux-arm64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-darwin-amd64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-darwin-arm64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-windows-amd64.zip',
  ]) {
    writeFixture(distDir, artifact, `fixture ${artifact}`);
  }

  execFileSync('bash', [scriptPath], {
    cwd: repoRoot,
    env: {
      ...process.env,
      DIST_DIR: distDir,
      RELEASE_DIR: releaseDir,
      VERSION: '1.0.3-beta.42',
      RELEASE_TAG: 'v1.0.3-beta.42',
      GITHUB_SHA: '0123456789abcdef0123456789abcdef01234567',
      SDN_UPDATE_SIGNING_KEY_PEM: privateKeyPem,
      SDN_UPDATE_KEY_ID: 'sdn-test-key',
      SDN_UPDATE_SEQUENCE: '4242',
      SDN_UPDATE_FEED_GENERATED_AT: '2026-06-22T00:00:00Z'
    },
    stdio: 'pipe'
  });

  for (const target of [
    ['linux', 'amd64'],
    ['linux', 'arm64'],
    ['darwin', 'amd64'],
    ['darwin', 'arm64'],
    ['windows', 'amd64'],
  ]) {
    const [platform, arch] = target;
    const index = JSON.parse(readFileSync(join(releaseDir, 'update-feed', 'cli-bundle', 'beta', platform, arch, 'index.json'), 'utf8'));
    assert.equal(index.schema, 'org.spacedatanetwork.update.index.v1');
    assert.equal(index.updates[0].target.kind, 'cli-bundle');
    assert.equal(index.updates[0].target.platform, platform);
    assert.equal(index.updates[0].target.arch, arch);
    assert.equal(index.updates[0].sequence, 4242);
    assert.match(index.updates[0].manifest_url, new RegExp(`/cli-bundle/beta/${platform}/${arch}/1\\.0\\.3-beta\\.42/manifest\\.json$`));
    assert.match(index.updates[0].carrier_url, new RegExp(`/cli-bundle/beta/${platform}/${arch}/1\\.0\\.3-beta\\.42/update\\.wasm$`));
  }

  assert.doesNotThrow(() => readFileSync(join(releaseDir, 'spacedatanetwork-update-feed-1.0.3-beta.42.tar.gz')));
  const checksums = readFileSync(join(releaseDir, 'spacedatanetwork-checksums.txt'), 'utf8');
  assert.match(checksums, /spacedatanetwork-update-feed-1\.0\.3-beta\.42\.tar\.gz/);
});
