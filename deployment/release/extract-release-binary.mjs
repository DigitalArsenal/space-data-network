#!/usr/bin/env node
/**
 * extract-release-binary — pull a built binary out of a Docker image/container
 * so that two extractions can NEVER interleave into one output path.
 *
 * This is the other half of the 2026-08-09 truncated-publish fix
 * (graph/tasks/sdn-publish-fleet-update-wraps-an-unverified-binary.md). The
 * publisher now refuses a binary that is moving under it, but refusing late is
 * a worse control than making the race impossible. The race was:
 *
 *     docker cp <container>:/out/spacedatanetwork ./buildout-spacedatanetwork   # job A
 *     docker cp <container>:/out/spacedatanetwork ./buildout-spacedatanetwork   # job B, superseding A
 *
 * `docker cp` writes the destination IN PLACE and non-atomically, so between
 * those two commands the path is a partially written file that every tool in
 * the lane will happily read.
 *
 * The fix is the standard one and it is not optional here:
 *
 *   1. an O_EXCL lock keyed on the DESTINATION — a second extraction of the
 *      same target refuses instead of interleaving;
 *   2. extraction into a unique temp dir on the SAME filesystem, never the
 *      destination;
 *   3. verification of the temp copy (ELF shape, self-declared extents, size
 *      floor) BEFORE it is allowed to become the destination;
 *   4. rename() into place — atomic, so a concurrent reader sees either the
 *      whole old file or the whole new one and never a prefix;
 *   5. refusal when the destination already exists with different content,
 *      unless --replace says that is intended.
 *
 * Usage:
 *   node deployment/release/extract-release-binary.mjs \
 *     --image sdn-build:latest --container-path /out/spacedatanetwork \
 *     --out ./buildout-spacedatanetwork [--arch amd64] [--replace] [--smoke]
 *
 *   node deployment/release/extract-release-binary.mjs \
 *     --container <name> --container-path /out/spacedatanetwork --out ./bin
 */
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, renameSync, rmSync, chmodSync } from 'node:fs';
import { dirname, join, resolve, basename } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  DAEMON_BINARY_MIN_BYTES,
  ReleaseBinaryRefusal,
  acquireExclusiveLock,
  assertElfNotTruncated,
  assertElfTarget,
  assertSizeFloor,
  readStableFile,
  smokeTestBinary,
} from './verify-release-binary.mjs';

/**
 * Extract `containerPath` out of a container/image to `out`, atomically.
 *
 * @param {object}   opts
 * @param {string}   [opts.image]          image to instantiate (mutually exclusive with container)
 * @param {string}   [opts.container]      existing container to copy from
 * @param {string}   opts.containerPath    path INSIDE the container
 * @param {string}   opts.out              destination path on the host
 * @param {string}   [opts.arch]           expected ELF arch (default amd64)
 * @param {number}   [opts.minBytes]       plausibility floor
 * @param {boolean}  [opts.replace]        allow overwriting a differing destination
 * @param {boolean}  [opts.smoke]          also run the artifact (--version) before installing it
 * @param {Function} [opts.docker]         injected docker runner (tests)
 * @param {Function} [opts.log]
 */
export function extractReleaseBinary({
  image,
  container,
  containerPath,
  out,
  arch = 'amd64',
  minBytes = DAEMON_BINARY_MIN_BYTES,
  replace = false,
  smoke = false,
  docker = defaultDocker,
  log = () => {},
}) {
  if (!containerPath) throw new ReleaseBinaryRefusal('--container-path is required');
  if (!out) throw new ReleaseBinaryRefusal('--out is required');
  if (!!image === !!container) {
    throw new ReleaseBinaryRefusal('exactly one of --image or --container is required');
  }

  const destination = resolve(out);
  const destinationDir = dirname(destination);
  if (!existsSync(destinationDir)) {
    throw new ReleaseBinaryRefusal(`destination directory does not exist: ${destinationDir}`);
  }

  // (1) The lock lives NEXT TO the destination and is keyed on it, because the
  // destination path — not the container, not the image — is the resource two
  // extractions contend for.
  const releaseLock = acquireExclusiveLock(`${destination}.lock`, {
    holder: `extract-release-binary ${image ?? container}:${containerPath}`,
    log,
  });

  // (2) Unique staging dir on the destination's own filesystem, so step (4)
  // can be a rename and not a copy. mkdtemp is itself race-free.
  const staging = mkdtempSync(join(destinationDir, `.${basename(destination)}.extract-`));
  const stagedPath = join(staging, basename(destination));

  let created = null;
  try {
    let sourceContainer = container;
    if (image) {
      sourceContainer = String(docker(['create', image])).trim().split('\n').pop().trim();
      if (!sourceContainer) {
        throw new ReleaseBinaryRefusal(`docker create ${image} returned no container id`);
      }
      created = sourceContainer;
      log(`[extract] created container ${sourceContainer.slice(0, 12)} from ${image}`);
    }

    docker(['cp', `${sourceContainer}:${containerPath}`, stagedPath]);
    log(`[extract] copied ${containerPath} -> ${stagedPath}`);

    // (3) Verify the STAGED copy. Nothing reaches the destination unverified.
    const stable = readStableFile(stagedPath, { settleMs: 0, log });
    assertElfTarget(stable.bytes, { path: stagedPath, arch });
    assertElfNotTruncated(stable.bytes, { path: stagedPath });
    assertSizeFloor(stable.bytes, { path: stagedPath, minBytes });
    chmodSync(stagedPath, 0o755);
    if (smoke) {
      smokeTestBinary(stagedPath, { log });
    }

    // (5) An unexpected destination is a signal, not an inconvenience: someone
    // else's artifact is sitting where ours is about to go.
    if (existsSync(destination)) {
      const current = readStableFile(destination, { settleMs: 0, log: () => {} });
      if (current.sha256 === stable.sha256) {
        log(`[extract] destination already holds these exact bytes (${stable.sha256.slice(0, 16)}) — nothing to do`);
        rmSync(staging, { recursive: true, force: true });
        return { path: destination, sha256: stable.sha256, size: stable.size, replaced: false, unchanged: true };
      }
      if (!replace) {
        throw new ReleaseBinaryRefusal(
          `${destination} already exists with DIFFERENT content ` +
            `(there: ${current.size}B sha ${current.sha256.slice(0, 16)}; new: ${stable.size}B sha ${stable.sha256.slice(0, 16)}). ` +
            'Refusing to overwrite an artifact this run did not produce — pass --replace if that is intended.',
          { destination, existing: current.sha256, incoming: stable.sha256 },
        );
      }
    }

    // (4) Atomic install. A reader either sees the old file or the new one.
    renameSync(stagedPath, destination);
    const landed = readStableFile(destination, { settleMs: 0, log: () => {} });
    if (landed.sha256 !== stable.sha256) {
      throw new ReleaseBinaryRefusal(
        `post-rename readback mismatch at ${destination}: expected ${stable.sha256}, got ${landed.sha256}`,
        { destination },
      );
    }
    log(`[extract] installed ${destination} ${landed.size}B sha256 ${landed.sha256}`);
    return { path: destination, sha256: landed.sha256, size: landed.size, replaced: true, unchanged: false };
  } finally {
    rmSync(staging, { recursive: true, force: true });
    if (created) {
      try {
        docker(['rm', '-f', created]);
      } catch {
        /* best effort */
      }
    }
    releaseLock();
  }
}

function defaultDocker(args) {
  return execFileSync('docker', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] });
}

function arg(argv, name, fallback) {
  const i = argv.indexOf(`--${name}`);
  if (i >= 0 && argv[i + 1] && !argv[i + 1].startsWith('--')) return argv[i + 1];
  return fallback;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const argv = process.argv.slice(2);
  try {
    const result = extractReleaseBinary({
      image: arg(argv, 'image'),
      container: arg(argv, 'container'),
      containerPath: arg(argv, 'container-path'),
      out: arg(argv, 'out'),
      arch: arg(argv, 'arch', 'amd64'),
      minBytes: Number(arg(argv, 'min-bytes', DAEMON_BINARY_MIN_BYTES)),
      replace: argv.includes('--replace'),
      smoke: argv.includes('--smoke'),
      log: (line) => console.error(line),
    });
    console.log(JSON.stringify(result, null, 2));
  } catch (error) {
    if (error instanceof ReleaseBinaryRefusal) {
      console.error(`\nREFUSED: ${error.message}\n`);
      process.exit(3);
    }
    throw error;
  }
}
