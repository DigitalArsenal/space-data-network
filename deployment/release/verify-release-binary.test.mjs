import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  DAEMON_BINARY_MIN_BYTES,
  ReleaseBinaryRefusal,
  acquireExclusiveLock,
  assertElfNotTruncated,
  assertElfTarget,
  assertSizeFloor,
  readStableFile,
  smokeTestBinary,
  verifyReleaseBinary,
} from './verify-release-binary.mjs';

const sha256 = (b) => createHash('sha256').update(b).digest('hex');

/**
 * Synthesise a linux/amd64 ELF whose header describes a file of exactly
 * `size` bytes: 64-byte header, one program header, one section header placed
 * so the section table ends at EOF — the same shape the real daemon binary has
 * (its section header table ends at byte `size` exactly).
 */
function makeElf({ size = 24 * 1024 * 1024, machine = 0x3e, type = 2, elfClass = 2, data = 1 } = {}) {
  const buf = Buffer.alloc(size);
  buf.set([0x7f, 0x45, 0x4c, 0x46], 0);
  buf[4] = elfClass;
  buf[5] = data;
  buf[6] = 1; // EI_VERSION
  buf.writeUInt16LE(type, 16); // e_type
  buf.writeUInt16LE(machine, 18); // e_machine
  buf.writeUInt32LE(1, 20); // e_version

  const phentsize = 56;
  const phnum = 1;
  const phoff = 64;
  const shentsize = 64;
  const shnum = 1;
  const shoff = size - shentsize * shnum;

  buf.writeBigUInt64LE(BigInt(phoff), 32); // e_phoff
  buf.writeBigUInt64LE(BigInt(shoff), 40); // e_shoff
  buf.writeUInt16LE(64, 52); // e_ehsize
  buf.writeUInt16LE(phentsize, 54);
  buf.writeUInt16LE(phnum, 56);
  buf.writeUInt16LE(shentsize, 58);
  buf.writeUInt16LE(shnum, 60);
  buf.writeUInt16LE(0, 62); // e_shstrndx

  // One PT_LOAD segment whose file image covers everything up to the section
  // header table.
  buf.writeUInt32LE(1, phoff); // p_type = PT_LOAD
  buf.writeUInt32LE(5, phoff + 4); // p_flags
  buf.writeBigUInt64LE(0n, phoff + 8); // p_offset
  buf.writeBigUInt64LE(BigInt(shoff), phoff + 32); // p_filesz
  return buf;
}

/**
 * assert.throws() returns undefined, so it cannot be used when the test needs
 * to inspect the refusal's MESSAGE and fields — and the message is the product
 * here: an operator has to be able to read what was wrong from it.
 */
function refusalFrom(fn, expectedType = ReleaseBinaryRefusal) {
  try {
    fn();
  } catch (error) {
    assert.ok(
      error instanceof expectedType,
      `expected ${expectedType.name}, got ${error?.name}: ${error?.message}`,
    );
    return error;
  }
  return assert.fail('expected a refusal, but the call returned normally');
}

function withTempDir(fn) {
  const dir = mkdtempSync(join(tmpdir(), 'verify-release-binary-'));
  try {
    return fn(dir);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test('a whole ELF passes every structural check', () => {
  withTempDir((dir) => {
    const path = join(dir, 'daemon');
    const bytes = makeElf();
    writeFileSync(path, bytes);

    const stable = readStableFile(path, { settleMs: 0 });
    assert.equal(stable.sha256, sha256(bytes));
    assert.equal(stable.size, bytes.length);
    assertElfTarget(bytes, { path, arch: 'amd64' });
    assertElfNotTruncated(bytes, { path });
    assertSizeFloor(bytes, { path });
  });
});

test('THE INCIDENT: a truncated ELF is refused by its own header, with no size reference', () => {
  withTempDir((dir) => {
    const path = join(dir, 'daemon');
    // The real proportions: a 20 MB build cut down to 265,425 bytes.
    const whole = makeElf({ size: 20 * 1024 * 1024 });
    const truncated = whole.subarray(0, 265_425);
    writeFileSync(path, truncated);

    // Still a valid-looking ELF for the right architecture...
    assertElfTarget(truncated, { path, arch: 'amd64' });

    // ...and yet self-evidently incomplete.
    const error = refusalFrom(() => assertElfNotTruncated(truncated, { path }));
    assert.match(error.message, /TRUNCATED/);
    assert.match(error.message, /265425B/);
    assert.match(error.message, /section header table/);
    assert.ok(error.declaredEnd > truncated.length);
  });
});

test('the size floor names the size and the bound', () => {
  withTempDir((dir) => {
    const path = join(dir, 'daemon');
    const small = makeElf({ size: 1024 * 1024 });
    writeFileSync(path, small);
    const error = refusalFrom(() => assertSizeFloor(small, { path }));
    assert.match(error.message, new RegExp(`${small.length}B`));
    assert.match(error.message, new RegExp(`${DAEMON_BINARY_MIN_BYTES}B`));
  });
});

test('a non-ELF artifact is refused outright', () => {
  withTempDir((dir) => {
    const path = join(dir, 'daemon');
    const bytes = Buffer.from('#!/bin/sh\necho not a daemon\n'.repeat(64));
    writeFileSync(path, bytes);
    assert.throws(() => assertElfTarget(bytes, { path }), /not an ELF executable/);
  });
});

test('an ELF for the wrong architecture is refused', () => {
  withTempDir((dir) => {
    const path = join(dir, 'daemon');
    const bytes = makeElf({ machine: 0xb7 }); // aarch64
    writeFileSync(path, bytes);
    assert.throws(() => assertElfTarget(bytes, { path, arch: 'amd64' }), /not amd64/);
  });
});

test('a file that changes while it is read is refused as an in-flight write', () => {
  withTempDir((dir) => {
    const path = join(dir, 'daemon');
    writeFileSync(path, makeElf({ size: 21 * 1024 * 1024 }));

    // The writer must be a SEPARATE PROCESS. readStableFile's settle is a
    // blocking wait — that is deliberate, it is a CLI holding still and looking
    // again — so an in-process timer could never fire inside the window. A
    // separate process is also the honest reproduction: on 2026-08-09 the
    // competing writer was a superseded `docker cp`, not a callback.
    const writer = spawn(
      process.execPath,
      [
        '-e',
        `setTimeout(() => require('fs').writeFileSync(${JSON.stringify(path)}, Buffer.alloc(22 * 1024 * 1024, 1)), 60)`,
      ],
      { stdio: 'ignore' },
    );

    try {
      const error = refusalFrom(() => readStableFile(path, { settleMs: 600 }));
      assert.match(error.message, /CHANGED while it was being read/);
      assert.ok(error.drift.length > 0, 'the refusal must name what drifted');
      assert.match(error.drift.join(' '), /size|sha256|mtime/);
    } finally {
      writer.kill();
    }
  });
});

test('the smoke test refuses an artifact that cannot run, and one that says nothing', () => {
  withTempDir((dir) => {
    const path = join(dir, 'daemon');
    writeFileSync(path, makeElf());

    const exploding = () => {
      const error = new Error('exec format error');
      error.stderr = Buffer.from('exec /artifact: exec format error');
      throw error;
    };
    const failed = refusalFrom(() => smokeTestBinary(path, { runner: exploding }));
    assert.match(failed.message, /FAILED to execute/);
    assert.match(failed.message, /exec format error/);

    const mute = refusalFrom(() => smokeTestBinary(path, { runner: () => '   ' }));
    assert.match(mute.message, /printed nothing/);

    assert.deepEqual(smokeTestBinary(path, { runner: () => 'spacedatanetwork 1.0.6\n' }), {
      level: 'ran',
      output: 'spacedatanetwork 1.0.6',
    });
  });
});

test('a missing shared library downgrades the smoke test to LOADED instead of failing it', () => {
  withTempDir((dir) => {
    const path = join(dir, 'daemon');
    writeFileSync(path, makeElf());

    // The real signature, measured on the actual daemon in debian:bookworm-slim:
    // the SDN binary links libwasmedge.so.0, which lives in LIBDIR OUTSIDE the
    // fleet bundle by design. Reaching this message proves the kernel mapped the
    // ELF and ld.so parsed DT_NEEDED — which a truncated artifact never does
    // (measured: SIGBUS/135, no output).
    const missingLib = () => {
      const error = new Error('Command failed');
      error.status = 127;
      error.stderr = Buffer.from(
        '/artifact: error while loading shared libraries: libwasmedge.so.0: cannot open shared object file: No such file or directory',
      );
      throw error;
    };
    const result = smokeTestBinary(path, { runner: missingLib });
    assert.equal(result.level, 'loaded');
    assert.equal(result.missingLibrary, 'libwasmedge.so.0');

    // But a crash is still a refusal — the truncated artifact's actual outcome.
    const crashed = () => {
      const error = new Error('Command failed');
      error.status = 135;
      error.stderr = Buffer.from('');
      throw error;
    };
    assert.match(refusalFrom(() => smokeTestBinary(path, { runner: crashed })).message, /FAILED to execute/);
  });
});

test('verifyReleaseBinary hands back the bytes it verified', () => {
  withTempDir((dir) => {
    const path = join(dir, 'daemon');
    const bytes = makeElf({ size: 21 * 1024 * 1024 });
    writeFileSync(path, bytes);
    const result = verifyReleaseBinary({
      path,
      settleMs: 0,
      smoke: true,
      smokeOptions: { runner: () => 'spacedatanetwork 1.0.6' },
      log: () => {},
    });
    assert.equal(result.sha256, sha256(bytes));
    assert.equal(result.size, bytes.length);
    assert.equal(result.smokeLevel, 'ran');
    assert.equal(result.version, 'spacedatanetwork 1.0.6');
    assert.ok(result.bytes.equals(bytes));
  });
});

test('verifyReleaseBinary refuses when the file is swapped during the smoke test', () => {
  withTempDir((dir) => {
    const path = join(dir, 'daemon');
    writeFileSync(path, makeElf({ size: 21 * 1024 * 1024 }));
    const error = refusalFrom(() =>
      verifyReleaseBinary({
        path,
        settleMs: 0,
        smoke: true,
        smokeOptions: {
          runner: () => {
            writeFileSync(path, makeElf({ size: 22 * 1024 * 1024 }));
            return 'spacedatanetwork 1.0.6';
          },
        },
        log: () => {},
      }),
    );
    assert.match(error.message, /changed during the smoke test/);
  });
});

test('the exclusive lock admits one holder and refuses a live second', () => {
  withTempDir((dir) => {
    const lock = join(dir, 'artifact.lock');
    const release = acquireExclusiveLock(lock, { holder: 'first' });
    const error = refusalFrom(() => acquireExclusiveLock(lock, { holder: 'second' }));
    assert.match(error.message, /already working/);
    release();
    // Once released, the next holder gets it.
    acquireExclusiveLock(lock, { holder: 'third' })();
  });
});

test('the lock is published atomically — a reader never sees it empty', () => {
  withTempDir((dir) => {
    const lock = join(dir, 'artifact.lock');
    const release = acquireExclusiveLock(lock, { holder: 'first' });
    // The record is complete the instant the name exists. An earlier version
    // created the file and wrote the pid a moment later; a competitor reading
    // in that window found no pid, judged the lock stale, deleted it and took
    // it — two holders, from the same read-a-partial-write defect this module
    // exists to prevent. The race regression test caught it.
    const record = JSON.parse(readFileSync(lock, 'utf8'));
    assert.equal(record.pid, process.pid);
    assert.equal(record.holder, 'first');
    assert.ok(Date.parse(record.at) > 0);
    release();
  });
});

test('an unreadable lock fails CLOSED rather than being assumed stale', () => {
  withTempDir((dir) => {
    const lock = join(dir, 'artifact.lock');
    writeFileSync(lock, '');
    const error = refusalFrom(() => acquireExclusiveLock(lock, { holder: 'next' }));
    assert.match(error.message, /could not be read as a lock record/);
    // And it is still there: nothing silently cleared another holder's lock.
    assert.equal(readFileSync(lock, 'utf8'), '');
  });
});

test('a lock left by a dead process is reclaimed, not honoured forever', () => {
  withTempDir((dir) => {
    const lock = join(dir, 'artifact.lock');
    // A pid that has certainly exited: spawn a process and let it die.
    const dead = spawnSync(process.execPath, ['-e', 'process.exit(0)']);
    assert.equal(dead.status, 0);
    writeFileSync(lock, JSON.stringify({ pid: dead.pid, holder: 'crashed publish', at: new Date().toISOString() }));
    const release = acquireExclusiveLock(lock, { holder: 'next run' });
    assert.match(readFileSync(lock, 'utf8'), new RegExp(`"pid":${process.pid}`));
    release();
  });
});
