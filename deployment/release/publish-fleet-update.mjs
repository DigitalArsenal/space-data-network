#!/usr/bin/env node
/**
 * publish-fleet-update — the ONE driver for "every change/build reaches the
 * fleet through the update server, in place" (owner ruling 2026-08-01).
 *
 * Takes a locally built linux/amd64 binary (build-locally-ship-binaries law:
 * Docker buildx on the operator Mac, never on hosts) and drives the full
 * producer lane that heph-updater proved end-to-end on 2026-07-31:
 *
 *   0. VERIFY THE BINARY, under an exclusive lock, before anything else;
 *   1. stage the MINIMAL fleet bundle (bin/<exe> + manifest.json)
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
 * ---------------------------------------------------------------------------
 * WHY STEP 0 AND THE HASH CHAIN EXIST (incident 2026-08-09, graph task
 * sdn-publish-fleet-update-wraps-an-unverified-binary):
 *
 * This tool published, SIGNED and indexed a 265,425-byte truncated "daemon"
 * (1.3 % of its real size) as the newest sequence on the beta channel. Two
 * `docker cp` extractions had raced into one output path and the tool read the
 * file mid-write. Every gate in the lane passed it, because every gate asked
 * "does the manifest describe these bytes?" — and it did. Nothing asked
 * whether the bytes were a program. The signature made garbage authoritative,
 * and the fleet is built to install the newest signed sequence automatically.
 *
 * Two controls now make that class impossible:
 *
 *   VERIFY BEFORE SIGN (step 0). The binary must hold still while it is read,
 *   be a linux/amd64 ELF whose own header does not describe a file larger than
 *   itself, clear a plausibility floor, and load and run (`version`) in the
 *   target architecture. See verify-release-binary.mjs — that module carries
 *   the reasoning for each check. All of it happens before a signing key is
 *   contacted.
 *
 *   SOURCE LINEAGE (step 0b). Verified bytes are not enough: a perfectly good
 *   binary built from a branch that predates fixes already live is a silent
 *   revert, and it looks NEWER because `sequence` is publish time. The source
 *   commit must be a git descendant of the commit the feed's newest artifact
 *   was built from, unless --rollback names the intent. See source-lineage.mjs.
 *
 *   ONE HASH, END TO END (steps 1–6). The verified bytes are hashed once and
 *   that hash is re-proved at every boundary the artifact crosses: staged copy,
 *   inside the tar, inside the carrier, inside the manifest that comes back
 *   SIGNED, on the publisher host after upload, and finally in what the public
 *   URL actually serves — unwrapped all the way back down to the binary. A
 *   substitution anywhere in that chain is a hard failure, not a warning.
 *
 * The lock is the third control and it is upstream of both: a publish holds
 * the same exclusive lock on --binary that extract-release-binary.mjs takes,
 * so a publish can never run against a path something else is writing.
 * ---------------------------------------------------------------------------
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
 *     [--version-prefix 1.0.6-updatelane] \
 *     [--min-binary-bytes <n>] [--smoke-image <image>] \
 *     [--rollback "<why this deliberately reverts live code>"] \
 *     [--ledger-path /opt/spacedatanetwork/publish-ledger.log] \
 *     [--dry-run] [--no-smoke (dry-run only)]
 *
 * --smoke-image should name an image carrying the SDN runtime libraries (the
 * build image, e.g. sdn-builder:<tag>) for the strongest check: the daemon
 * links libwasmedge.so.0, which lives in LIBDIR outside the bundle, so in a
 * bare image the smoke test can only prove the artifact LOADS, not that it
 * runs. Both outcomes are reported as smokeLevel=ran|loaded.
 */
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, chmodSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildCarrier, extractBundleBytes } from './build-update-carrier.mjs';
import { buildUpdateManifest } from './sign-update-manifest.mjs';
import {
  DAEMON_BINARY_MIN_BYTES,
  ReleaseBinaryRefusal,
  acquireExclusiveLock,
  verifyReleaseBinary,
} from './verify-release-binary.mjs';
import {
  LINEAGE_ROLLBACK,
  SourceLineageRefusal,
  assertSourceLineage,
  resolveLiveSourceCommit,
} from './source-lineage.mjs';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');

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
const minBinaryBytes = Number(arg('min-binary-bytes', DAEMON_BINARY_MIN_BYTES));
const smokeImage = arg('smoke-image', 'debian:bookworm-slim');
const ledgerPath = arg('ledger-path', '/opt/spacedatanetwork/publish-ledger.log');
// --rollback takes its REASON as its value. A rollback with no stated reason is
// the thing we are trying to prevent, so there is no bare boolean form.
const rollbackReason = arg('rollback', '');
// --supersede takes its REASON as its value, for the same reason --rollback
// does: replacing a published artifact without saying why it was bad is the
// thing this guard exists to stop.
const supersedeReason = arg('supersede', '');
// --no-signal takes its REASON as its value, like every other opt-out here.
//
// OWNER RULING 2026-08-09: "pushing an update signal to all installs to upgrade
// in place... That's the point of the update server." Publishing an artifact
// and NOT telling the fleet leaves the boxes exactly where they were, which
// looks identical to a publish that never happened — so the push is the DEFAULT
// and skipping it has to be said out loud and recorded in the ledger line.
const noSignalReason = arg('no-signal', '');
const dryRun = flag('dry-run');
const noSmoke = flag('no-smoke');

// --no-smoke exists so the regression tests can drive the real code path
// without Docker. It is refused on a real publish: an artifact that has not
// been executed has not been verified, and the ONE lane to the fleet does not
// take that on trust.
if (noSmoke && !dryRun) {
  console.error(
    'REFUSED: --no-smoke is only permitted with --dry-run. A published artifact must be executed ' +
      'in its target architecture before it is signed.',
  );
  process.exit(2);
}

const resolvedBinary = resolve(binaryPath);
// Mutable: a --supersede publish mints a NEW identity in step 0c rather than
// overwriting the published one. Everything downstream (bundle name, manifest,
// signing, upload path, index, ledger) reads these AFTER that decision.
let version = `${versionPrefix}.${sourceCommit}`;
let updateId = `sdn-cli-bundle-${version}`;
// Set by step 0c to the version this publish revokes, if any.
let supersededVersion = '';
const sequence = Math.floor(Date.now() / 1000);
const createdAt = new Date().toISOString();
const expiresAt = new Date(Date.now() + 365 * 24 * 3600 * 1000).toISOString();

const sha256 = (b) => createHash('sha256').update(b).digest('hex');
const run = (cmd, args, opts = {}) =>
  execFileSync(cmd, args, { stdio: ['ignore', 'pipe', 'inherit'], ...opts });
const log = (line) => console.error(line);

/**
 * Fetch a URL and report the HTTP status SEPARATELY from transport failure.
 *
 * Deliberately not `curl -f`: with -f a 404 and a DNS failure both surface as a
 * non-zero exit, and the lineage guard must distinguish "nothing is published
 * here yet" (proceed as an initial publish) from "I could not look" (refuse).
 * Collapsing them would let a flaky network silently disarm the guard.
 */
function fetchWithStatus(url) {
  const bodyPath = join(tmpdir(), `sdn-fetch-${process.pid}-${Date.now()}`);
  try {
    const status = run('curl', ['-s', '-m', '30', '-o', bodyPath, '-w', '%{http_code}', url]).toString().trim();
    const body = readFileSync(bodyPath, 'utf8');
    return { status: Number(status), body };
  } catch (error) {
    // curl itself failed: DNS, connect, TLS, timeout. No HTTP status exists.
    return { status: 0, body: '', error: error.message };
  } finally {
    rmSync(bodyPath, { force: true });
  }
}

const shellQuote = (value) => `'${String(value).replace(/'/g, `'\\''`)}'`;
const dirnamePosix = (p) => {
  const i = String(p).lastIndexOf('/');
  return i <= 0 ? '/' : String(p).slice(0, i);
};

/**
 * Every boundary the artifact crosses gets its own assertion. `where` names
 * the boundary so a failure says which hop substituted the bytes.
 */
function assertSameBytes(where, actualHash, expectedHash, extra = '') {
  if (actualHash !== expectedHash) {
    throw new ReleaseBinaryRefusal(
      `HASH CHAIN BROKEN at "${where}": expected ${expectedHash}, got ${actualHash}${extra ? ` (${extra})` : ''}. ` +
        'The bytes changed between two stages of the publish — nothing further may be signed or published.',
      { where, expected: expectedHash, actual: actualHash },
    );
  }
}

let releaseLock = () => {};
let work = null;

/**
 * Always drop the lock and the scratch tree, on every exit path. A lock left
 * behind by a crashed publish is recognised as stale by the next run (the pid
 * is gone), but a lock left behind by a SUCCESSFUL one would block the lane
 * behind a process that is very much alive.
 */
function cleanup() {
  if (work) {
    rmSync(work, { recursive: true, force: true });
    work = null;
  }
  releaseLock();
}

try {
  // --- 0. LOCK + VERIFY THE SOURCE BINARY ------------------------------------
  // The lock is keyed on the binary path and is the SAME lock
  // extract-release-binary.mjs takes, so an extraction still in flight (the
  // 2026-08-09 mechanism) and a second concurrent publish both refuse here
  // rather than interleaving.
  releaseLock = acquireExclusiveLock(`${resolvedBinary}.lock`, {
    holder: `publish-fleet-update ${updateId}`,
    log,
  });

  const verified = verifyReleaseBinary({
    path: resolvedBinary,
    arch,
    minBytes: minBinaryBytes,
    smoke: !noSmoke,
    // `version` is the daemon's subcommand — it has no --version FLAG, and
    // using one would make every smoke test fail on a perfectly good binary.
    smokeOptions: { image: smokeImage, platform: `linux/${arch}`, args: ['version'] },
    log,
  });
  // From here on the VERIFIED BUFFER is the only source of the binary. The
  // path is never read again: re-reading is exactly how a verified artifact
  // and a published artifact come apart.
  const binaryBytes = verified.bytes;
  const binarySha = verified.sha256;
  log(
    `[fleet-update] verified binary ${verified.size}B sha256 ${binarySha} ` +
      `smoke=${verified.smokeLevel}${verified.version ? ` (${verified.version.split('\n')[0]})` : ''}`,
  );

  // --- 0b. SOURCE LINEAGE ----------------------------------------------------
  // Verified bytes are not enough: a perfectly good binary built from a branch
  // that predates fixes already live on the fleet is a silent revert, and it
  // looks NEWER because sequence is publish time. See source-lineage.mjs.
  const live = resolveLiveSourceCommit({
    feedBaseUrl,
    channel,
    platform,
    arch,
    versionPrefix,
    fetch: fetchWithStatus,
  });
  const lineage = assertSourceLineage({
    repoRoot,
    sourceCommit,
    liveCommit: live?.commit ?? null,
    rollbackReason,
  });
  if (live) {
    log(
      `[fleet-update] lineage ${lineage.lineage.toUpperCase()}: ${sourceCommit} vs live ` +
        `${live.commit} (${live.version}, via ${live.via})`,
    );
  } else {
    log('[fleet-update] lineage INITIAL: the feed carries no prior artifact to descend from');
  }
  if (lineage.lineage === LINEAGE_ROLLBACK) {
    log(`[fleet-update] *** DECLARED ROLLBACK *** reason: ${lineage.reason}`);
    log('[fleet-update] hosts will refuse this update unless installed with --allow-rollback');
  }

  // --- 0c. PUBLISHED ARTIFACTS ARE IMMUTABLE ---------------------------------
  // version derives from the source commit, so republishing the same commit
  // lands in the SAME feed directory and overwrites manifest.json and
  // update.wasm IN PLACE. That is what the 2026-08-09 correction did: a
  // truncated 265,425-byte payload went out as 1.0.6-updatelane.1873884f and
  // the corrected 20 MB payload was written over the top of it. The feed ended
  // up serving the right bytes, and the correction was still unsound:
  //
  //   1. The URL never changed, so a client that already fetched
  //      .../1.0.6-updatelane.1873884f/update.wasm has NO change signal.
  //      Whether it ever picks up the fix becomes a question about HTTP cache
  //      headers rather than about the feed.
  //   2. The bad artifact's manifest, hash and sequence were DESTROYED rather
  //      than superseded. Nothing records that it existed, so nothing can
  //      revoke it and no audit can find it afterwards.
  //   3. Worst: update_id stopped being a stable name for a fixed set of
  //      bytes. Hosts persist update_id + sequence in updates/state.json —
  //      including as the `previous` rollback target — so an in-place
  //      overwrite silently changes what an already-recorded identifier means
  //      on every box that holds it.
  //
  // This check runs BEFORE anything is built, hashed, smoke-tested or signed,
  // so a correction costs a clear refusal instead of a wasted publish, and it
  // reads the PUBLIC index rather than the publisher's disk — the feed as the
  // fleet sees it is the thing that has to stay immutable.
  {
    const indexUrl = `${feedBaseUrl}/cli-bundle/${channel}/${platform}/${arch}/index.json`;
    const res = fetchWithStatus(indexUrl);
    if (res.status === 200) {
      let published = [];
      try {
        published = JSON.parse(res.body)?.updates ?? [];
      } catch {
        published = [];
      }
      const clash = published.find((u) => u.version === version);
      if (clash) {
        if (!supersedeReason) {
          console.error(
            `REFUSED: ${version} IS ALREADY PUBLISHED and published artifacts are immutable.\n` +
              `  in the feed : ${clash.update_id} sequence ${clash.sequence} bundle ${clash.bundle_hash}\n` +
              `  this publish: would reuse that exact URL and update_id.\n` +
              `Overwriting it gives cached clients no change signal, destroys the record of what was\n` +
              `previously served under that name, and silently changes what ${clash.update_id} denotes\n` +
              `on every host that already recorded it in updates/state.json.\n` +
              `If the published artifact is BAD and this is the correction, say so — it will be\n` +
              `published under a NEW version, NEW sequence and NEW URL, and the old one will be\n` +
              `marked revoked rather than erased:\n` +
              `  --supersede "<why the published artifact is bad>"`,
          );
          process.exit(3);
        }
        supersededVersion = version;
        const priorRevisions = published.filter((u) =>
          String(u.version ?? '').startsWith(`${version}+r`),
        ).length;
        version = `${version}+r${priorRevisions + 2}`;
        updateId = `sdn-cli-bundle-${version}`;
        log(
          `[fleet-update] *** SUPERSEDING *** ${supersededVersion} ` +
            `(${clash.update_id}, sequence ${clash.sequence})`,
        );
        log(`[fleet-update]   reason: ${supersedeReason}`);
        log(`[fleet-update]   new identity: ${updateId} sequence ${sequence} — new URL, nothing overwritten`);
      }
    }
  }

  // --- 1. minimal fleet bundle ----------------------------------------------
  work = mkdtempSync(join(tmpdir(), 'sdn-fleet-update-'));
  const bundleName = `spacedatanetwork-${version}-${platform}-${arch}`;
  const bundleRoot = join(work, bundleName);
  const stagedBinary = join(bundleRoot, 'bin', 'spacedatanetwork');
  mkdirSync(join(bundleRoot, 'bin'), { recursive: true });
  writeFileSync(stagedBinary, binaryBytes);
  chmodSync(stagedBinary, 0o755);
  assertSameBytes('staged bundle copy', sha256(readFileSync(stagedBinary)), binarySha);

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
          binarySha256: binarySha,
          binarySize: verified.size,
        },
      },
      null,
      2,
    )}\n`,
  );

  // --- 2. archive + carrier --------------------------------------------------
  const archivePath = join(work, `${bundleName}.tar.gz`);
  run('tar', ['-czf', archivePath, bundleName], { cwd: work });
  const bundleBytes = readFileSync(archivePath);
  const bundleHash = sha256(bundleBytes);

  // Unpack the archive we just made and hash the binary INSIDE it. tar is not
  // suspected of lying; the point is that from here the published artifact is
  // the archive, and this is the last moment the binary can be recovered from
  // it and compared to what was verified.
  const roundTrip = join(work, 'roundtrip');
  mkdirSync(roundTrip, { recursive: true });
  run('tar', ['-xzf', archivePath, '-C', roundTrip]);
  assertSameBytes(
    'binary re-extracted from the tar.gz',
    sha256(readFileSync(join(roundTrip, bundleName, 'bin', 'spacedatanetwork'))),
    binarySha,
  );

  const wasmBytes = buildCarrier(bundleBytes);
  const wasmHash = sha256(wasmBytes);
  assertSameBytes('bundle re-extracted from the wasm carrier', sha256(extractBundleBytes(wasmBytes)), bundleHash);
  log(
    `[fleet-update] bundle ${bundleBytes.length}B sha ${bundleHash.slice(0, 16)} ` +
      `carrier ${wasmBytes.length}B sha ${wasmHash.slice(0, 16)}`,
  );

  // --- 3. unsigned manifest --------------------------------------------------
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
    // Echoed so an operator can compare the FEED against their local build
    // without unpacking the carrier — the check that caught this incident by
    // hand is now a published field.
    provenance: {
      source_repository: 'DigitalArsenal/space-data-network',
      source_commit: sourceCommit,
      supersedes_commit: lineage.supersedesCommit,
      lineage: lineage.lineage,
      ...(lineage.reason ? { rollback_reason: lineage.reason } : {}),
      binary_sha256: binarySha,
      binary_size: verified.size,
    },
  });
  assertSameBytes('unsigned manifest bundle hash', manifest.bundle.hash, bundleHash);
  assertSameBytes('unsigned manifest wasm hash', manifest.wasm.hash, wasmHash);
  if (manifest.bundle.size !== bundleBytes.length) {
    throw new ReleaseBinaryRefusal(
      `unsigned manifest bundle size ${manifest.bundle.size} != ${bundleBytes.length}`,
    );
  }

  const unsignedPath = join(work, 'manifest.unsigned.json');
  writeFileSync(unsignedPath, `${JSON.stringify(manifest, null, 2)}\n`);

  if (dryRun) {
    console.log(
      JSON.stringify(
        {
          dryRun: true,
          updateId,
          version,
          sequence,
          work,
          binarySha256: binarySha,
          binarySize: verified.size,
          smokeLevel: verified.smokeLevel,
          bundleHash,
          bundleSize: bundleBytes.length,
          wasmHash,
          wasmSize: wasmBytes.length,
          lineage: lineage.lineage,
          sourceCommit: lineage.sourceCommit,
          supersedesCommit: lineage.supersedesCommit,
          ...(lineage.reason ? { rollbackReason: lineage.reason } : {}),
          // Present only when this publish replaces a published artifact, so a
          // correction is distinguishable from an ordinary roll without log parsing.
          ...(supersededVersion ? { superseded: supersededVersion, supersedeReason } : {}),
        },
        null,
        2,
      ),
    );
    cleanup();
    process.exit(0);
  }

  // --- 4. node-signed on the publisher host ----------------------------------
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
  // What came back SIGNED must still be a statement about our artifact. A
  // signature is only as good as the document it covers, and that document
  // just made a round trip through another machine.
  assertSameBytes('signed manifest bundle hash', signed.bundle?.hash, bundleHash);
  assertSameBytes('signed manifest wasm hash', signed.wasm?.hash, wasmHash);
  assertSameBytes('signed manifest binary provenance', signed.provenance?.binary_sha256, binarySha);
  if (signed.bundle?.size !== bundleBytes.length || signed.update_id !== updateId || signed.sequence !== sequence) {
    throw new ReleaseBinaryRefusal(
      `signed manifest does not describe this publish (update_id=${signed.update_id} sequence=${signed.sequence} ` +
        `bundle.size=${signed.bundle?.size}; expected ${updateId} / ${sequence} / ${bundleBytes.length})`,
    );
  }

  // --- 5. publish payload + regenerate index ---------------------------------
  const feedRel = `cli-bundle/${channel}/${platform}/${arch}`;
  const payloadRemote = `${feedDir}/${feedRel}/${version}`;

  // Immutability was decided in step 0c, before anything was built or signed.
  // If that step minted a superseding identity, mark the artifact it replaces
  // revoked, in its OWN directory, so the record travels with the artifact and
  // the index regeneration below picks it up.
  if (supersededVersion) {
    const oldRemote = `${feedDir}/${feedRel}/${supersededVersion}`;
    const revocation = JSON.stringify({
      schema: 'org.spacedatanetwork.update.revocation.v1',
      revoked_at: createdAt,
      reason: supersedeReason,
      superseded_by: updateId,
      superseded_by_sequence: sequence,
    });
    run('ssh', [publisherSSH, `printf '%s' ${shellQuote(revocation)} > ${oldRemote}/revoked.json`]);
    log(`[fleet-update] revoked ${supersededVersion}; superseded by ${updateId}`);
  }

  run('ssh', [publisherSSH, `mkdir -p ${payloadRemote}`]);
  run('scp', ['-q', signedPath, `${publisherSSH}:${payloadRemote}/manifest.json`]);
  const carrierLocal = join(work, 'update.wasm');
  writeFileSync(carrierLocal, wasmBytes);
  run('scp', ['-q', carrierLocal, `${publisherSSH}:${payloadRemote}/update.wasm`]);

  // The bytes ON THE HOST, hashed BY the host. scp reports success on a short
  // write to a full disk; this does not.
  const remoteSum = run('ssh', [
    publisherSSH,
    `sha256sum ${payloadRemote}/update.wasm | cut -d' ' -f1; stat -c %s ${payloadRemote}/update.wasm`,
  ])
    .toString()
    .trim()
    .split('\n');
  assertSameBytes('carrier as stored on the publisher host', remoteSum[0]?.trim(), wasmHash);
  if (Number(remoteSum[1]) !== wasmBytes.length) {
    throw new ReleaseBinaryRefusal(
      `uploaded carrier is ${remoteSum[1]}B on ${publisherSSH}, expected ${wasmBytes.length}B`,
    );
  }

  // Regenerate index.json from every manifest present (newest first by
  // sequence) so history stays listed and rollback targets stay resolvable.
  // The entries carry bundle_size and wasm_size alongside the hashes so a
  // consumer — or a human, or a monitor — can spot an implausible artifact
  // from the index alone, without downloading 20 MB to find out.
  const indexScript = `
import json, glob, os, sys
base = ${JSON.stringify(`${feedDir}/${feedRel}`)}
url_base = ${JSON.stringify(`${feedBaseUrl}/${feedRel}`)}
updates = []
for mp in glob.glob(base + '/*/manifest.json'):
    m = json.load(open(mp))
    v = m['version']
    carrier = os.path.join(os.path.dirname(mp), 'update.wasm')
    entry = {
        'update_id': m['update_id'],
        'version': v,
        'sequence': m['sequence'],
        'channel': m['channel'],
        'target': m['target'],
        'expires_at': m['expires_at'],
        'bundle_hash': m['bundle']['hash'],
        'bundle_size': m['bundle'].get('size'),
        'wasm_hash': m['wasm']['hash'],
        'signing_key_id': m['signing']['key_id'],
        'manifest_url': f'{url_base}/{v}/manifest.json',
        'carrier_url': f'{url_base}/{v}/update.wasm',
    }
    if os.path.exists(carrier):
        entry['wasm_size'] = os.path.getsize(carrier)
    # source_commit is what the NEXT publish tests ancestry against, so it is
    # lifted into the index: resolving it must not require fetching a manifest
    # per candidate. lineage is surfaced so a rollback is visible in a listing.
    prov = m.get('provenance') or {}
    if prov.get('source_commit'):
        entry['source_commit'] = prov['source_commit']
    if prov.get('lineage'):
        entry['lineage'] = prov['lineage']
    # A superseded artifact stays LISTED but marked revoked. Erasing it is what
    # made the 2026-08-09 truncated publish unauditable: a consumer can refuse a
    # revoked entry, but an absent one it can only fail to notice.
    rev = os.path.join(os.path.dirname(mp), 'revoked.json')
    if os.path.exists(rev):
        entry['revoked'] = json.load(open(rev))
    updates.append(entry)
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

  // --- 6. verify the public surface serves what we built ---------------------
  // Not just "the manifest quotes our hash" — the manifest is a claim. Fetch
  // the CARRIER the fleet will fetch, unwrap it, and walk it back down to the
  // binary. This is the assertion that would have failed loudly on 2026-08-09.
  const pubManifest = run('curl', ['-s', '-m', '30', `${feedBaseUrl}/${feedRel}/${version}/manifest.json`]);
  const pubIndex = run('curl', ['-s', '-m', '30', `${feedBaseUrl}/${feedRel}/index.json`]);
  const pm = JSON.parse(pubManifest.toString());
  const pi = JSON.parse(pubIndex.toString());
  assertSameBytes('public manifest bundle hash', pm.bundle?.hash, bundleHash);
  assertSameBytes('public manifest wasm hash', pm.wasm?.hash, wasmHash);
  assertSameBytes('public manifest signature', pm.signing?.signature, signed.signing.signature);

  const servedCarrierPath = join(work, 'served.wasm');
  run('curl', ['-s', '-m', '300', '-o', servedCarrierPath, `${feedBaseUrl}/${feedRel}/${version}/update.wasm`]);
  const servedCarrier = readFileSync(servedCarrierPath);
  assertSameBytes('carrier served by the public feed', sha256(servedCarrier), wasmHash, `${servedCarrier.length}B`);
  const servedBundle = extractBundleBytes(servedCarrier);
  assertSameBytes('bundle inside the served carrier', sha256(servedBundle), bundleHash);

  const servedDir = join(work, 'served');
  mkdirSync(servedDir, { recursive: true });
  writeFileSync(join(work, 'served.tar.gz'), servedBundle);
  run('tar', ['-xzf', join(work, 'served.tar.gz'), '-C', servedDir]);
  assertSameBytes(
    'binary inside the bundle the public feed serves',
    sha256(readFileSync(join(servedDir, bundleName, 'bin', 'spacedatanetwork'))),
    binarySha,
  );

  const indexEntry = pi.updates.find((u) => u.update_id === updateId && u.sequence === sequence);
  if (!indexEntry) {
    throw new Error('public index does not list the new update');
  }
  assertSameBytes('public index bundle hash', indexEntry.bundle_hash, bundleHash);
  assertSameBytes('public index wasm hash', indexEntry.wasm_hash, wasmHash);
  if (indexEntry.bundle_size !== bundleBytes.length || indexEntry.wasm_size !== wasmBytes.length) {
    throw new ReleaseBinaryRefusal(
      `public index sizes disagree with the publish (bundle ${indexEntry.bundle_size}/${bundleBytes.length}, ` +
        `wasm ${indexEntry.wasm_size}/${wasmBytes.length})`,
    );
  }

  // --- 7. ledger -------------------------------------------------------------
  // One append-only line per publish, on the publisher host. The 2026-08-09
  // ops P1 (ops-host01-unledgered-rolls-and-fleet-skew) turned on exactly this:
  // when a change leaves no ledger line, the next agent cannot tell what is
  // running, what it superseded, or who put it there — and a halted roll that
  // executes anyway becomes unattributable. A declared rollback is recorded AS
  // a rollback, with its reason, because that is the line someone will need.
  const ledgerLine = JSON.stringify({
    at: new Date().toISOString(),
    event: 'publish-fleet-update',
    update_id: updateId,
    version,
    sequence,
    channel,
    target: `${platform}/${arch}`,
    source_commit: lineage.sourceCommit,
    supersedes_commit: lineage.supersedesCommit,
    lineage: lineage.lineage,
    ...(lineage.reason ? { rollback_reason: lineage.reason } : {}),
    binary_sha256: binarySha,
    binary_size: verified.size,
    smoke_level: verified.smokeLevel,
    bundle_sha256: bundleHash,
    wasm_sha256: wasmHash,
    published_by: process.env.USER || 'unknown',
    // Whether the fleet was TOLD. A published-but-unsignalled artifact and a
    // published-and-pushed one look identical on the feed and are completely
    // different events on the boxes.
    signal: noSignalReason ? 'skipped' : 'pushed',
    ...(noSignalReason ? { signal_skipped_reason: noSignalReason } : {}),
  });
  try {
    run('ssh', [
      publisherSSH,
      // shellQuote, not JSON.stringify, for the PATHS too: a JSON string is
      // double-quoted, and a double-quoted shell word still expands $, `` and
      // backslashes. Single-quoting is the only literal form, and these strings
      // are being handed to a remote shell.
      `mkdir -p ${shellQuote(dirnamePosix(ledgerPath))} && ` +
        `printf '%s\\n' ${shellQuote(ledgerLine)} >> ${shellQuote(ledgerPath)}`,
    ]);
    log(`[fleet-update] ledgered -> ${publisherSSH}:${ledgerPath}`);
  } catch (error) {
    // A failed ledger write must be visible, but it must not orphan an update
    // that is already published and verified — the operator needs to know both
    // facts, not lose one of them.
    console.error(`[fleet-update] WARNING: could not write the publish ledger line: ${error.message}`);
    console.error(`[fleet-update] record this by hand on ${publisherSSH}:${ledgerPath}:\n${ledgerLine}`);
  }

  // --- 8. PUSH ---------------------------------------------------------------
  // A publish puts the artifact where the fleet CAN get it. The signal is what
  // makes the fleet GO and get it. Until 2026-08-09 this step did not exist and
  // a human supplied it by ssh'ing to each box and typing `update install` —
  // which is polling, performed by a person.
  //
  // It runs LAST, after the served bytes have been re-fetched and proved to
  // match, because a signal is an instruction to install and must never point
  // at something this publish has not yet verified is actually being served.
  let signalled = false;
  let signalTopic = '';
  if (dryRun) {
    log('[fleet-update] dry run: no signal pushed');
  } else if (noSignalReason) {
    log(`[fleet-update] SIGNAL SKIPPED (recorded reason): ${noSignalReason}`);
    log('[fleet-update] the artifact is published but NO install has been told to fetch it.');
    log(`[fleet-update] push it later with: ssh ${publisherSSH} ${publisherBin} update signal --channel ${channel}`);
  } else {
    try {
      const signalOut = run('ssh', [
        publisherSSH,
        // Deliberately NO --node-url: that routes the admin call through the
        // PUBLIC hostname, i.e. through the CDN, and the first real push
        // (2026-08-09) came back as a Cloudflare 502 with the origin perfectly
        // healthy. The deploy control plane has no business traversing a CDN
        // to reach a daemon on the same box. Without it the client dials
        // loopback and anchors verification to the certificate the daemon's own
        // config declares (newAdminClient -> daemonTLSConfig), which is both
        // shorter and strictly better authenticated.
        `${shellQuote(publisherBin)} update signal --channel ${shellQuote(channel)} ` +
          `--update-id ${shellQuote(updateId)} --platform ${shellQuote(platform)} --arch ${shellQuote(arch)}`,
      ]).toString();
      signalled = /published=true/.test(signalOut);
      signalTopic = (signalOut.match(/^topic=(.+)$/m) || [])[1] || '';
      if (!signalled) {
        throw new Error(`the publisher did not report published=true:\n${signalOut}`);
      }
      log(`[fleet-update] SIGNAL PUSHED on ${signalTopic} -> every subscribed install upgrades itself`);
    } catch (error) {
      // Loud, and NOT fatal: the artifact is published, verified and serving.
      // Failing the whole publish here would leave a good artifact behind a red
      // exit code, and the recovery is one command.
      console.error(`[fleet-update] WARNING: the artifact is published but the SIGNAL FAILED: ${error.message}`);
      console.error(
        `[fleet-update] the fleet has NOT been told. Push it with:\n` +
          `  ssh ${publisherSSH} ${publisherBin} update signal --channel ${channel} --update-id ${updateId}`,
      );
    }
  }

  console.log(
    JSON.stringify(
      {
        published: updateId,
        version,
        sequence,
        signalled,
        ...(signalTopic ? { signalTopic } : {}),
        ...(noSignalReason ? { signalSkippedReason: noSignalReason } : {}),
        // Present only when this publish replaced a published artifact, so a
        // caller can tell a correction from an ordinary roll without parsing logs.
        ...(supersededVersion ? { superseded: supersededVersion, superseded_reason: supersedeReason } : {}),
        binarySha256: binarySha,
        binarySize: verified.size,
        smokeLevel: verified.smokeLevel,
        bundleHash,
        bundleSize: bundleBytes.length,
        wasmHash,
        wasmSize: wasmBytes.length,
        lineage: lineage.lineage,
        sourceCommit: lineage.sourceCommit,
        supersedesCommit: lineage.supersedesCommit,
        ...(lineage.reason ? { rollbackReason: lineage.reason } : {}),
        verifiedThrough: 'served carrier -> bundle -> binary',
        feed: `${feedBaseUrl}/${feedRel}/`,
      },
      null,
      2,
    ),
  );
} catch (error) {
  cleanup();
  if (error instanceof ReleaseBinaryRefusal || error instanceof SourceLineageRefusal) {
    // Exit 3 is the lane's "the artifact was refused" code, distinct from a
    // crash (1) and from bad arguments (2), so a wrapper can tell a defective
    // artifact from a broken tool.
    console.error(`\nREFUSED: ${error.message}\n`);
    process.exit(3);
  }
  throw error;
}

cleanup();
