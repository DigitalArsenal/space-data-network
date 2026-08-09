/**
 * verify-release-binary — the gate that stands between a locally built
 * artifact and the fleet's signed update lane.
 *
 * WHY THIS FILE EXISTS (incident 2026-08-09,
 * graph/tasks/sdn-publish-fleet-update-wraps-an-unverified-binary.md):
 * two `docker cp` extractions raced into one output path. While the second was
 * mid-write, `publish-fleet-update.mjs` read the file and published, SIGNED and
 * indexed a 265,425-byte "daemon" — 1.3 % of its real size — as the newest
 * sequence on the beta channel. EVERY existing gate passed it, because every
 * gate checked CONSISTENCY (does the manifest describe these bytes?) and none
 * checked PLAUSIBILITY (are these bytes a daemon at all?). A signature over
 * garbage is a correctly signed piece of garbage.
 *
 * The rule this module encodes: verification is against MEASURED REALITY, not
 * against intent, and it happens BEFORE the bytes reach a signing key.
 *
 * Four independent checks, cheapest first, each catching a class the others
 * cannot:
 *
 *   1. STABILITY   — the file must not change while we read it. This is the
 *                    only check that catches the actual incident's mechanism
 *                    (a writer still running), and it is the only one that can:
 *                    a half-written file is internally consistent with itself
 *                    at every instant.
 *   2. ELF SHAPE   — magic/class/endianness/machine. Catches "wrong file
 *                    entirely" (a tarball, a script, a darwin binary) outright.
 *   3. ELF EXTENT  — the header's OWN declared extents (program headers,
 *                    section headers, segment file ranges) must fit inside the
 *                    file. THIS is the structural truncation detector: a
 *                    truncated ELF still has valid magic, but its section
 *                    header table claims to live at an offset past EOF. It
 *                    needs no threshold and no knowledge of the expected size,
 *                    so it cannot be tuned wrong.
 *   4. SIZE FLOOR  — a blunt plausibility bound for this specific artifact.
 *                    Redundant with 3 for truncation, but it also catches a
 *                    *complete* build of the wrong (tiny) program.
 *   5. SMOKE TEST  — the artifact actually runs, in the arch it targets. The
 *                    last thing a publisher can do that a manifest cannot fake.
 *
 * Nothing here is specific to the update lane; the publisher composes it.
 */
import { execFileSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { closeSync, linkSync, openSync, readFileSync, statSync, unlinkSync, writeSync } from 'node:fs';

/**
 * Plausibility floor for the SDN daemon binary. The real linux/amd64 build is
 * ~56 MB and has grown monotonically across the whole 1.0.6 update lane (the
 * live feed's 45 bundles run 20.1–20.9 MB compressed, i.e. ~54–56 MB
 * uncompressed). 20 MB is far below any real build and far above any
 * truncation or wrong-artifact mistake, so it refuses the defect class without
 * ever refusing a legitimate smaller build.
 *
 * A floor is a LOWER bound on purpose: shipping a suspiciously *large* binary
 * is not the failure mode that hurt us, and an upper bound would eventually
 * fire on a legitimate growth step and teach operators to pass --force.
 */
export const DAEMON_BINARY_MIN_BYTES = 20 * 1024 * 1024;

const ELF_MAGIC = Buffer.from([0x7f, 0x45, 0x4c, 0x46]);
const ELFCLASS64 = 2;
const ELFDATA2LSB = 1;
const EM_X86_64 = 0x3e;
const EM_AARCH64 = 0xb7;
const ET_EXEC = 2;
const ET_DYN = 3;

const MACHINE_BY_ARCH = {
  amd64: EM_X86_64,
  x86_64: EM_X86_64,
  arm64: EM_AARCH64,
  aarch64: EM_AARCH64,
};

export const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');

export class ReleaseBinaryRefusal extends Error {
  constructor(message, detail = {}) {
    super(message);
    this.name = 'ReleaseBinaryRefusal';
    Object.assign(this, detail);
  }
}

const refuse = (message, detail) => {
  throw new ReleaseBinaryRefusal(message, detail);
};

/**
 * Read a file and prove it did not change underneath us.
 *
 * The incident's binary was mid-write when it was read. No property OF the
 * bytes can detect that in general — you have to observe the file twice. We
 * bracket the read with stat() and compare identity (dev/ino), size, and
 * mtime, then re-read and re-hash to close the window where a writer replaces
 * the whole file between the two stats with something of equal length.
 *
 * `settleMs` gives an in-flight writer a moment to advance, so a slow writer
 * producing a file that happens to be stat-identical across a fast read still
 * gets caught by the size moving.
 */
export function readStableFile(path, { settleMs = 250, log = () => {} } = {}) {
  const before = statSync(path);
  if (!before.isFile()) {
    refuse(`not a regular file: ${path}`, { path });
  }
  const first = readFileSync(path);
  const firstHash = sha256(first);

  if (settleMs > 0) {
    // Synchronous sleep: this module is used by a CLI publisher, and the whole
    // point is to hold still and look again.
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, settleMs);
  }

  const after = statSync(path);
  const second = readFileSync(path);
  const secondHash = sha256(second);

  const drift = [];
  if (before.size !== after.size) drift.push(`size ${before.size} -> ${after.size}`);
  if (before.ino !== after.ino) drift.push(`inode ${before.ino} -> ${after.ino}`);
  if (before.dev !== after.dev) drift.push(`device ${before.dev} -> ${after.dev}`);
  if (before.mtimeMs !== after.mtimeMs) {
    drift.push(`mtime ${new Date(before.mtimeMs).toISOString()} -> ${new Date(after.mtimeMs).toISOString()}`);
  }
  if (firstHash !== secondHash) drift.push(`sha256 ${firstHash.slice(0, 16)} -> ${secondHash.slice(0, 16)}`);

  if (drift.length > 0) {
    refuse(
      `${path} CHANGED while it was being read (${drift.join(', ')}) — something is still writing it. ` +
        'This is the 2026-08-09 truncated-publish mechanism: refusing rather than publishing a snapshot of a partial write.',
      { path, drift, firstHash, secondHash },
    );
  }

  log(`[verify] stable read ${path} ${second.length}B sha ${secondHash.slice(0, 16)}`);
  return { bytes: second, sha256: secondHash, size: second.length, stat: after };
}

/**
 * Parse the ELF identification + header. Returns null-free structured fields,
 * or refuses if the file is not an ELF at all.
 */
export function inspectElf(bytes, { path = '<bytes>' } = {}) {
  if (bytes.length < 64) {
    refuse(`${path} is ${bytes.length}B — too small to even contain an ELF header (64B)`, {
      path,
      size: bytes.length,
    });
  }
  if (!bytes.subarray(0, 4).equals(ELF_MAGIC)) {
    refuse(
      `${path} is not an ELF executable (magic ${[...bytes.subarray(0, 4)]
        .map((b) => b.toString(16).padStart(2, '0'))
        .join(' ')}) — wrong artifact entirely`,
      { path },
    );
  }
  const eiClass = bytes[4];
  const eiData = bytes[5];
  const eType = bytes.readUInt16LE(16);
  const eMachine = bytes.readUInt16LE(18);
  const ePhoff = Number(bytes.readBigUInt64LE(32));
  const eShoff = Number(bytes.readBigUInt64LE(40));
  const ePhentsize = bytes.readUInt16LE(54);
  const ePhnum = bytes.readUInt16LE(56);
  const eShentsize = bytes.readUInt16LE(58);
  const eShnum = bytes.readUInt16LE(60);
  return { eiClass, eiData, eType, eMachine, ePhoff, eShoff, ePhentsize, ePhnum, eShentsize, eShnum };
}

/** The artifact must be a 64-bit little-endian executable for the target arch. */
export function assertElfTarget(bytes, { path = '<bytes>', arch = 'amd64' } = {}) {
  const elf = inspectElf(bytes, { path });
  if (elf.eiClass !== ELFCLASS64) {
    refuse(`${path} is not a 64-bit ELF (EI_CLASS=${elf.eiClass})`, { path, elf });
  }
  if (elf.eiData !== ELFDATA2LSB) {
    refuse(`${path} is not little-endian (EI_DATA=${elf.eiData})`, { path, elf });
  }
  const expected = MACHINE_BY_ARCH[arch];
  if (expected === undefined) {
    refuse(`no ELF machine mapping for arch "${arch}" — refuse rather than skip the check`, { path, arch });
  }
  if (elf.eMachine !== expected) {
    refuse(
      `${path} targets ELF machine 0x${elf.eMachine.toString(16)}, not ${arch} (0x${expected.toString(16)})`,
      { path, arch, elf },
    );
  }
  if (elf.eType !== ET_EXEC && elf.eType !== ET_DYN) {
    refuse(`${path} is ELF type ${elf.eType}, not an executable (EXEC/DYN)`, { path, elf });
  }
  return elf;
}

/**
 * THE TRUNCATION DETECTOR.
 *
 * An ELF header states exactly where its program headers, section headers and
 * every segment's file image end. On a complete binary those extents land at
 * or before EOF (on the real daemon the section table ends at EXACTLY the file
 * size). On a truncated one they point past it — by megabytes. So the file
 * indicts itself, with no reference size, no threshold, and nothing to tune.
 */
export function assertElfNotTruncated(bytes, { path = '<bytes>' } = {}) {
  const elf = inspectElf(bytes, { path });
  const size = bytes.length;
  const extents = [];

  if (elf.ePhnum > 0) {
    extents.push({ what: 'program header table', end: elf.ePhoff + elf.ePhnum * elf.ePhentsize });
  }
  if (elf.eShnum > 0) {
    extents.push({ what: 'section header table', end: elf.eShoff + elf.eShnum * elf.eShentsize });
  }

  // Segment file images: walk the program headers we just proved are in range.
  const phTableEnd = elf.ePhoff + elf.ePhnum * elf.ePhentsize;
  if (elf.ePhnum > 0 && phTableEnd <= size && elf.ePhentsize >= 56) {
    for (let i = 0; i < elf.ePhnum; i += 1) {
      const off = elf.ePhoff + i * elf.ePhentsize;
      const pOffset = Number(bytes.readBigUInt64LE(off + 8));
      const pFilesz = Number(bytes.readBigUInt64LE(off + 32));
      extents.push({ what: `segment ${i} file image`, end: pOffset + pFilesz });
    }
  }

  const overrun = extents.filter((e) => e.end > size);
  if (overrun.length > 0) {
    const worst = overrun.reduce((a, b) => (a.end > b.end ? a : b));
    refuse(
      `${path} is TRUNCATED: it is ${size}B, but its own ELF header says the ${worst.what} ends at ` +
        `${worst.end}B (${overrun.length} extent(s) past EOF, short by ${worst.end - size}B). ` +
        'The bytes are not a whole program; nothing may sign them.',
      { path, size, overrun, declaredEnd: worst.end },
    );
  }
  return { size, extents };
}

/** Blunt plausibility bound for the artifact class. */
export function assertSizeFloor(bytes, { path = '<bytes>', minBytes = DAEMON_BINARY_MIN_BYTES } = {}) {
  if (bytes.length < minBytes) {
    refuse(
      `${path} is ${bytes.length}B, below the ${minBytes}B plausibility floor for this artifact ` +
        `(${((bytes.length / minBytes) * 100).toFixed(1)} % of the floor). A real build is never this small; ` +
        'this is a truncation or the wrong file.',
      { path, size: bytes.length, minBytes },
    );
  }
  return bytes.length;
}

/**
 * Run the artifact and require it to answer. A version query is the cheapest
 * proof that the bytes are a loadable, linkable, runnable program for the
 * target platform — the one property no amount of manifest arithmetic can
 * establish.
 *
 * `args` defaults to `['version']`, the SDN daemon's SUBCOMMAND. It has no
 * `--version` flag (it exits non-zero with "unknown flag"), so defaulting to
 * the flag would fail every smoke test on a perfectly good binary and train
 * operators to disable the check.
 *
 * Runs under `docker run --platform linux/amd64` so a Mac operator (the
 * build-locally-ship-binaries law puts the build on the operator's laptop)
 * exercises the artifact in the architecture the fleet will run it in.
 */
export function smokeTestBinary(
  path,
  {
    image = 'debian:bookworm-slim',
    platform = 'linux/amd64',
    args = ['version'],
    timeoutMs = 120_000,
    runner = defaultDockerRunner,
    log = () => {},
  } = {},
) {
  let output;
  try {
    output = runner({ path, image, platform, args, timeoutMs });
  } catch (error) {
    const detail = [error.stdout, error.stderr]
      .map((v) => (v ? v.toString().trim() : ''))
      .filter(Boolean)
      .join('\n')
      .slice(0, 1500);

    // The SDN daemon links against libwasmedge.so.0, which lives in LIBDIR
    // OUTSIDE the fleet bundle by design (so a bundle swap can never take the
    // runtime with it). In a bare container it is therefore absent, and the
    // dynamic linker says so precisely.
    //
    // That message is itself evidence: to produce it, the kernel had to map the
    // ELF, hand off to ld.so, and ld.so had to parse DT_NEEDED. A truncated or
    // corrupt artifact never gets there — measured on the real pair, the
    // 265 KB truncation dies on SIGBUS (135) with no output at all, while the
    // whole binary missing only its library exits 127 with this exact text.
    //
    // So a missing shared library downgrades the result to LOADED instead of
    // failing it. Refusing here would make the gate unusable for anyone without
    // the build image at hand, and an unusable gate is a disabled gate.
    const missing = /error while loading shared libraries:\s*([^\s:]+)/.exec(detail);
    if (missing && error.status === 127) {
      log(
        `[verify] smoke LOADED (not run): ${path} is a complete, loadable ${platform} executable, but ` +
          `${missing[1]} is absent from ${image}.`,
      );
      log(`[verify] for the stronger check, pass --smoke-image <the image the binary was built in>`);
      return { level: 'loaded', output: detail, missingLibrary: missing[1] };
    }

    refuse(
      `${path} FAILED to execute in ${platform} (${args.join(' ')}): ${error.message}` +
        (detail ? `\n--- container output ---\n${detail}` : ''),
      { path, platform, args, cause: error.message, status: error.status },
    );
  }
  const text = String(output ?? '').trim();
  if (text.length === 0) {
    refuse(`${path} ran but printed nothing for ${args.join(' ')} — refusing an artifact that cannot identify itself`, {
      path,
      args,
    });
  }
  log(`[verify] smoke RAN ${platform} ${args.join(' ')} -> ${text.split('\n')[0].slice(0, 120)}`);
  return { level: 'ran', output: text };
}

function defaultDockerRunner({ path, image, platform, args, timeoutMs }) {
  return execFileSync(
    'docker',
    [
      'run',
      '--rm',
      '--platform',
      platform,
      '-v',
      `${path}:/artifact:ro`,
      '--entrypoint',
      '/artifact',
      image,
      ...args,
    ],
    { encoding: 'utf8', timeout: timeoutMs, stdio: ['ignore', 'pipe', 'pipe'] },
  );
}

/**
 * The composed gate: everything a publisher must know before a signing key is
 * allowed anywhere near these bytes.
 *
 * Returns the verified bytes and their sha256 — the publisher must carry THESE
 * bytes forward, never re-read the path, so the verified thing and the
 * published thing cannot diverge.
 */
export function verifyReleaseBinary({
  path,
  arch = 'amd64',
  minBytes = DAEMON_BINARY_MIN_BYTES,
  smoke = true,
  smokeOptions = {},
  settleMs = 250,
  log = () => {},
}) {
  const stable = readStableFile(path, { settleMs, log });
  assertElfTarget(stable.bytes, { path, arch });
  assertElfNotTruncated(stable.bytes, { path });
  assertSizeFloor(stable.bytes, { path, minBytes });

  let smokeResult = null;
  if (smoke) {
    smokeResult = smokeTestBinary(path, { log, ...smokeOptions });
    // The smoke test ran the PATH, not our buffer. Prove they were still the
    // same file afterwards, so a writer that landed during the container run
    // cannot slip past the checks we already did.
    const recheck = readStableFile(path, { settleMs: 0, log: () => {} });
    if (recheck.sha256 !== stable.sha256) {
      refuse(
        `${path} changed during the smoke test (${stable.sha256.slice(0, 16)} -> ${recheck.sha256.slice(0, 16)}) — ` +
          'the artifact that ran is not the artifact we verified',
        { path, before: stable.sha256, after: recheck.sha256 },
      );
    }
  }

  log(`[verify] PASS ${path} ${stable.size}B sha256 ${stable.sha256}`);
  return {
    bytes: stable.bytes,
    sha256: stable.sha256,
    size: stable.size,
    smokeLevel: smokeResult?.level ?? 'skipped',
    version: smokeResult?.level === 'ran' ? smokeResult.output : null,
    missingLibrary: smokeResult?.missingLibrary ?? null,
  };
}

/**
 * Exclusive, crash-safe lock on a path.
 *
 * The incident's root cause was two producers writing one output path with no
 * mutual exclusion. `O_EXCL` is the mutual exclusion: the second holder cannot
 * create the lock file and refuses loudly instead of interleaving. The pid is
 * recorded so a lock left by a killed process is recognised as stale rather
 * than blocking the lane forever — but a lock held by a LIVE process is never
 * broken automatically, because "someone else is mid-write" is exactly the
 * state we must not proceed through.
 */
export function acquireExclusiveLock(lockPath, { holder = `pid ${process.pid}`, log = () => {} } = {}) {
  const payload = JSON.stringify({ pid: process.pid, holder, at: new Date().toISOString() });

  // The lock is published by LINK, not by open+write.
  //
  // `open(…, 'wx')` is atomic about the NAME but not about the CONTENT: the
  // file exists, empty, for the moment between create and write. A competitor
  // that reads it in that window sees no pid, concludes the lock is stale
  // garbage, deletes it and takes the lock — and now both hold it. That is the
  // same "read a file someone is still writing" defect this whole module
  // exists to prevent, and it was found by the race regression test.
  //
  // link() fails with EEXIST if the destination exists, so the name appears
  // atomically AND already carries its full content.
  const tryCreate = () => {
    const staging = `${lockPath}.${process.pid}.${randomBytes(6).toString('hex')}`;
    const fd = openSync(staging, 'wx');
    try {
      writeSync(fd, payload);
    } finally {
      closeSync(fd);
    }
    try {
      linkSync(staging, lockPath);
    } finally {
      unlinkSync(staging);
    }
  };

  try {
    tryCreate();
  } catch (error) {
    if (error.code !== 'EEXIST') throw error;
    let existing;
    try {
      existing = JSON.parse(readFileSync(lockPath, 'utf8'));
    } catch {
      // A lock we cannot read is a lock we cannot reason about. Fail CLOSED:
      // guessing "stale" here is precisely how two holders happen.
      refuse(
        `${lockPath} exists but could not be read as a lock record. Refusing to guess whether a publish is ` +
          'in flight — if you are certain none is, delete the file and retry.',
        { lockPath },
      );
    }
    if (isProcessAlive(existing.pid)) {
      refuse(
        `another publish/extract is already working ${lockPath.replace(/\.lock$/, '')} ` +
          `(held by pid ${existing.pid}, ${existing.holder ?? 'unknown'}, since ${existing.at ?? 'unknown'}). ` +
          'Refusing to race it — this is the concurrency that produced the truncated 2026-08-09 publish.',
        { lockPath, existing },
      );
    }
    log(`[verify] clearing stale lock ${lockPath} (pid ${existing.pid ?? '?'} is gone)`);
    unlinkSync(lockPath);
    // The reclaim itself is contended: two runs can both find the same stale
    // lock. Whoever loses the link() race loses the lock, and says so.
    try {
      tryCreate();
    } catch (retry) {
      if (retry.code !== 'EEXIST') throw retry;
      refuse(
        `another publish/extract reclaimed ${lockPath} at the same moment. Refusing to race it.`,
        { lockPath },
      );
    }
  }

  let released = false;
  return () => {
    if (released) return;
    released = true;
    try {
      unlinkSync(lockPath);
    } catch {
      /* already gone */
    }
  };
}

function isProcessAlive(pid) {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  if (pid === process.pid) return true;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error.code === 'EPERM';
  }
}
