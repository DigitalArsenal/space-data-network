import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { generateKeyPairSync } from 'node:crypto';
import { existsSync, mkdtempSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
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
  writeFixture(distDir, 'desktop/space-data-network-desktop-0.47.0-mac-arm64.dmg', 'desktop dmg');
  writeFixture(distDir, 'desktop/space-data-network-desktop-0.47.0-mac-arm64.zip', 'desktop mac zip');
  writeFixture(distDir, 'desktop/space-data-network-desktop-setup-0.47.0-windows-x64.exe', 'desktop windows installer');
  writeFixture(distDir, 'desktop/space-data-network-desktop-portable-0.47.0-windows-x64.exe', 'desktop windows portable');
  writeFixture(distDir, 'desktop/space-data-network-desktop-0.47.0-linux-x86_64.AppImage', 'desktop appimage');
  writeFixture(distDir, 'desktop/space-data-network-desktop-0.47.0-linux-amd64.deb', 'desktop deb');
  writeFixture(distDir, 'desktop/space-data-network-desktop-0.47.0-linux-x64.tar.xz', 'desktop linux tarball');
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
    'space-data-network-desktop-0.47.0-mac-arm64.dmg',
    'space-data-network-desktop-0.47.0-mac-arm64.zip',
    'space-data-network-desktop-setup-0.47.0-windows-x64.exe',
    'space-data-network-desktop-portable-0.47.0-windows-x64.exe',
    'space-data-network-desktop-0.47.0-linux-x86_64.AppImage',
    'space-data-network-desktop-0.47.0-linux-amd64.deb',
    'space-data-network-desktop-0.47.0-linux-x64.tar.xz',
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
  assert(manifest.artifacts.some((artifact) => artifact.name === 'space-data-network-desktop-0.47.0-mac-arm64.dmg'));
  assert(manifest.artifacts.some((artifact) => artifact.name === 'space-data-network-desktop-setup-0.47.0-windows-x64.exe'));
  assert(manifest.artifacts.some((artifact) => artifact.name === 'space-data-network-desktop-0.47.0-linux-x86_64.AppImage'));

  const releaseBody = readFileSync(join(releaseDir, 'SDN-BETA-RELEASE.md'), 'utf8');
  assert.match(releaseBody, /Space Data Network v1\.0\.3-beta\.42 Beta/);
  assert.match(releaseBody, /spacedatanetwork-sdn-js-2\.0\.12\.tgz/);
  assert.match(releaseBody, /digitalarsenal\/space-data-network:<beta-version>/);
  assert.match(releaseBody, /spacedatanetwork-container-1\.0\.3\.beta\.42-linux-amd64\.tar\.gz/);
  assert.match(releaseBody, /spacedatanetwork-1\.0\.3-beta\.42-linux-amd64\.tar\.gz/);
  assert.match(releaseBody, /spacedatanetwork-1\.0\.3-beta\.42-windows-amd64\.zip/);
  assert.match(releaseBody, /space-data-network-desktop-0\.47\.0-mac-arm64\.dmg/);
  assert.match(releaseBody, /space-data-network-desktop-setup-0\.47\.0-windows-x64\.exe/);
  assert.match(releaseBody, /space-data-network-desktop-0\.47\.0-linux-x86_64\.AppImage/);
  assert.doesNotMatch(releaseBody, /space-data-network-full/);
  assert.doesNotMatch(releaseBody, /space-data-network-edge/);

  const checksums = readFileSync(join(releaseDir, 'spacedatanetwork-checksums.txt'), 'utf8');
  assert.match(checksums, /spacedatanetwork-linux-vm-1\.0\.3\.beta\.42\.tar\.gz/);
  assert.match(checksums, /spacedatanetwork-container-1\.0\.3\.beta\.42-linux-amd64\.tar\.gz/);
  assert.match(checksums, /spacedatanetwork-1\.0\.3-beta\.42-linux-amd64\.tar\.gz/);
  assert.match(checksums, /spacedatanetwork-1\.0\.3-beta\.42-windows-amd64\.zip/);
  assert.match(checksums, /space-data-network-desktop-0\.47\.0-mac-arm64\.dmg/);
  assert.match(checksums, /space-data-network-desktop-setup-0\.47\.0-windows-x64\.exe/);
  assert.match(checksums, /space-data-network-desktop-0\.47\.0-linux-x86_64\.AppImage/);
  assert.doesNotMatch(checksums, /spacedatanetwork-checksums\.txt/);
});

function writeStableFixtures(distDir) {
  for (const artifact of [
    'cli/spacedatanetwork-1.1.0-linux-amd64.tar.gz',
    'cli/spacedatanetwork-1.1.0-linux-arm64.tar.gz',
    'cli/spacedatanetwork-1.1.0-darwin-amd64.tar.gz',
    'cli/spacedatanetwork-1.1.0-darwin-arm64.tar.gz',
    'cli/spacedatanetwork-1.1.0-windows-amd64.zip',
    'desktop/space-data-network-desktop-1.1.0-mac-arm64.dmg',
    'desktop/space-data-network-desktop-1.1.0-mac-arm64.zip',
    'desktop/space-data-network-desktop-1.1.0-linux-x86_64.AppImage',
    'desktop/space-data-network-desktop-1.1.0-linux-amd64.deb',
    'desktop/space-data-network-desktop-1.1.0-linux-x64.tar.xz',
    'container-images/spacedatanetwork-container-1.1.0-linux-amd64.tar.gz'
  ]) {
    writeFixture(distDir, artifact, 'fixture');
  }
}

test('a stable release is worded as a release, not a beta', () => {
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-stable-release-'));
  const distDir = join(tempRoot, 'dist');
  const releaseDir = join(distDir, 'release');
  writeStableFixtures(distDir);

  execFileSync('bash', [scriptPath], {
    cwd: repoRoot,
    env: {
      ...process.env,
      DIST_DIR: distDir,
      RELEASE_DIR: releaseDir,
      VERSION: '1.1.0',
      RELEASE_TAG: 'v1.1.0',
      RELEASE_MATURITY: 'stable',
      GITHUB_SHA: '0123456789abcdef0123456789abcdef01234567'
    },
    stdio: 'pipe'
  });

  const notes = readFileSync(join(releaseDir, 'SDN-BETA-RELEASE.md'), 'utf8');
  assert.match(notes, /^# Space Data Network v1\.1\.0\n/);
  assert.match(notes, /This is a stable release\./);
  assert.match(notes, /`dockerdigitalarsenal\/space-data-network:v1\.1\.0`/);
  assert.doesNotMatch(notes, /beta/i);

  const manifest = JSON.parse(readFileSync(join(releaseDir, 'spacedatanetwork-beta-manifest.json'), 'utf8'));
  assert.equal(manifest.releaseTag, 'v1.1.0');
  assert.equal(manifest.channel, 'stable');
});

test('MACOS_DESKTOP=false ships without the macOS desktop app and says so', () => {
  // Owner decision 2026-11-14, "Forget Apple desktop for now": v1.1.0 ships
  // without it while Apple refuses notarization; the macOS CLI still ships.
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-no-mac-desktop-'));
  const distDir = join(tempRoot, 'dist');
  const releaseDir = join(distDir, 'release');
  for (const artifact of [
    'cli/spacedatanetwork-1.1.0-linux-amd64.tar.gz',
    'cli/spacedatanetwork-1.1.0-linux-arm64.tar.gz',
    'cli/spacedatanetwork-1.1.0-darwin-amd64.tar.gz',
    'cli/spacedatanetwork-1.1.0-darwin-arm64.tar.gz',
    'cli/spacedatanetwork-1.1.0-windows-amd64.zip',
    'desktop/space-data-network-desktop-1.1.0-linux-x86_64.AppImage',
    'desktop/space-data-network-desktop-1.1.0-linux-amd64.deb',
    'desktop/space-data-network-desktop-1.1.0-linux-x64.tar.xz',
    'desktop/space-data-network-desktop-1.1.0-win-x64.exe',
    'desktop/space-data-network-desktop-portable-1.1.0-win-x64.exe'
  ]) {
    writeFixture(distDir, artifact, 'fixture');
  }
  const env = {
    ...process.env,
    DIST_DIR: distDir,
    RELEASE_DIR: releaseDir,
    VERSION: '1.1.0',
    RELEASE_TAG: 'v1.1.0',
    RELEASE_MATURITY: 'stable',
    GITHUB_SHA: '0123456789abcdef0123456789abcdef01234567'
  };

  // Default: the macOS desktop app is still required.
  assert.throws(
    () => execFileSync('bash', [scriptPath], { cwd: repoRoot, env, stdio: 'pipe' }),
    /missing required desktop release artifact matching: space-data-network-desktop-\*-mac-\*\.dmg/
  );

  execFileSync('bash', [scriptPath], { cwd: repoRoot, env: { ...env, MACOS_DESKTOP: 'false' }, stdio: 'pipe' });
  const notes = readFileSync(join(releaseDir, 'SDN-BETA-RELEASE.md'), 'utf8');
  assert.match(notes, /## Not in this release[\s\S]*space-data-network-desktop-\*-mac-\*\.dmg[\s\S]*space-data-network-desktop-\*-mac-\*\.zip/);
  assert.match(notes, /spacedatanetwork-1\.1\.0-darwin-arm64\.tar\.gz/);
  assert.match(notes, /spacedatanetwork-1\.1\.0-darwin-amd64\.tar\.gz/);
});

test('a release maturity other than beta or stable is refused', () => {
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-stable-release-bad-maturity-'));
  const distDir = join(tempRoot, 'dist');
  writeStableFixtures(distDir);

  assert.throws(() => {
    execFileSync('bash', [scriptPath], {
      cwd: repoRoot,
      env: {
        ...process.env,
        DIST_DIR: distDir,
        RELEASE_DIR: join(distDir, 'release'),
        VERSION: '1.1.0',
        RELEASE_TAG: 'v1.1.0',
        RELEASE_MATURITY: 'release',
        GITHUB_SHA: '0123456789abcdef0123456789abcdef01234567'
      },
      stdio: 'pipe'
    });
  }, /RELEASE_MATURITY must be beta or stable; received release/);
});

test('fails when a PROVEN-platform CLI artifact is missing', () => {
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-beta-release-missing-cli-'));
  const distDir = join(tempRoot, 'dist');
  const releaseDir = join(distDir, 'release');

  // linux-amd64 deliberately absent: a release without it is broken.
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-linux-arm64.tar.gz', 'cli');
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-darwin-amd64.tar.gz', 'cli');
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-darwin-arm64.tar.gz', 'cli');
  writeFixture(distDir, 'cli/spacedatanetwork-1.0.3-beta.42-windows-amd64.zip', 'cli');

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
  }, /missing required CLI release artifact: spacedatanetwork-1\.0\.3-beta\.42-linux-amd64\.tar\.gz/);
});

test('publishes without the Windows archive, and says it is absent', () => {
  // Windows is experimental everywhere else in the pipeline
  // (matrix.experimental + continue-on-error). Requiring it HERE quietly undid
  // that: the leg was allowed to fail, every other artifact built, and then
  // assembly refused, so a Windows toolchain problem meant nobody got a
  // release at all. Four platforms beat zero — provided the gap is stated.
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-beta-release-no-windows-'));
  const distDir = join(tempRoot, 'dist');
  const releaseDir = join(distDir, 'release');

  for (const artifact of [
    'cli/spacedatanetwork-1.0.3-beta.42-linux-amd64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-linux-arm64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-darwin-amd64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-darwin-arm64.tar.gz',
    'desktop/space-data-network-desktop-0.47.0-mac-arm64.dmg',
    'desktop/space-data-network-desktop-0.47.0-mac-arm64.zip',
    'desktop/space-data-network-desktop-0.47.0-linux-x86_64.AppImage',
    'desktop/space-data-network-desktop-0.47.0-linux-amd64.deb',
    'desktop/space-data-network-desktop-0.47.0-linux-x64.tar.xz',
    'packages/spacedatanetwork-full_1.0.3~beta.42_amd64.deb',
    'packages/spacedatanetwork-edge_1.0.3~beta.42_amd64.rpm',
    'linux-vm/spacedatanetwork-linux-vm-1.0.3~beta.42.tar.gz',
    'container-images/spacedatanetwork-container-1.0.3~beta.42-linux-amd64.tar.gz',
    'sdn-js/spacedatanetwork-sdn-js-2.0.12.tgz',
    'sbom/spacedatanetwork-sbom.cdx.json'
  ]) {
    writeFixture(distDir, artifact, 'fixture');
  }

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

  const notes = readFileSync(join(releaseDir, 'SDN-BETA-RELEASE.md'), 'utf8');
  assert.match(notes, /## Not in this release/);
  assert.match(notes, /windows-amd64\.zip/);
});

test('the real electron-builder Windows names are not reported as missing', () => {
  // These are the EXACT names run 35095153877 produced, the first run in which
  // Windows desktop artifacts existed at all:
  //   space-data-network-desktop-<version>-win-x64.exe            (nsis setup)
  //   space-data-network-desktop-portable-<version>-win-x64.exe   (portable)
  //
  // The patterns used to read "-setup-*-windows-*.exe" and
  // "-portable-*-windows-*.exe" — "windows" not "win", and a "setup" infix that
  // electron-builder does not emit. Because copy_matches publishes everything
  // under dist/desktop, the mismatch did not drop the installers; it listed
  // them under "Not in this release" while they were attached to it. Nothing
  // caught it because Windows had never produced a desktop artifact before.
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-beta-release-windows-desktop-'));
  const distDir = join(tempRoot, 'dist');
  const releaseDir = join(distDir, 'release');

  for (const artifact of [
    'cli/spacedatanetwork-1.0.3-beta.42-linux-amd64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-linux-arm64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-darwin-amd64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-darwin-arm64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-windows-amd64.zip',
    'desktop/space-data-network-desktop-0.47.0-mac-arm64.dmg',
    'desktop/space-data-network-desktop-0.47.0-mac-arm64.zip',
    'desktop/space-data-network-desktop-0.47.0-linux-x86_64.AppImage',
    'desktop/space-data-network-desktop-0.47.0-linux-amd64.deb',
    'desktop/space-data-network-desktop-0.47.0-linux-x64.tar.xz',
    'desktop/space-data-network-desktop-0.47.0-win-x64.exe',
    'desktop/space-data-network-desktop-portable-0.47.0-win-x64.exe',
    'packages/spacedatanetwork-full_1.0.3~beta.42_amd64.deb',
    'packages/spacedatanetwork-edge_1.0.3~beta.42_amd64.rpm',
    'linux-vm/spacedatanetwork-linux-vm-1.0.3~beta.42.tar.gz',
    'container-images/spacedatanetwork-container-1.0.3~beta.42-linux-amd64.tar.gz',
    'sdn-js/spacedatanetwork-sdn-js-2.0.12.tgz',
    'sbom/spacedatanetwork-sbom.cdx.json'
  ]) {
    writeFixture(distDir, artifact, 'fixture');
  }

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

  // Both installers are published...
  assert.ok(existsSync(join(releaseDir, 'space-data-network-desktop-0.47.0-win-x64.exe')));
  assert.ok(existsSync(join(releaseDir, 'space-data-network-desktop-portable-0.47.0-win-x64.exe')));

  // ...and nothing claims a Windows desktop artifact is absent. Everything is
  // present here, so the section must not appear at all.
  const notes = readFileSync(join(releaseDir, 'SDN-BETA-RELEASE.md'), 'utf8');
  assert.doesNotMatch(notes, /## Not in this release/);
  assert.doesNotMatch(notes, /win-\*\.exe/);
});

test('fails when a required desktop release artifact class is missing', () => {
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-beta-release-missing-desktop-'));
  const distDir = join(tempRoot, 'dist');
  const releaseDir = join(distDir, 'release');

  for (const artifact of [
    'cli/spacedatanetwork-1.0.3-beta.42-linux-amd64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-linux-arm64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-darwin-amd64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-darwin-arm64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-windows-amd64.zip',
    'desktop/space-data-network-desktop-0.47.0-mac-arm64.dmg',
    'desktop/space-data-network-desktop-0.47.0-mac-arm64.zip',
    'desktop/space-data-network-desktop-setup-0.47.0-windows-x64.exe',
    'desktop/space-data-network-desktop-portable-0.47.0-windows-x64.exe',
    'desktop/space-data-network-desktop-0.47.0-linux-x86_64.AppImage',
    'desktop/space-data-network-desktop-0.47.0-linux-amd64.deb'
  ]) {
    writeFixture(distDir, artifact, `fixture ${artifact}`);
  }

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
  }, /missing required desktop release artifact matching: space-data-network-desktop-\*-linux-\*\.tar\.xz/);
});

// The feed is the cli-update-feed job's, checked and verified there
// (check-update-signing-key.mjs, verify-cli-update-feed.mjs). The assembler
// ships that tarball as it is: even with a signing key in its environment it
// builds no feed of its own (it used to, with a fallback key id and no
// verification, under the same file name, replacing the verified one).
test('ships the verified CLI update feed as it is and never builds its own', () => {
  const tempRoot = mkdtempSync(join(tmpdir(), 'sdn-beta-release-feed-'));
  const distDir = join(tempRoot, 'dist');
  const releaseDir = join(distDir, 'release');
  const { privateKey } = generateKeyPairSync('ed25519');
  const privateKeyPem = privateKey.export({ type: 'pkcs8', format: 'pem' });
  const verifiedFeed = 'fixture: the verified feed from the cli-update-feed job';
  writeFixture(distDir, 'update-feed/spacedatanetwork-update-feed-1.0.3-beta.42.tar.gz', verifiedFeed);

  for (const artifact of [
    'cli/spacedatanetwork-1.0.3-beta.42-linux-amd64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-linux-arm64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-darwin-amd64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-darwin-arm64.tar.gz',
    'cli/spacedatanetwork-1.0.3-beta.42-windows-amd64.zip',
    'desktop/space-data-network-desktop-0.47.0-mac-arm64.dmg',
    'desktop/space-data-network-desktop-0.47.0-mac-arm64.zip',
    'desktop/space-data-network-desktop-setup-0.47.0-windows-x64.exe',
    'desktop/space-data-network-desktop-portable-0.47.0-windows-x64.exe',
    'desktop/space-data-network-desktop-0.47.0-linux-x86_64.AppImage',
    'desktop/space-data-network-desktop-0.47.0-linux-amd64.deb',
    'desktop/space-data-network-desktop-0.47.0-linux-x64.tar.xz',
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

  assert.equal(
    readFileSync(join(releaseDir, 'spacedatanetwork-update-feed-1.0.3-beta.42.tar.gz'), 'utf8'),
    verifiedFeed,
    'the release must carry the cli-update-feed job\'s verified feed, byte for byte'
  );
  assert.equal(existsSync(join(releaseDir, 'update-feed')), false, 'the assembler built a feed of its own');
  assert.equal(existsSync(join(distDir, 'update-payloads')), false, 'the assembler built update payloads of its own');
  const checksums = readFileSync(join(releaseDir, 'spacedatanetwork-checksums.txt'), 'utf8');
  assert.match(checksums, /spacedatanetwork-update-feed-1\.0\.3-beta\.42\.tar\.gz/);
});

test('every desktop format the workflow builds has a pattern, and vice versa', () => {
  // Three required patterns had never matched anything electron-builder
  // produces — "*-mac.dmg" against a real "-mac-arm64.dmg", a squirrel.zip
  // that is a Windows updater format, and an rpm the Linux leg is not asked to
  // build. The publish job had simply never run far enough to check, so the
  // list drifted for as long as it liked. Tie it to the source of truth.
  const workflow = readFileSync(join(repoRoot, '.github/workflows/beta-release-artifacts.yml'), 'utf8');
  const script = readFileSync(scriptPath, 'utf8');

  const formats = new Set();
  for (const m of workflow.matchAll(/builder_args:\s*(.+)/g)) {
    const args = m[1].trim();
    for (const f of args.matchAll(/--(mac|win|linux)\s+([^-\n]+)/g)) {
      for (const fmt of f[2].trim().split(/\s+/)) formats.add(`${f[1]}:${fmt}`);
    }
  }
  assert(formats.size > 0, 'no builder_args found — has the desktop job moved?');

  const patterns = [
    ...script.matchAll(/"(space-data-network-desktop-[^"]+)"/g)
  ].map((m) => m[1]);
  assert(patterns.length > 0, 'no desktop patterns in the assemble script');

  // Each built format must be claimed by some pattern (required or optional).
  const extensionFor = {
    'mac:dmg': '.dmg', 'mac:zip': '.zip',
    'linux:AppImage': '.AppImage', 'linux:deb': '.deb',
    'linux:tar.xz': '.tar.xz', 'linux:rpm': '.rpm',
    'win:nsis': '.exe', 'win:portable': '.exe',
  };
  for (const format of formats) {
    const ext = extensionFor[format];
    if (!ext) continue;
    assert(
      patterns.some((pattern) => pattern.endsWith(ext)),
      `the desktop job builds ${format} but no pattern in the assemble script claims a ${ext}`,
    );
  }

  // And no pattern may demand a format nothing builds.
  const builtExtensions = new Set([...formats].map((f) => extensionFor[f]).filter(Boolean));
  for (const pattern of patterns) {
    const ext = ['.tar.xz', '.AppImage', '.dmg', '.zip', '.deb', '.rpm', '.exe'].find((e) => pattern.endsWith(e));
    if (!ext) continue;
    assert(
      builtExtensions.has(ext),
      `the assemble script requires "${pattern}", but no desktop builder_args produce a ${ext}`,
    );
  }
});
