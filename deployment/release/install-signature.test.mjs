// SEC-045 regression: the installers trust a release only through the offline
// release key's signature over spacedatanetwork-checksums.txt.
//
// The audit reproduced the hole with the real installer functions: an attacker
// release (newest v* tag) carrying a backdoored archive and a checksum file
// the attacker generated was accepted, extracted and executed. These tests run
// the shipped scripts against a simulated GitHub (download functions served
// from a fixture directory) and assert that only a correctly signed release
// is ever extracted, and that the scripts as committed (no key provisioned)
// refuse to install anything.
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = dirname(dirname(dirname(fileURLToPath(import.meta.url))));
const installerPath = join(repoRoot, 'scripts/install.sh');
const stubPath = join(repoRoot, 'docs/install.sh');
const installer = readFileSync(installerPath, 'utf8');
const stub = readFileSync(stubPath, 'utf8');

const KEY_BLOCK = /# BEGIN SDN RELEASE SIGNING KEY\n[\s\S]*?\n# END SDN RELEASE SIGNING KEY/;
const withKey = (text, pem) => text.replace(KEY_BLOCK,
  `# BEGIN SDN RELEASE SIGNING KEY\nSDN_RELEASE_SIGNING_KEY_PEM='${pem.trim()}'\n# END SDN RELEASE SIGNING KEY`);

const releaseKey = generateKeyPairSync('ec', { namedCurve: 'P-256' });
const attackerKey = generateKeyPairSync('ec', { namedCurve: 'P-256' });
const releasePem = releaseKey.publicKey.export({ type: 'spki', format: 'pem' });
const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');
const signWith = (key, bytes) => sign('sha256', bytes, key.privateKey);

const platform = (() => {
  const os = process.platform === 'darwin' ? 'darwin' : 'linux';
  const arch = process.arch === 'arm64' ? 'arm64' : 'amd64';
  return { os, arch };
})();
const archiveName = (version) => `spacedatanetwork-${version}-${platform.os}-${platform.arch}.tar.gz`;

/** A fake GitHub: releases/download/<tag>/<file> served from <root>/<tag>/<file>. */
function makeWorld() {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-install-sig-'));
  const releases = join(dir, 'releases');
  mkdirSync(releases);
  const tags = [];
  const world = {
    dir,
    releases,
    tags,
    addRelease(tag, { marker, signer = releaseKey, signedArchiveOf, unsigned = false, tamperAfterSigning = false } = {}) {
      const version = tag.replace(/^v/, '');
      const relDir = join(releases, tag);
      mkdirSync(relDir, { recursive: true });
      const bundle = `spacedatanetwork-${version}-${platform.os}-${platform.arch}`;
      const stage = join(dir, 'stage', tag, bundle);
      mkdirSync(join(stage, 'bin'), { recursive: true });
      mkdirSync(join(stage, 'runtime/modules'), { recursive: true });
      for (const name of ['spacedatanetwork', 'sdn']) {
        writeFileSync(join(stage, 'bin', name), `#!/bin/sh\necho ${marker}\n`);
        chmodSync(join(stage, 'bin', name), 0o755);
      }
      writeFileSync(join(stage, 'runtime/modules/org.spacedatanetwork.updater.wasm'), '');
      writeFileSync(join(stage, 'runtime/modules/hd-wallet-wasi.wasm'), '');
      writeFileSync(join(stage, 'manifest.json'), '{}');
      const archive = join(relDir, archiveName(version));
      execFileSync('tar', ['-czf', archive, '-C', join(dir, 'stage', tag), bundle]);
      // The checksum list names the archive of `signedArchiveOf` (a replay of
      // another release's signed list) or this release's own archive.
      const listed = signedArchiveOf
        ? readFileSync(join(releases, signedArchiveOf, 'spacedatanetwork-checksums.txt'))
        : Buffer.from(`${sha256(readFileSync(archive))}  ${archiveName(version)}\n`);
      writeFileSync(join(relDir, 'spacedatanetwork-checksums.txt'), listed);
      if (signedArchiveOf) {
        writeFileSync(join(relDir, 'spacedatanetwork-checksums.txt.sig'),
          readFileSync(join(releases, signedArchiveOf, 'spacedatanetwork-checksums.txt.sig')));
      } else if (!unsigned) {
        writeFileSync(join(relDir, 'spacedatanetwork-checksums.txt.sig'), signWith(signer, listed));
      }
      if (tamperAfterSigning) {
        writeFileSync(join(stage, 'bin', 'spacedatanetwork'), '#!/bin/sh\necho TAMPERED\n');
        execFileSync('tar', ['-czf', archive, '-C', join(dir, 'stage', tag), bundle]);
      }
      tags.unshift(tag); // newest first, like the GitHub API
      return relDir;
    },
    cleanup() {
      rmSync(dir, { recursive: true, force: true });
    },
  };
  return world;
}

/**
 * Run the real installer functions in the order main() runs them, with the
 * network replaced by the fixture. Returns { status, stdout, stderr, ran }.
 */
function runInstaller(world, { script = installer, env = {} } = {}) {
  const lib = join(world.dir, 'install-lib.sh');
  writeFileSync(lib, script.replace(/\nmain "\$@"\n?$/, '\n'));
  const releasesJson = JSON.stringify(world.tags.map((tag_name) => ({ tag_name })));
  const driver = `
set -e
source "${lib}"
fetch_url_stdout() { printf '%s' '${releasesJson}'; }
download_url() {
  local rel="\${1#https://github.com/\${REPO}/releases/download/}"
  [ -f "${world.releases}/\${rel}" ] || return 1
  cp "${world.releases}/\${rel}" "$2"
}
detect_platform
get_latest_version
normalize_version_names
select_archive
download_archive
verify_checksum
extract_archive
"$BUNDLE_ROOT/bin/spacedatanetwork"
`;
  const result = spawnSync('bash', ['-c', driver], {
    encoding: 'utf8',
    env: {
      PATH: process.env.PATH,
      HOME: world.dir,
      SDN_BUNDLE_DIR: join(world.dir, 'bundles'),
      SDN_INSTALL_DIR: join(world.dir, 'bin'),
      SDN_VERSION: '',
      ...env,
    },
  });
  return { ...result, ran: result.stdout.trim().split('\n').at(-1) };
}

const configured = withKey(installer, releasePem);

test('as committed, the installer carries no key and refuses to install anything', (t) => {
  const world = makeWorld();
  t.after(() => world.cleanup());
  world.addRelease('v1.0.5-beta.70', { marker: 'LEGIT_NODE' });
  const result = runInstaller(world);
  assert.notEqual(result.status, 0);
  assert.match(result.stdout + result.stderr, /no release signing key/);
  assert.equal(existsSync(join(world.dir, 'bundles')), false, 'nothing may be extracted');
});

test('SEC-045 repro: a newer attacker release with its own checksums is never installed', (t) => {
  const world = makeWorld();
  t.after(() => world.cleanup());
  world.addRelease('v1.0.5-beta.70', { marker: 'LEGIT_NODE' });
  world.addRelease('v9.9.9-beta.1', { marker: 'BACKDOORED_NODE', unsigned: true });
  const result = runInstaller(world, { script: configured });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.equal(result.ran, 'LEGIT_NODE');
  assert.match(result.stdout, /Skipping v9\.9\.9-beta\.1/);
  assert.doesNotMatch(result.stdout, /BACKDOORED_NODE/);
});

test('a release signed by any other key is skipped', (t) => {
  const world = makeWorld();
  t.after(() => world.cleanup());
  world.addRelease('v1.0.5-beta.70', { marker: 'LEGIT_NODE' });
  world.addRelease('v9.9.9-beta.1', { marker: 'BACKDOORED_NODE', signer: attackerKey });
  const result = runInstaller(world, { script: configured });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.equal(result.ran, 'LEGIT_NODE');
});

test('a signed checksum list replayed under a newer tag is skipped', (t) => {
  const world = makeWorld();
  t.after(() => world.cleanup());
  world.addRelease('v1.0.5-beta.70', { marker: 'LEGIT_NODE' });
  world.addRelease('v9.9.9-beta.1', { marker: 'BACKDOORED_NODE', signedArchiveOf: 'v1.0.5-beta.70' });
  const result = runInstaller(world, { script: configured });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.equal(result.ran, 'LEGIT_NODE');
});

test('pinning an unsigned release with SDN_VERSION fails closed', (t) => {
  const world = makeWorld();
  t.after(() => world.cleanup());
  world.addRelease('v1.0.5-beta.70', { marker: 'LEGIT_NODE' });
  world.addRelease('v9.9.9-beta.1', { marker: 'BACKDOORED_NODE', unsigned: true });
  const result = runInstaller(world, { script: configured, env: { SDN_VERSION: 'v9.9.9-beta.1' } });
  assert.notEqual(result.status, 0);
  assert.match(result.stdout, /no valid release-key signature/);
  assert.doesNotMatch(result.stdout, /BACKDOORED_NODE/);
  assert.equal(existsSync(join(world.dir, 'bundles')), false, 'nothing may be extracted');
});

test('an archive swapped after signing fails the checksum and is not extracted', (t) => {
  const world = makeWorld();
  t.after(() => world.cleanup());
  world.addRelease('v1.0.5-beta.70', { marker: 'LEGIT_NODE', tamperAfterSigning: true });
  const result = runInstaller(world, { script: configured });
  assert.notEqual(result.status, 0);
  assert.match(result.stdout, /Checksum mismatch/);
  assert.equal(existsSync(join(world.dir, 'bundles')), false);
});

test('no signed release at all fails closed', (t) => {
  const world = makeWorld();
  t.after(() => world.cleanup());
  world.addRelease('v9.9.9-beta.1', { marker: 'BACKDOORED_NODE', unsigned: true });
  const result = runInstaller(world, { script: configured });
  assert.notEqual(result.status, 0);
  assert.match(result.stdout, /No signed node release found/);
});

// ---- docs/install.sh, the public stub --------------------------------------

/** Run the stub with a fake curl serving the fixture release. */
function runStub(world, script) {
  const bin = join(world.dir, 'fakebin');
  mkdirSync(bin, { recursive: true });
  const log = join(world.dir, 'curl.log');
  writeFileSync(join(bin, 'curl'), `#!/bin/bash
url=""; out=""
while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift 2;; -*) shift;; *) url="$1"; shift;; esac; done
echo "$url" >> "${log}"
rel="\${url#https://github.com/DigitalArsenal/space-data-network/releases/download/}"
[ -f "${world.releases}/\${rel}" ] || exit 22
cp "${world.releases}/\${rel}" "$out"
`);
  chmodSync(join(bin, 'curl'), 0o755);
  const stubFile = join(world.dir, 'stub.sh');
  writeFileSync(stubFile, script);
  const result = spawnSync('bash', [stubFile], {
    encoding: 'utf8',
    env: { PATH: `${bin}:${process.env.PATH}`, HOME: world.dir },
  });
  return { ...result, fetched: existsSync(log) ? readFileSync(log, 'utf8') : '' };
}

function pinnedStub(tag, installerSha) {
  return withKey(stub, releasePem)
    .replace(/^SDN_INSTALLER_RELEASE_TAG=''$/m, `SDN_INSTALLER_RELEASE_TAG='${tag}'`)
    .replace(/^SDN_INSTALLER_SHA256=''$/m, `SDN_INSTALLER_SHA256='${installerSha}'`);
}

function addInstallerRelease(world, tag, { signer = releaseKey, installerBody = 'echo INSTALLER_RAN\n', listedSha } = {}) {
  const relDir = join(world.releases, tag);
  mkdirSync(relDir, { recursive: true });
  writeFileSync(join(relDir, 'install.sh'), installerBody);
  const sha = listedSha || sha256(Buffer.from(installerBody));
  const list = Buffer.from(`${sha}  install.sh\n`);
  writeFileSync(join(relDir, 'spacedatanetwork-checksums.txt'), list);
  writeFileSync(join(relDir, 'spacedatanetwork-checksums.txt.sig'), signWith(signer, list));
  return sha;
}

test('the public stub never reads main and, as committed, runs nothing', (t) => {
  assert.doesNotMatch(stub, /raw\.githubusercontent\.com/);
  assert.doesNotMatch(stub, /main\/scripts\//);
  assert.doesNotMatch(stub, /SDN_INSTALLER_URL/);
  const world = makeWorld();
  t.after(() => world.cleanup());
  addInstallerRelease(world, 'v1.0.5-beta.71');
  const result = runStub(world, stub);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /no signed installer release is pinned/);
  assert.equal(result.fetched, '', 'the unpinned stub must not download anything');
});

test('the pinned stub runs the signed installer and nothing else', (t) => {
  const world = makeWorld();
  t.after(() => world.cleanup());
  const sha = addInstallerRelease(world, 'v1.0.5-beta.71');
  const ok = runStub(world, pinnedStub('v1.0.5-beta.71', sha));
  assert.equal(ok.status, 0, ok.stderr);
  assert.match(ok.stdout, /INSTALLER_RAN/);
  assert.doesNotMatch(ok.fetched, /\/main\//);
});

test('the pinned stub refuses a swapped installer, a forged signature or a list that pins another file', (t) => {
  const world = makeWorld();
  t.after(() => world.cleanup());
  const sha = addInstallerRelease(world, 'v1.0.5-beta.71');
  // Swapped asset: same signed list, different install.sh bytes.
  writeFileSync(join(world.releases, 'v1.0.5-beta.71', 'install.sh'), 'echo BACKDOOR\n');
  const swapped = runStub(world, pinnedStub('v1.0.5-beta.71', sha));
  assert.notEqual(swapped.status, 0);
  assert.doesNotMatch(swapped.stdout, /BACKDOOR/);

  const forgedSha = addInstallerRelease(world, 'v1.0.5-beta.72', { signer: attackerKey, installerBody: 'echo BACKDOOR\n' });
  const forged = runStub(world, pinnedStub('v1.0.5-beta.72', forgedSha));
  assert.notEqual(forged.status, 0);
  assert.match(forged.stderr, /signature does not verify/);
  assert.doesNotMatch(forged.stdout, /BACKDOOR/);

  // A correctly signed list whose install.sh entry differs from the stub pin.
  addInstallerRelease(world, 'v1.0.5-beta.73', { listedSha: 'f'.repeat(64) });
  const realSha = sha256(Buffer.from('echo INSTALLER_RAN\n'));
  const mismatch = runStub(world, pinnedStub('v1.0.5-beta.73', realSha));
  assert.notEqual(mismatch.status, 0);
  assert.match(mismatch.stderr, /does not pin this installer/);
});

test('both installers carry the same release key block', () => {
  const block = (text) => KEY_BLOCK.exec(text)?.[0];
  assert.ok(block(installer), 'scripts/install.sh has the key block');
  assert.equal(block(stub), block(installer), 'docs/install.sh and scripts/install.sh must embed the same key');
});

test('the PowerShell entrypoint no longer executes code from main', () => {
  const ps = readFileSync(join(repoRoot, 'docs/install.ps1'), 'utf8')
    .split('\n').filter((line) => !line.trimStart().startsWith('#')).join('\n');
  assert.doesNotMatch(ps, /raw\.githubusercontent\.com/);
  assert.doesNotMatch(ps, /Invoke-Expression|\biex\b/i);
  assert.match(ps, /throw/);
});

test('the beta release ships install.sh and is created as a draft for offline signing', () => {
  const assemble = readFileSync(join(repoRoot, 'deployment/release/assemble-beta-release-artifacts.sh'), 'utf8');
  assert.match(assemble, /cp "\$\{root\}\/scripts\/install\.sh" "\$\{release_dir\}\/install\.sh"/);
  const workflow = readFileSync(join(repoRoot, '.github/workflows/beta-release-artifacts.yml'), 'utf8');
  assert.match(workflow, /gh release create[\s\S]*--draft/);
  assert.doesNotMatch(workflow, /make_latest:\s*true/);
  assert.doesNotMatch(workflow, /softprops\/action-gh-release/);
});
