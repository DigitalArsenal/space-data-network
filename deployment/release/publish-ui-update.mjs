#!/usr/bin/env node
/**
 * publish-ui-update — the dashboard and homepage to every node, with no new
 * binary.
 *
 * OWNER 2026-10-07: "package the updates and send them through the update
 * channel, digitally signed and encrypted, and have the running servers update
 * in situ WITHOUT needing to republish the binaries."
 *
 * Takes a UI build (what sdn-js/dashboard/build-dashboard.mjs writes into the
 * binary's embedded/ folder: dashboard.html + .csp, homepage.html + .csp,
 * media/) down the lane publish-fleet-update.mjs takes a binary down: the same
 * feed host, the same node key, the same signal topic.
 *
 *   0. lineage: the UI commit must descend from the one the feed's newest UI
 *      package was built from (source-lineage.mjs, run against the UI repo);
 *   1. recipients: each node in fleet-nodes.json, its sealed-transport key and
 *      running build read from its own /api/node/info and /api/v1/id over ssh;
 *   2. the bundle (manifest.json + ui/), tar.gz;
 *   3. the unsigned manifest: kind ui-bundle, target any/any, modules[] naming
 *      every file with its sha256, compatibility.bundle_versions = the build
 *      the nodes run;
 *   4. on the publisher host: `update seal` (encrypt once, wrap the key for
 *      each node), then `update sign-manifest` (the node key);
 *   5. manifest.json + update.wasm into ui-bundle/<channel>/any/any/<version>/,
 *      the index regenerated, the public URLs checked against those bytes;
 *   6. the ledger line, then the signal. Each node installs the package while
 *      it serves (internal/update/uipackage.go).
 *
 * Usage:
 *   node deployment/release/publish-ui-update.mjs --source-commit <ui sha> [--dry-run]
 */
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildUpdateManifest } from './sign-update-manifest.mjs';
import { LINEAGE_ROLLBACK, SourceLineageRefusal, assertSourceLineage, resolveLiveSourceCommit } from './source-lineage.mjs';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');

function arg(name, fallback) {
  const i = process.argv.indexOf(`--${name}`);
  if (i >= 0 && process.argv[i + 1] && !process.argv[i + 1].startsWith('--')) {
    return process.argv[i + 1];
  }
  return fallback;
}
const flag = (name) => process.argv.includes(`--${name}`);

const sourceCommitArg = arg('source-commit');
if (!sourceCommitArg) {
  console.error('required: --source-commit <the SpaceAware-UI commit the build came from>');
  process.exit(2);
}
const uiDir = resolve(arg('ui-dir', join(repoRoot, 'sdn-server/cmd/spacedatanetwork/embedded')));
const uiRepo = resolve(arg('ui-repo', join(repoRoot, 'sdn-js/spaceaware-ui')));
const channel = arg('channel', 'beta');
const publisherSSH = arg('publisher-ssh', 'space-data-network-01');
const publisherBin = arg('publisher-bin', '/opt/spacedatanetwork/bin/spacedatanetwork');
const feedDir = arg('feed-dir', '/opt/spacedatanetwork/update-feed');
const feedBaseUrl = arg('feed-base-url', 'https://sdn.spaceaware.io/updates');
const keyId = arg('key-id', 'd4a971a7e534');
const nodesFile = resolve(arg('nodes', join(repoRoot, 'deployment/release/fleet-nodes.json')));
const forVersions = arg('for-version', '');
const ledgerPath = arg('ledger-path', '/opt/spacedatanetwork/publish-ledger.log');
// Opt-outs take their REASON as their value, like publish-fleet-update's.
const rollbackReason = arg('rollback', '');
const noSignalReason = arg('no-signal', '');
const dryRun = flag('dry-run');

const UI_FILES = ['dashboard.html', 'dashboard.csp', 'homepage.html', 'homepage.csp'];
const MEDIA_EXTENSIONS = new Set(['.mp4', '.jpg', '.png', '.webp']);
const kind = 'ui-bundle';
const feedRel = `${kind}/${channel}/any/any`;
const sequence = Math.floor(Date.now() / 1000);
const createdAt = new Date().toISOString();
const expiresAt = new Date(Date.now() + 90 * 24 * 3600 * 1000).toISOString();

const sha256 = (b) => createHash('sha256').update(b).digest('hex');
const run = (cmd, args, opts = {}) => execFileSync(cmd, args, { stdio: ['ignore', 'pipe', 'inherit'], ...opts });
const log = (line) => console.error(line);
const shellQuote = (s) => `'${String(s).replace(/'/g, `'\\''`)}'`;

class Refusal extends Error {}

function fetchWithStatus(url) {
  try {
    const out = run('curl', ['-s', '-m', '30', '-w', '\n%{http_code}', url]).toString();
    const at = out.lastIndexOf('\n');
    return { status: Number(out.slice(at + 1)), body: out.slice(0, at) };
  } catch (error) {
    return { status: 0, error: error.message };
  }
}

const work = mkdtempSync(join(tmpdir(), 'sdn-ui-update-'));
const cleanup = () => rmSync(work, { recursive: true, force: true });

try {
  const sourceCommit = run('git', ['-C', uiRepo, 'rev-parse', `${sourceCommitArg}^{commit}`]).toString().trim();
  const remote = run('git', ['-C', uiRepo, 'remote', 'get-url', 'origin']).toString().trim();
  const sourceRepository = (remote.match(/[:/]([^/:]+\/[^/]+?)(?:\.git)?$/) || [])[1] || remote;
  const version = `ui.${sequence}.${sourceCommit.slice(0, 9)}`;
  const updateId = `sdn-${kind}-${version}`;

  // --- 0. lineage ------------------------------------------------------------
  const live = resolveLiveSourceCommit({ feedBaseUrl, channel, platform: 'any', arch: 'any', versionPrefix: 'ui', fetch: fetchWithStatus, kind });
  const lineage = assertSourceLineage({ repoRoot: uiRepo, sourceCommit, liveCommit: live?.commit ?? null, rollbackReason });
  log(`[ui-update] lineage ${lineage.lineage.toUpperCase()}: ${sourceCommit.slice(0, 9)} vs live ${live?.commit?.slice(0, 9) ?? '(none)'}`);
  if (lineage.lineage === LINEAGE_ROLLBACK) log(`[ui-update] *** DECLARED ROLLBACK *** reason: ${lineage.reason}`);

  // --- 1. recipients ---------------------------------------------------------
  const fleet = JSON.parse(readFileSync(nodesFile, 'utf8'));
  const recipients = [];
  const builds = new Set();
  for (const node of fleet.nodes ?? []) {
    const ask = (route) =>
      JSON.parse(run('ssh', ['-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', node.ssh, `curl -sk -m 15 ${shellQuote(node.node_url + route)}`]).toString());
    const info = ask('/api/node/info');
    const id = ask('/api/v1/id');
    const sealed = info.sealed_transport ?? {};
    if (!/^0[23][0-9a-f]{64}$/.test(sealed.encryption_key ?? '') || !sealed.fingerprint) {
      throw new Refusal(`${node.name} advertises no sealed-transport key at ${node.node_url}/api/node/info; it could not open the package`);
    }
    if (!id.bundle_version) throw new Refusal(`${node.name} reports no bundle_version; it is not a bundle install the lane serves`);
    recipients.push({ name: node.name, peer_id: info.peer_id, encryption_key: sealed.encryption_key, fingerprint: sealed.fingerprint });
    builds.add(id.bundle_version);
    log(`[ui-update] recipient ${node.name} ${String(info.peer_id).slice(-8)} key ${sealed.fingerprint} build ${id.bundle_version}`);
  }
  if (recipients.length === 0) throw new Refusal(`${nodesFile} names no node`);
  const bundleVersions = forVersions ? forVersions.split(',').map((v) => v.trim()).filter(Boolean) : [...builds];
  if (!forVersions && builds.size > 1) {
    throw new Refusal(
      `the nodes run different builds (${[...builds].join(', ')}); a UI package is made for one. Roll them to one build, ` +
        'or name the builds this UI works on with --for-version a,b',
    );
  }

  // --- 2. bundle -------------------------------------------------------------
  const bundleName = `sdn-${kind}-${version}`;
  const bundleRoot = join(work, bundleName);
  mkdirSync(join(bundleRoot, 'ui', 'media'), { recursive: true });
  const files = [];
  for (const name of UI_FILES) {
    copyFileSync(join(uiDir, name), join(bundleRoot, 'ui', name));
    files.push(`ui/${name}`);
  }
  const mediaDir = join(uiDir, 'media');
  for (const name of readdirSync(mediaDir).sort()) {
    const ext = name.slice(name.lastIndexOf('.')).toLowerCase();
    if (name.startsWith('.') || !MEDIA_EXTENSIONS.has(ext) || !statSync(join(mediaDir, name)).isFile()) continue;
    copyFileSync(join(mediaDir, name), join(bundleRoot, 'ui', 'media', name));
    files.push(`ui/media/${name}`);
  }
  const artifacts = files.map((path) => ({ path, sha256: sha256(readFileSync(join(bundleRoot, path))) }));
  writeFileSync(
    join(bundleRoot, 'manifest.json'),
    `${JSON.stringify({ schema: 'org.spacedatanetwork.bundle.v1', version, channel, artifacts, provenance: { sourceRepository, sourceCommit } }, null, 2)}\n`,
  );
  const archivePath = join(work, `${bundleName}.tar.gz`);
  // No AppleDouble or extended-attribute entries: the node refuses any file
  // the signed manifest does not name.
  const tarFlags = process.platform === 'darwin' ? ['--no-mac-metadata', '--no-xattrs'] : [];
  run('tar', [...tarFlags, '-czf', archivePath, bundleName], { cwd: work, env: { ...process.env, COPYFILE_DISABLE: '1' } });
  const listed = run('tar', ['-tzf', archivePath]).toString().split('\n').filter(Boolean);
  const allowed = new Set([`${bundleName}/`, `${bundleName}/manifest.json`, `${bundleName}/ui/`, `${bundleName}/ui/media/`, ...files.map((f) => `${bundleName}/${f}`)]);
  const stray = listed.filter((entry) => !allowed.has(entry));
  if (stray.length) throw new Refusal(`the archive carries entries the manifest does not name: ${stray.join(', ')}`);
  const bundleBytes = readFileSync(archivePath);
  const bundleHash = sha256(bundleBytes);
  log(`[ui-update] bundle ${bundleBytes.length}B sha ${bundleHash.slice(0, 16)}: ${files.length} files`);

  // --- 3. unsigned manifest --------------------------------------------------
  const manifest = buildUpdateManifest({
    updateId,
    version,
    channel,
    platform: 'any',
    arch: 'any',
    kind,
    keyId,
    sequence,
    createdAt,
    expiresAt,
    bundleBytes,
    // A placeholder: `update seal` replaces wasm.hash with the ciphertext
    // carrier's, which is what the feed serves.
    wasmBytes: bundleBytes,
    provenance: {
      source_repository: sourceRepository,
      source_commit: sourceCommit,
      supersedes_commit: lineage.supersedesCommit,
      lineage: lineage.lineage,
      ...(lineage.reason ? { rollback_reason: lineage.reason } : {}),
    },
  });
  manifest.modules = artifacts.map((a) => ({ id: a.path.replace(/^ui\//, ''), hash: a.sha256, path: a.path }));
  manifest.compatibility = { bundle_versions: bundleVersions };
  const unsignedPath = join(work, 'manifest.unsigned.json');
  writeFileSync(unsignedPath, `${JSON.stringify(manifest, null, 2)}\n`);
  const recipientsPath = join(work, 'recipients.json');
  writeFileSync(recipientsPath, `${JSON.stringify(recipients, null, 2)}\n`);

  if (dryRun) {
    console.log(JSON.stringify({ dryRun: true, updateId, version, sequence, work, bundleHash, bundleSize: bundleBytes.length,
      files, bundleVersions, recipients: recipients.map((r) => r.name), lineage: lineage.lineage, sourceCommit }, null, 2));
    process.exit(0);
  }

  // --- 4. sealed and node-signed on the publisher host -----------------------
  const remoteTmp = `/tmp/sdn-ui-update-${sequence}`;
  run('ssh', [publisherSSH, `mkdir -p ${remoteTmp}`]);
  run('scp', ['-q', archivePath, unsignedPath, recipientsPath, `${publisherSSH}:${remoteTmp}/`]);
  log(
    run('ssh', [
      publisherSSH,
      `${shellQuote(publisherBin)} update seal --manifest ${remoteTmp}/manifest.unsigned.json ` +
        `--bundle ${remoteTmp}/${bundleName}.tar.gz --recipients ${remoteTmp}/recipients.json ` +
        `--out-manifest ${remoteTmp}/manifest.sealed.json --out-carrier ${remoteTmp}/update.wasm`,
    ])
      .toString()
      .trim()
      .replace(/^/gm, '[ui-update] '),
  );
  // NO --node-url, as in publish-fleet-update: the client dials loopback and
  // anchors to the daemon's own certificate.
  run('ssh', [publisherSSH, `${shellQuote(publisherBin)} update sign-manifest --manifest ${remoteTmp}/manifest.sealed.json --out ${remoteTmp}/manifest.json`]);
  const signedPath = join(work, 'manifest.json');
  const carrierPath = join(work, 'update.wasm');
  run('scp', ['-q', `${publisherSSH}:${remoteTmp}/manifest.json`, signedPath]);
  run('scp', ['-q', `${publisherSSH}:${remoteTmp}/update.wasm`, carrierPath]);
  const signed = JSON.parse(readFileSync(signedPath, 'utf8'));
  const carrier = readFileSync(carrierPath);
  const carrierHash = sha256(carrier);
  // What came back must still describe this package, sealed for these nodes.
  if (!signed.signing?.signature || signed.signing.statement_domain !== 'SDN-UPDATE-MANIFEST-V1') {
    throw new Refusal('the publisher returned an unsigned or wrongly-domained manifest');
  }
  if (signed.bundle?.hash !== bundleHash || signed.bundle?.size !== bundleBytes.length || signed.update_id !== updateId || signed.sequence !== sequence) {
    throw new Refusal('the signed manifest does not describe this bundle');
  }
  if (signed.wasm?.hash !== carrierHash) throw new Refusal('the signed manifest does not describe the sealed carrier');
  const sealedFor = new Set((signed.envelope?.recipients ?? []).map((r) => Buffer.from(r.key_id, 'base64').toString()));
  const missing = recipients.filter((r) => !sealedFor.has(r.fingerprint));
  if (missing.length || sealedFor.size !== recipients.length) {
    throw new Refusal(`the envelope is not sealed for exactly the recipients (missing: ${missing.map((r) => r.name).join(', ') || 'none'})`);
  }

  // --- 5. publish + index + verify the public surface ------------------------
  const payloadRemote = `${feedDir}/${feedRel}/${version}`;
  run('ssh', [publisherSSH, `mkdir -p ${payloadRemote} && cp ${remoteTmp}/manifest.json ${remoteTmp}/update.wasm ${payloadRemote}/ && rm -rf ${remoteTmp}`]);
  const stored = run('ssh', [publisherSSH, `sha256sum ${payloadRemote}/update.wasm | cut -d' ' -f1`]).toString().trim();
  if (stored !== carrierHash) throw new Refusal(`the carrier stored on ${publisherSSH} hashes to ${stored}, expected ${carrierHash}`);
  const indexScript = `
import json, glob, os
base = ${JSON.stringify(`${feedDir}/${feedRel}`)}
url_base = ${JSON.stringify(`${feedBaseUrl}/${feedRel}`)}
updates = []
for mp in glob.glob(base + '/*/manifest.json'):
    m = json.load(open(mp))
    v = m['version']
    carrier = os.path.join(os.path.dirname(mp), 'update.wasm')
    entry = {
        'update_id': m['update_id'], 'version': v, 'sequence': m['sequence'], 'channel': m['channel'],
        'target': m['target'], 'expires_at': m['expires_at'],
        'bundle_hash': m['bundle']['hash'], 'bundle_size': m['bundle'].get('size'),
        'wasm_hash': m['wasm']['hash'], 'signing_key_id': m['signing']['key_id'],
        'manifest_url': f'{url_base}/{v}/manifest.json', 'carrier_url': f'{url_base}/{v}/update.wasm',
    }
    if os.path.exists(carrier):
        entry['wasm_size'] = os.path.getsize(carrier)
    prov = m.get('provenance') or {}
    if prov.get('source_commit'):
        entry['source_commit'] = prov['source_commit']
    if prov.get('lineage'):
        entry['lineage'] = prov['lineage']
    rev = os.path.join(os.path.dirname(mp), 'revoked.json')
    if os.path.exists(rev):
        entry['revoked'] = json.load(open(rev))
    updates.append(entry)
updates.sort(key=lambda u: u['sequence'], reverse=True)
index = {'schema': 'org.spacedatanetwork.update.index.v1', 'generated_at': ${JSON.stringify(createdAt)},
         'feed_base_url': ${JSON.stringify(feedBaseUrl)}, 'updates': updates}
open(base + '/index.json', 'w').write(json.dumps(index, indent=2) + '\\n')
print(f'index: {len(updates)} UI package(s)')
`;
  run('ssh', [publisherSSH, `python3 - <<'PYEOF'\n${indexScript}\nPYEOF`], { stdio: 'inherit' });
  const publicManifest = fetchWithStatus(`${feedBaseUrl}/${feedRel}/${version}/manifest.json`);
  if (publicManifest.status !== 200 || JSON.parse(publicManifest.body).signing?.signature !== signed.signing.signature) {
    throw new Refusal(`the public feed does not serve the signed manifest (HTTP ${publicManifest.status})`);
  }
  const servedPath = join(work, 'served.wasm');
  run('curl', ['-s', '-m', '300', '-o', servedPath, `${feedBaseUrl}/${feedRel}/${version}/update.wasm`]);
  if (sha256(readFileSync(servedPath)) !== carrierHash) throw new Refusal('the public feed does not serve the sealed carrier');
  log(`[ui-update] published ${updateId}; the public feed serves the signed manifest and the sealed carrier`);

  // --- 6. ledger, then the signal --------------------------------------------
  const ledgerLine = JSON.stringify({
    event: 'ui-update-published', at: createdAt, update_id: updateId, version, sequence, channel, kind,
    source_repository: sourceRepository, source_commit: sourceCommit, lineage: lineage.lineage,
    bundle_sha256: bundleHash, wasm_sha256: carrierHash, bundle_versions: bundleVersions,
    recipients: recipients.map((r) => `${r.name}:${r.fingerprint}`), published_by: process.env.USER || 'unknown',
    signal: noSignalReason ? 'skipped' : 'pushed', ...(noSignalReason ? { signal_skipped_reason: noSignalReason } : {}),
  });
  try {
    run('ssh', [publisherSSH, `printf '%s\\n' ${shellQuote(ledgerLine)} >> ${shellQuote(ledgerPath)}`]);
  } catch (error) {
    console.error(`[ui-update] WARNING: could not write the publish ledger line (${error.message}); record it by hand:\n${ledgerLine}`);
  }
  let signalled = false;
  if (noSignalReason) {
    log(`[ui-update] SIGNAL SKIPPED (recorded reason): ${noSignalReason}`);
  } else {
    const out = run('ssh', [
      publisherSSH,
      `${shellQuote(publisherBin)} update signal --channel ${shellQuote(channel)} --update-id ${shellQuote(updateId)} ` +
        `--kind ${kind} --platform any --arch any`,
    ]).toString();
    signalled = /published=true/.test(out);
    if (!signalled) throw new Error(`the publisher did not report published=true:\n${out}`);
    log(`[ui-update] SIGNAL PUSHED: every node installs ${version} while it serves`);
  }
  console.log(JSON.stringify({ published: updateId, version, sequence, signalled, bundleVersions,
    recipients: recipients.map((r) => r.name), sourceCommit, lineage: lineage.lineage, feed: `${feedBaseUrl}/${feedRel}/` }, null, 2));
  cleanup();
} catch (error) {
  cleanup();
  if (error instanceof Refusal || error instanceof SourceLineageRefusal) {
    console.error(`\nREFUSED: ${error.message}\n`);
    process.exit(3);
  }
  throw error;
}
