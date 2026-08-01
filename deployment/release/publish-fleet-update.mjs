#!/usr/bin/env node
/**
 * publish-fleet-update — the ONE driver for "every change/build reaches the
 * fleet through the update server, in place" (owner ruling 2026-08-01).
 *
 * Takes a locally built linux/amd64 binary (build-locally-ship-binaries law:
 * Docker buildx on the operator Mac, never on hosts) and drives the full
 * producer lane that heph-updater proved end-to-end on 2026-07-31:
 *
 *   1. stage the MINIMAL fleet bundle (bin/<exe> + manifest.json + trust/)
 *      — the deliberate lean shape the live vm-orbit-det-01 bundle uses, NOT
 *      the desktop stageBundle layout; wasmedge/hd-wallet stay in LIBDIR
 *      outside the bundle so a swap never takes the runtime with it;
 *   2. tar.gz it and wrap it in the inert wasm carrier;
 *   3. build the unsigned update manifest (sequence = epoch seconds, so it is
 *      monotonic across publishes without any registry);
 *   4. sign it WITH THE NODE KEY: the manifest document travels to the
 *      publisher host over ssh and `spacedatanetwork update sign-manifest`
 *      runs THERE (the bonded key cannot leave the box; owner ruling
 *      2026-07-30) — no session token ever crosses the wire;
 *   5. publish payload + regenerated index into SDN_UPDATE_FEED_DIR and
 *      verify the PUBLIC url serves the exact bytes just built.
 *
 * Consumers then run `spacedatanetwork update install` on each box (in
 * place). Nothing here restarts daemons — the fleet's own lane does.
 *
 * Usage:
 *   node deployment/release/publish-fleet-update.mjs \
 *     --binary ./buildout-spacedatanetwork \
 *     --source-commit <shortsha> \
 *     [--channel beta] [--platform linux] [--arch amd64] \
 *     [--publisher-ssh space-data-network-01] \
 *     [--publisher-bin /opt/spacedatanetwork/bin/spacedatanetwork] \
 *     [--feed-dir /opt/spacedatanetwork/update-feed] \
 *     [--feed-base-url https://sdn.spaceaware.io/updates] \
 *     [--version-prefix 1.0.6-updatelane] [--dry-run]
 */
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, chmodSync, rmSync, cpSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildCarrier } from './build-update-carrier.mjs';
import { buildUpdateManifest } from './sign-update-manifest.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, '..', '..');

function arg(name, fallback) {
  const i = process.argv.indexOf(`--${name}`);
  if (i >= 0 && process.argv[i + 1] && !process.argv[i + 1].startsWith('--')) {
    return process.argv[i + 1];
  }
  return fallback;
}
const flag = (name) => process.argv.includes(`--${name}`);

const binaryPath = arg('binary');
const sourceCommit = arg('source-commit');
if (!binaryPath || !sourceCommit) {
  console.error('required: --binary <path> --source-commit <shortsha>');
  process.exit(2);
}
const channel = arg('channel', 'beta');
const platform = arg('platform', 'linux');
const arch = arg('arch', 'amd64');
const publisherSSH = arg('publisher-ssh', 'space-data-network-01');
const publisherBin = arg('publisher-bin', '/opt/spacedatanetwork/bin/spacedatanetwork');
const feedDir = arg('feed-dir', '/opt/spacedatanetwork/update-feed');
const feedBaseUrl = arg('feed-base-url', 'https://sdn.spaceaware.io/updates');
const versionPrefix = arg('version-prefix', '1.0.6-updatelane');
const keyId = arg('key-id', 'd4a971a7e534');
const dryRun = flag('dry-run');

const version = `${versionPrefix}.${sourceCommit}`;
const updateId = `sdn-cli-bundle-${version}`;
const sequence = Math.floor(Date.now() / 1000);
const createdAt = new Date().toISOString();
const expiresAt = new Date(Date.now() + 365 * 24 * 3600 * 1000).toISOString();

const sha256 = (b) => createHash('sha256').update(b).digest('hex');
const run = (cmd, args, opts = {}) =>
  execFileSync(cmd, args, { stdio: ['ignore', 'pipe', 'inherit'], ...opts });

// --- 1. minimal fleet bundle -------------------------------------------------
const work = mkdtempSync(join(tmpdir(), 'sdn-fleet-update-'));
const bundleName = `spacedatanetwork-${version}-${platform}-${arch}`;
const bundleRoot = join(work, bundleName);
mkdirSync(join(bundleRoot, 'bin'), { recursive: true });
cpSync(binaryPath, join(bundleRoot, 'bin', 'spacedatanetwork'));
chmodSync(join(bundleRoot, 'bin', 'spacedatanetwork'), 0o755);

// NO trust/ in a lane bundle — the updater REFUSES it as a protected entry
// (proven live on the first publish: `update bundle must not contain
// protected entry "trust"`). Trust roots are installed once at BOOTSTRAP and
// persist across swaps precisely so an update can never rotate the anchors
// that verify updates. Bootstrap installers get trust/ from
// deployment/release/fleet-trust-roots.json instead.

writeFileSync(
  join(bundleRoot, 'manifest.json'),
  `${JSON.stringify(
    {
      schema: 'org.spacedatanetwork.bundle.v1',
      version,
      channel,
      signature: 'ed25519:pending-bundle-signature',
      os: platform,
      arch,
      update: {
        feedBaseUrl,
        pubsubTopic: `/sdn/updates/v1/${channel}`,
        updaterModule: 'org.spacedatanetwork.updater',
        updaterWasm: 'runtime/modules/org.spacedatanetwork.updater.wasm',
      },
      provenance: {
        sourceRepository: 'DigitalArsenal/space-data-network',
        sourceCommit,
        buildRunId: `local-publish-fleet-update-${createdAt.replace(/[-:.]/g, '').slice(0, 15)}Z`,
      },
    },
    null,
    2,
  )}\n`,
);

// --- 2. archive + carrier ----------------------------------------------------
const archivePath = join(work, `${bundleName}.tar.gz`);
run('tar', ['-czf', archivePath, bundleName], { cwd: work });
const bundleBytes = readFileSync(archivePath);
const wasmBytes = buildCarrier(bundleBytes);
console.error(`[fleet-update] bundle ${bundleBytes.length}B sha ${sha256(bundleBytes).slice(0, 16)} carrier ${wasmBytes.length}B`);

// --- 3. unsigned manifest ----------------------------------------------------
const manifest = buildUpdateManifest({
  updateId,
  version,
  channel,
  platform,
  arch,
  kind: 'cli-bundle',
  keyId,
  sequence,
  createdAt,
  expiresAt,
  bundleBytes,
  wasmBytes,
});
const unsignedPath = join(work, 'manifest.unsigned.json');
writeFileSync(unsignedPath, `${JSON.stringify(manifest, null, 2)}\n`);

if (dryRun) {
  console.log(JSON.stringify({ dryRun: true, updateId, version, sequence, work }, null, 2));
  process.exit(0);
}

// --- 4. node-signed on the publisher host -----------------------------------
const remoteTmp = `/tmp/sdn-manifest-${sequence}`;
run('ssh', [publisherSSH, `mkdir -p ${remoteTmp}`]);
run('scp', ['-q', unsignedPath, `${publisherSSH}:${remoteTmp}/manifest.unsigned.json`]);
run('ssh', [
  publisherSSH,
  `${publisherBin} update sign-manifest --manifest ${remoteTmp}/manifest.unsigned.json ` +
    `--out ${remoteTmp}/manifest.json --node-url https://sdn.spaceaware.io`,
]);
const signedPath = join(work, 'manifest.json');
run('scp', ['-q', `${publisherSSH}:${remoteTmp}/manifest.json`, signedPath]);
run('ssh', [publisherSSH, `rm -rf ${remoteTmp}`]);
const signed = JSON.parse(readFileSync(signedPath, 'utf8'));
if (!signed.signing?.signature || signed.signing.statement_domain !== 'SDN-UPDATE-MANIFEST-V1') {
  throw new Error('publisher returned an unsigned or wrongly-domained manifest');
}

// --- 5. publish payload + regenerate index ----------------------------------
const feedRel = `cli-bundle/${channel}/${platform}/${arch}`;
const payloadRemote = `${feedDir}/${feedRel}/${version}`;
run('ssh', [publisherSSH, `mkdir -p ${payloadRemote}`]);
run('scp', ['-q', signedPath, `${publisherSSH}:${payloadRemote}/manifest.json`]);
const carrierLocal = join(work, 'update.wasm');
writeFileSync(carrierLocal, wasmBytes);
run('scp', ['-q', carrierLocal, `${publisherSSH}:${payloadRemote}/update.wasm`]);

// Regenerate index.json from every manifest present (newest first by
// sequence) so history stays listed and rollback targets stay resolvable.
const indexScript = `
import json, glob, sys
base = ${JSON.stringify(`${feedDir}/${feedRel}`)}
url_base = ${JSON.stringify(`${feedBaseUrl}/${feedRel}`)}
updates = []
for mp in glob.glob(base + '/*/manifest.json'):
    m = json.load(open(mp))
    v = m['version']
    updates.append({
        'update_id': m['update_id'],
        'version': v,
        'sequence': m['sequence'],
        'channel': m['channel'],
        'target': m['target'],
        'expires_at': m['expires_at'],
        'bundle_hash': m['bundle']['hash'],
        'wasm_hash': m['wasm']['hash'],
        'signing_key_id': m['signing']['key_id'],
        'manifest_url': f'{url_base}/{v}/manifest.json',
        'carrier_url': f'{url_base}/{v}/update.wasm',
    })
updates.sort(key=lambda u: u['sequence'], reverse=True)
index = {
    'schema': 'org.spacedatanetwork.update.index.v1',
    'generated_at': ${JSON.stringify(createdAt)},
    'feed_base_url': ${JSON.stringify(feedBaseUrl)},
    'updates': updates,
}
open(base + '/index.json', 'w').write(json.dumps(index, indent=2) + '\\n')
print(f'index: {len(updates)} update(s)')
`;
run('ssh', [publisherSSH, `python3 - <<'PYEOF'\n${indexScript}\nPYEOF`], { stdio: 'inherit' });

// --- 6. verify the public surface serves what we built ----------------------
const pubManifest = run('curl', ['-s', '-m', '15', `${feedBaseUrl}/${feedRel}/${version}/manifest.json`]);
const pubIndex = run('curl', ['-s', '-m', '15', `${feedBaseUrl}/${feedRel}/index.json`]);
const pm = JSON.parse(pubManifest.toString());
const pi = JSON.parse(pubIndex.toString());
if (pm.bundle.hash !== sha256(bundleBytes)) {
  throw new Error('public manifest bundle hash does not match the bytes just built');
}
if (!pi.updates.some((u) => u.update_id === updateId && u.sequence === sequence)) {
  throw new Error('public index does not list the new update');
}
rmSync(work, { recursive: true, force: true });
console.log(
  JSON.stringify(
    { published: updateId, version, sequence, bundleHash: pm.bundle.hash, feed: `${feedBaseUrl}/${feedRel}/` },
    null,
    2,
  ),
);
