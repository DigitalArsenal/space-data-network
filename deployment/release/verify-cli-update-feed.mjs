// Verifies a built CLI update feed tree the way an installed node will, before
// the release publishes it.
//
//   node deployment/release/verify-cli-update-feed.mjs --feed-dir <update-feed> \
//     [--roots deployment/release/fleet-trust-roots.json] [--expect-version <v>] [--expect-count <n>]
//
// For every index.json under the tree, and every entry in it:
//   - the manifest verifies against fleet-trust-roots.json (shape, target,
//     expiry, signature: sdn-updater/manifest.js validateUpdateManifest, the
//     same rules as the Go verifier), and the carrier and the bundle it wraps
//     match the manifest's hashes and size;
//   - its signing key is not a SEC-001 key;
//   - the bundle payload carries no trust/ (every node refuses one that does);
//   - the index entry agrees with the manifest, and its manifest and carrier
//     URLs lie under the index's own feed base.
// Exits non-zero on the first failure.
import { createHash, createPublicKey } from 'node:crypto';
import { mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { archiveEntries, trustDirs } from './build-cli-update-payload.mjs';
import { extractBundleBytes } from './build-update-carrier.mjs';
import { DEFAULT_ROOTS_PATH, loadSec001Keys } from './check-update-signing-key.mjs';

const require = createRequire(import.meta.url);
const { verifyDownloadedUpdatePayload } = require('./sdn-updater/manifest');

async function findIndexes(dir) {
  const found = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      found.push(...(await findIndexes(path)));
    } else if (entry.name === 'index.json') {
      found.push(path);
    }
  }
  return found.sort();
}

function underBase(base, raw) {
  const b = new URL(base);
  const u = new URL(raw);
  const basePath = b.pathname.replace(/\/$/, '');
  return u.protocol === 'https:' && u.protocol === b.protocol && u.host === b.host && u.pathname.startsWith(`${basePath}/`) && !u.search && !u.hash;
}

function rawKeyHex(encoded) {
  const der = Buffer.from(String(encoded), 'base64');
  const key = der.length === 32 ? der : createPublicKey({ key: der, format: 'der', type: 'spki' }).export({ type: 'spki', format: 'der' }).subarray(-32);
  return Buffer.from(key).toString('hex');
}

export async function verifyCliUpdateFeed({ feedDir, roots, denied, expectVersion, expectCount, now = new Date() }) {
  for (const [id, encoded] of Object.entries(roots)) {
    if (denied.has(rawKeyHex(encoded))) {
      throw new Error(`the trust roots list ${id}, a key of the SEC-001 leaked identity`);
    }
  }
  const indexes = await findIndexes(resolve(feedDir));
  if (indexes.length === 0) {
    throw new Error(`no index.json under ${feedDir}`);
  }
  const verified = [];
  for (const indexPath of indexes) {
    const index = JSON.parse(await readFile(indexPath, 'utf8'));
    const laneDir = indexPath.slice(0, -'/index.json'.length);
    for (const entry of index.updates || []) {
      const where = `${entry.update_id} (${indexPath})`;
      const manifest = JSON.parse(await readFile(join(laneDir, entry.version, 'manifest.json'), 'utf8'));
      const wasmBytes = await readFile(join(laneDir, entry.version, 'update.wasm'));
      const bundleBytes = extractBundleBytes(wasmBytes);
      verifyDownloadedUpdatePayload({
        manifest,
        wasmBytes,
        bundleBytes,
        platform: manifest.target.platform,
        arch: manifest.target.arch,
        currentSequence: -1,
        trustedRoots: roots,
        now,
      });
      if (denied.has(rawKeyHex(roots[manifest.signing.key_id]))) {
        throw new Error(`${where} is signed by a SEC-001 key`);
      }
      for (const [field, value] of [['update_id', manifest.update_id], ['version', manifest.version], ['sequence', manifest.sequence], ['channel', manifest.channel]]) {
        if (entry[field] !== value) {
          throw new Error(`${where}: index ${field} ${entry[field]} != manifest ${field} ${value}`);
        }
      }
      if (entry.bundle_hash && entry.bundle_hash !== manifest.bundle.hash) {
        throw new Error(`${where}: index bundle_hash != manifest bundle.hash`);
      }
      for (const url of [entry.manifest_url, entry.carrier_url]) {
        if (!underBase(index.feed_base_url, url)) {
          throw new Error(`${where}: ${url} is not under the feed base ${index.feed_base_url}`);
        }
      }
      if (expectVersion && manifest.version !== expectVersion) {
        throw new Error(`${where}: version ${manifest.version}, want ${expectVersion}`);
      }
      const work = await mkdtemp(join(tmpdir(), 'sdn-feed-verify-'));
      try {
        const archive = join(work, `bundle.${manifest.bundle.format}`);
        await writeFile(archive, bundleBytes);
        const trust = trustDirs(await archiveEntries(archive, manifest.bundle.format));
        if (trust.length > 0) {
          throw new Error(`${where}: the payload carries ${trust.join(', ')}, which every node refuses`);
        }
      } finally {
        await rm(work, { recursive: true, force: true });
      }
      verified.push({
        updateId: manifest.update_id,
        target: `${manifest.target.platform}/${manifest.target.arch}`,
        keyId: manifest.signing.key_id,
        bundleSha256: createHash('sha256').update(bundleBytes).digest('hex'),
      });
    }
  }
  if (expectCount && verified.length !== Number(expectCount)) {
    throw new Error(`verified ${verified.length} manifests, want ${expectCount}`);
  }
  return verified;
}

function parseArgs(argv) {
  const options = {};
  for (let i = 0; i < argv.length; i += 2) {
    const key = argv[i];
    const value = argv[i + 1];
    if (!key?.startsWith('--') || value === undefined) {
      throw new Error(`invalid argument near ${key}`);
    }
    options[key.slice(2).replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = value;
  }
  return options;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const options = parseArgs(process.argv.slice(2));
  try {
    if (!options.feedDir) {
      throw new Error('--feed-dir is required');
    }
    const verified = await verifyCliUpdateFeed({
      feedDir: options.feedDir,
      roots: JSON.parse(await readFile(resolve(options.roots || DEFAULT_ROOTS_PATH), 'utf8')),
      denied: await loadSec001Keys(),
      expectVersion: options.expectVersion,
      expectCount: options.expectCount,
    });
    for (const v of verified) {
      console.log(`verified ${v.updateId} ${v.target} key ${v.keyId} bundle ${v.bundleSha256}`);
    }
  } catch (error) {
    console.log(`::error title=Update feed verification::${error.message}`);
    process.exit(1);
  }
}
