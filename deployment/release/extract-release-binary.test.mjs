/**
 * Tests for the atomic extraction that removes the 2026-08-09 race at its
 * source. Docker is injected, so these exercise the concurrency, atomicity and
 * refusal logic without needing an image.
 */
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { extractReleaseBinary } from './extract-release-binary.mjs';
import { ReleaseBinaryRefusal, acquireExclusiveLock } from './verify-release-binary.mjs';

const sha256 = (b) => createHash('sha256').update(b).digest('hex');

function makeElf(size, fill = 0) {
  const buf = Buffer.alloc(size, fill);
  buf.set([0x7f, 0x45, 0x4c, 0x46], 0);
  buf[4] = 2;
  buf[5] = 1;
  buf[6] = 1;
  buf.writeUInt16LE(2, 16);
  buf.writeUInt16LE(0x3e, 18);
  buf.writeUInt32LE(1, 20);
  const shoff = size - 64;
  buf.writeBigUInt64LE(64n, 32);
  buf.writeBigUInt64LE(BigInt(shoff), 40);
  buf.writeUInt16LE(64, 52);
  buf.writeUInt16LE(56, 54);
  buf.writeUInt16LE(1, 56);
  buf.writeUInt16LE(64, 58);
  buf.writeUInt16LE(1, 60);
  buf.writeUInt32LE(1, 64);
  buf.writeBigUInt64LE(0n, 72);
  buf.writeBigUInt64LE(BigInt(shoff), 96);
  return buf;
}

/** A docker stub that "copies" the given bytes to wherever `cp` is told to. */
function dockerServing(bytes) {
  const calls = [];
  return {
    calls,
    docker(args) {
      calls.push(args);
      if (args[0] === 'create') return 'sha256:container-id\n';
      if (args[0] === 'cp') {
        writeFileSync(args[2], bytes);
        return '';
      }
      return '';
    },
  };
}

function withTempDir(fn) {
  const dir = mkdtempSync(join(tmpdir(), 'extract-release-binary-'));
  try {
    return fn(dir);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function refusalFrom(fn) {
  try {
    fn();
  } catch (error) {
    assert.ok(error instanceof ReleaseBinaryRefusal, `expected a refusal, got ${error?.name}: ${error?.message}`);
    return error;
  }
  return assert.fail('expected a refusal, but the call returned normally');
}

test('extraction lands the binary atomically and leaves no staging behind', () => {
  withTempDir((dir) => {
    const bytes = makeElf(21 * 1024 * 1024);
    const out = join(dir, 'buildout-spacedatanetwork');
    const { docker, calls } = dockerServing(bytes);

    const result = extractReleaseBinary({
      image: 'sdn-build:test',
      containerPath: '/out/spacedatanetwork',
      out,
      docker,
    });

    assert.equal(result.sha256, sha256(bytes));
    assert.equal(result.size, bytes.length);
    assert.ok(readFileSync(out).equals(bytes));

    // docker cp NEVER targeted the destination — it wrote to staging, and the
    // destination appeared by rename. That is what makes a concurrent reader
    // unable to observe a prefix.
    const cp = calls.find((c) => c[0] === 'cp');
    assert.notEqual(cp[2], out, 'docker cp wrote directly to the destination');
    assert.match(cp[2], /\.extract-/);

    // No staging dirs or lock files survive.
    const leftovers = readdirSync(dir).filter((f) => f !== 'buildout-spacedatanetwork');
    assert.deepEqual(leftovers, [], `leftovers: ${leftovers.join(', ')}`);
  });
});

test('a truncated extraction never reaches the destination', () => {
  withTempDir((dir) => {
    const truncated = makeElf(21 * 1024 * 1024).subarray(0, 265_425);
    const out = join(dir, 'buildout-spacedatanetwork');
    const { docker } = dockerServing(truncated);

    const error = refusalFrom(() =>
      extractReleaseBinary({ image: 'sdn-build:test', containerPath: '/out/x', out, docker }),
    );
    assert.match(error.message, /TRUNCATED/);
    assert.equal(existsSync(out), false, 'a refused extraction still created the destination');
    assert.deepEqual(readdirSync(dir), [], 'staging survived a refusal');
  });
});

test('THE RACE, at its source: a second extraction of the same destination refuses', () => {
  withTempDir((dir) => {
    const out = join(dir, 'buildout-spacedatanetwork');
    // Job A is mid-copy and holds the lock (a live pid: this process).
    const release = acquireExclusiveLock(`${out}.lock`, { holder: 'extract job A' });

    const { docker, calls } = dockerServing(makeElf(21 * 1024 * 1024));
    const error = refusalFrom(() =>
      extractReleaseBinary({ image: 'sdn-build:test', containerPath: '/out/x', out, docker }),
    );

    assert.match(error.message, /already working/);
    assert.match(error.message, /extract job A/);
    // It refused BEFORE touching docker — no partial work at all.
    assert.deepEqual(calls, []);
    release();
  });
});

test('an unexpected destination is refused, and --replace is the way to say it is intended', () => {
  withTempDir((dir) => {
    const out = join(dir, 'buildout-spacedatanetwork');
    const theirs = makeElf(21 * 1024 * 1024, 7);
    writeFileSync(out, theirs);

    const mine = makeElf(21 * 1024 * 1024, 9);
    const { docker } = dockerServing(mine);

    const error = refusalFrom(() =>
      extractReleaseBinary({ image: 'sdn-build:test', containerPath: '/out/x', out, docker }),
    );
    assert.match(error.message, /already exists with DIFFERENT content/);
    assert.match(error.message, /--replace/);
    assert.ok(readFileSync(out).equals(theirs), 'a refused extraction overwrote the destination anyway');

    const replaced = extractReleaseBinary({
      image: 'sdn-build:test',
      containerPath: '/out/x',
      out,
      replace: true,
      docker,
    });
    assert.equal(replaced.sha256, sha256(mine));
    assert.ok(readFileSync(out).equals(mine));
  });
});

test('re-extracting identical bytes is a no-op, not a refusal', () => {
  withTempDir((dir) => {
    const out = join(dir, 'buildout-spacedatanetwork');
    const bytes = makeElf(21 * 1024 * 1024);
    writeFileSync(out, bytes);

    const { docker } = dockerServing(bytes);
    const result = extractReleaseBinary({ image: 'sdn-build:test', containerPath: '/out/x', out, docker });
    assert.equal(result.unchanged, true);
    assert.equal(result.sha256, sha256(bytes));
  });
});
