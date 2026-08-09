/**
 * Regression tests for the 2026-08-09 publish-lane failures.
 *
 * These drive the REAL publisher entry point as a subprocess, because the
 * property under test is "the tool refuses", and a tool that refuses only when
 * called through a test helper has not been tested. Every case runs with
 * --dry-run --no-smoke, which exercises verification, locking, lineage and
 * manifest construction while touching neither Docker, ssh, nor the feed.
 */
import assert from 'node:assert/strict';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const PUBLISHER = join(here, 'publish-fleet-update.mjs');
const sha256 = (b) => createHash('sha256').update(b).digest('hex');

/** A plausible, structurally complete linux/amd64 ELF of the given size. */
function makeElf(size) {
  const buf = Buffer.alloc(size);
  buf.set([0x7f, 0x45, 0x4c, 0x46], 0);
  buf[4] = 2; // ELFCLASS64
  buf[5] = 1; // ELFDATA2LSB
  buf[6] = 1;
  buf.writeUInt16LE(2, 16); // ET_EXEC
  buf.writeUInt16LE(0x3e, 18); // EM_X86_64
  buf.writeUInt32LE(1, 20);
  const shoff = size - 64;
  buf.writeBigUInt64LE(64n, 32); // e_phoff
  buf.writeBigUInt64LE(BigInt(shoff), 40); // e_shoff
  buf.writeUInt16LE(64, 52);
  buf.writeUInt16LE(56, 54); // e_phentsize
  buf.writeUInt16LE(1, 56); // e_phnum
  buf.writeUInt16LE(64, 58); // e_shentsize
  buf.writeUInt16LE(1, 60); // e_shnum
  buf.writeUInt32LE(1, 64); // p_type PT_LOAD
  buf.writeBigUInt64LE(0n, 64 + 8);
  buf.writeBigUInt64LE(BigInt(shoff), 64 + 32);
  return buf;
}

/**
 * A throwaway world: a temp git repo standing in for the sdn checkout, a stub
 * `curl` that serves a local feed index, and a binary to publish. Nothing here
 * reaches a real host or the real feed.
 */
function makeWorld({ binarySize = 22 * 1024 * 1024, feedIndex = null } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'publish-fleet-update-test-'));
  const binary = join(dir, 'buildout-spacedatanetwork');
  const bytes = makeElf(binarySize);
  writeFileSync(binary, bytes);
  chmodSync(binary, 0o755);

  // Stub curl on PATH: the publisher fetches the feed index through it, and in
  // these tests the "feed" is a file (or absent, meaning an initial publish).
  const binDir = join(dir, 'stub-bin');
  mkdirSync(binDir);
  const indexPath = join(dir, 'index.json');
  if (feedIndex) writeFileSync(indexPath, JSON.stringify(feedIndex));
  writeFileSync(
    join(binDir, 'curl'),
    feedIndex
      ? `#!/bin/sh\ncat ${JSON.stringify(indexPath)}\n`
      : '#!/bin/sh\nexit 22\n', // curl -f on a 404
  );
  chmodSync(join(binDir, 'curl'), 0o755);
  // ssh/scp must never be reached in these tests; if they are, say so loudly.
  for (const forbidden of ['ssh', 'scp']) {
    writeFileSync(
      join(binDir, forbidden),
      `#!/bin/sh\necho "FORBIDDEN: ${forbidden} was invoked during a refusal test" >&2\nexit 99\n`,
    );
    chmodSync(join(binDir, forbidden), 0o755);
  }

  return { dir, binary, bytes, sha: sha256(bytes), binDir };
}

/** A real git repo with a linear history plus a divergent branch. */
function makeRepo(dir) {
  const repo = join(dir, 'repo');
  mkdirSync(repo);
  const git = (...args) =>
    execFileSync('git', ['-C', repo, ...args], {
      encoding: 'utf8',
      env: {
        ...process.env,
        GIT_AUTHOR_NAME: 'test',
        GIT_AUTHOR_EMAIL: 't@example.com',
        GIT_COMMITTER_NAME: 'test',
        GIT_COMMITTER_EMAIL: 't@example.com',
      },
    });
  git('init', '-q', '-b', 'main');
  writeFileSync(join(repo, 'f'), 'base\n');
  git('add', '.');
  git('commit', '-qm', 'base');
  const base = git('rev-parse', 'HEAD').trim();

  writeFileSync(join(repo, 'f'), 'live fix\n');
  git('commit', '-qam', 'the fix that is live on the fleet');
  const liveCommit = git('rev-parse', 'HEAD').trim();

  writeFileSync(join(repo, 'f'), 'newer\n');
  git('commit', '-qam', 'work on top of the live fix');
  const descendant = git('rev-parse', 'HEAD').trim();

  // A branch off `base` that never got the live fix — the silent-revert shape.
  git('checkout', '-q', '-b', 'stale', base);
  writeFileSync(join(repo, 'g'), 'stale branch work\n');
  git('add', '.');
  git('commit', '-qm', 'stale branch, missing the live fix');
  const staleCommit = git('rev-parse', 'HEAD').trim();
  git('checkout', '-q', 'main');

  return { repo, base, liveCommit, descendant, staleCommit };
}

/**
 * Run the publisher. `repoRoot` replaces the repo the publisher resolves for
 * git ancestry: the publisher derives it from its own location, so the test
 * copies the release scripts into a scratch repo's deployment/release/.
 */
function runPublisher({ world, repoRoot, args, env = {} }) {
  const script = repoRoot ? join(repoRoot, 'deployment', 'release', 'publish-fleet-update.mjs') : PUBLISHER;
  return spawnSync(process.execPath, [script, ...args], {
    encoding: 'utf8',
    env: { ...process.env, ...env, PATH: `${world.binDir}:${process.env.PATH}` },
  });
}

/** Copy the release lane into a scratch repo so repoRoot resolution lands there. */
function stageLaneInto(repoRoot) {
  const target = join(repoRoot, 'deployment', 'release');
  mkdirSync(target, { recursive: true });
  for (const file of [
    'publish-fleet-update.mjs',
    'verify-release-binary.mjs',
    'source-lineage.mjs',
    'build-update-carrier.mjs',
    'sign-update-manifest.mjs',
  ]) {
    execFileSync('cp', [join(here, file), join(target, file)]);
  }
  // sign-update-manifest.mjs reaches into desktop/src/sdn-updater/manifest.
  const manifestDir = join(repoRoot, 'desktop', 'src', 'sdn-updater');
  mkdirSync(manifestDir, { recursive: true });
  execFileSync('cp', [
    join(here, '..', '..', 'desktop', 'src', 'sdn-updater', 'manifest.js'),
    join(manifestDir, 'manifest.js'),
  ]);
}

/**
 * Async-aware: the race test's body is a promise, and a synchronous `finally`
 * would delete the world out from under the processes it just launched.
 */
async function withWorld(options, fn) {
  const world = makeWorld(options);
  try {
    return await fn(world);
  } finally {
    rmSync(world.dir, { recursive: true, force: true });
  }
}

// ---------------------------------------------------------------------------
// FAILURE CLASS 1 — the truncated binary (the published incident)
// ---------------------------------------------------------------------------

test('a truncated binary is REFUSED before anything is signed', async () => {
  await withWorld({}, (world) => {
    // The incident's exact proportions: a real build cut to 265,425 bytes.
    const truncated = makeElf(22 * 1024 * 1024).subarray(0, 265_425);
    writeFileSync(world.binary, truncated);

    const { repo, descendant } = makeRepo(world.dir);
    stageLaneInto(repo);

    const result = runPublisher({
      world,
      repoRoot: repo,
      args: ['--binary', world.binary, '--source-commit', descendant, '--dry-run', '--no-smoke'],
    });

    assert.equal(result.status, 3, `expected refusal exit 3, got ${result.status}\n${result.stderr}`);
    assert.match(result.stderr, /REFUSED/);
    assert.match(result.stderr, /TRUNCATED/);
    assert.match(result.stderr, /265425B/);
    // Nothing was signed, uploaded, or even attempted.
    assert.doesNotMatch(result.stderr, /FORBIDDEN/);
    assert.equal(result.stdout.trim(), '');
  });
});

test('a small-but-whole binary is refused by the plausibility floor', async () => {
  await withWorld({ binarySize: 2 * 1024 * 1024 }, (world) => {
    const { repo, descendant } = makeRepo(world.dir);
    stageLaneInto(repo);
    const result = runPublisher({
      world,
      repoRoot: repo,
      args: ['--binary', world.binary, '--source-commit', descendant, '--dry-run', '--no-smoke'],
    });
    assert.equal(result.status, 3, result.stderr);
    assert.match(result.stderr, /plausibility floor/);
    assert.doesNotMatch(result.stderr, /FORBIDDEN/);
  });
});

test('a whole, plausible binary publishes (dry-run) and reports one hash end to end', async () => {
  await withWorld({}, (world) => {
    const { repo, descendant } = makeRepo(world.dir);
    stageLaneInto(repo);
    const result = runPublisher({
      world,
      repoRoot: repo,
      args: ['--binary', world.binary, '--source-commit', descendant, '--dry-run', '--no-smoke'],
    });
    assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
    const report = JSON.parse(result.stdout);
    assert.equal(report.binarySha256, world.sha);
    assert.equal(report.binarySize, world.bytes.length);
    assert.equal(report.lineage, 'initial'); // no feed in this world
    assert.ok(report.bundleHash && report.wasmHash);
  });
});

// ---------------------------------------------------------------------------
// FAILURE CLASS 2 — the race (the incident's ROOT cause)
// ---------------------------------------------------------------------------

test('THE RACE: two concurrent publishes of one binary — exactly one proceeds, the other refuses', async () => {
  await withWorld({}, async (world) => {
    const { repo, descendant } = makeRepo(world.dir);
    stageLaneInto(repo);
    const script = join(repo, 'deployment', 'release', 'publish-fleet-update.mjs');

    const launch = () =>
      new Promise((resolveRun) => {
        const child = spawn(
          process.execPath,
          [script, '--binary', world.binary, '--source-commit', descendant, '--dry-run', '--no-smoke'],
          { env: { ...process.env, PATH: `${world.binDir}:${process.env.PATH}` } },
        );
        let stdout = '';
        let stderr = '';
        child.stdout.on('data', (d) => (stdout += d));
        child.stderr.on('data', (d) => (stderr += d));
        child.on('close', (status) => resolveRun({ status, stdout, stderr }));
      });

    // Genuinely concurrent: both processes start before either can finish.
    const [a, b] = await Promise.all([launch(), launch()]);
    const runs = [a, b];

    const winners = runs.filter((r) => r.status === 0);
    const refusals = runs.filter((r) => r.status === 3);

    assert.equal(
      winners.length + refusals.length,
      2,
      `both runs must either publish or refuse, got statuses ${runs.map((r) => r.status).join(',')}\n` +
        runs.map((r) => r.stderr).join('\n---\n'),
    );

    // THE PROPERTY: never two winners. Before this fix both would have "won" —
    // and on 2026-08-09 the loser's half-written bytes are what got signed.
    assert.notEqual(winners.length, 2, 'two concurrent publishes both proceeded — the lock did not hold');

    // Contention is not merely possible here, it is observed: a dry-run publish
    // of a 22 MB artifact takes ~2 s (git, tar, carrier) and both processes are
    // launched in the same tick, so the second always meets a held lock.
    assert.equal(winners.length, 1, 'exactly one publish should have proceeded');
    assert.equal(refusals.length, 1, 'exactly one publish should have been refused');
    assert.match(refusals[0].stderr, /REFUSED/);
    assert.match(
      refusals[0].stderr,
      /already working/,
      'the loser must lose to the LOCK specifically, not to some incidental error',
    );
    assert.match(refusals[0].stderr, /publish-fleet-update/, 'the refusal names who holds the lock');
    // And the winner published a manifest describing the true, whole artifact.
    assert.equal(JSON.parse(winners[0].stdout).binarySha256, world.sha);
  });
});

test('a publish refuses while an extraction still holds the binary lock', async () => {
  await withWorld({}, (world) => {
    const { repo, descendant } = makeRepo(world.dir);
    stageLaneInto(repo);

    // Stand in for `extract-release-binary.mjs` mid-copy: the lock is held by a
    // live process (this one).
    writeFileSync(
      `${world.binary}.lock`,
      JSON.stringify({ pid: process.pid, holder: 'extract-release-binary', at: new Date().toISOString() }),
    );

    const result = runPublisher({
      world,
      repoRoot: repo,
      args: ['--binary', world.binary, '--source-commit', descendant, '--dry-run', '--no-smoke'],
    });
    assert.equal(result.status, 3, result.stderr);
    assert.match(result.stderr, /already working/);
    assert.match(result.stderr, /extract-release-binary/);
    rmSync(`${world.binary}.lock`, { force: true });
  });
});

// ---------------------------------------------------------------------------
// FAILURE CLASS 3 — the silent revert
// ---------------------------------------------------------------------------

function feedWith(commit, sequence = 1786273556) {
  return {
    schema: 'org.spacedatanetwork.update.index.v1',
    generated_at: new Date().toISOString(),
    feed_base_url: 'https://sdn.spaceaware.io/updates',
    updates: [
      {
        update_id: `sdn-cli-bundle-1.0.6-updatelane.${commit.slice(0, 8)}`,
        version: `1.0.6-updatelane.${commit.slice(0, 8)}`,
        sequence,
        channel: 'beta',
        target: { platform: 'linux', arch: 'amd64', kind: 'cli-bundle' },
        source_commit: commit,
      },
    ],
  };
}

test('SILENT REVERT: publishing a non-descendant commit is refused, naming both commits', () => {
  const world = makeWorld({});
  try {
    const { repo, liveCommit, staleCommit } = makeRepo(world.dir);
    stageLaneInto(repo);
    // Re-stub curl to serve a feed whose newest artifact came from liveCommit.
    const indexPath = join(world.dir, 'index.json');
    writeFileSync(indexPath, JSON.stringify(feedWith(liveCommit)));
    writeFileSync(join(world.binDir, 'curl'), `#!/bin/sh\ncat ${JSON.stringify(indexPath)}\n`);
    chmodSync(join(world.binDir, 'curl'), 0o755);

    const result = runPublisher({
      world,
      repoRoot: repo,
      args: ['--binary', world.binary, '--source-commit', staleCommit, '--dry-run', '--no-smoke'],
    });

    assert.equal(result.status, 3, `${result.stdout}\n${result.stderr}`);
    assert.match(result.stderr, /SILENT REVERT REFUSED/);
    assert.match(result.stderr, new RegExp(staleCommit.slice(0, 12)));
    assert.match(result.stderr, new RegExp(liveCommit.slice(0, 12)));
    assert.match(result.stderr, /--rollback/);
    assert.doesNotMatch(result.stderr, /FORBIDDEN/);
  } finally {
    rmSync(world.dir, { recursive: true, force: true });
  }
});

test('a descendant of the live commit publishes normally', () => {
  const world = makeWorld({});
  try {
    const { repo, liveCommit, descendant } = makeRepo(world.dir);
    stageLaneInto(repo);
    const indexPath = join(world.dir, 'index.json');
    writeFileSync(indexPath, JSON.stringify(feedWith(liveCommit)));
    writeFileSync(join(world.binDir, 'curl'), `#!/bin/sh\ncat ${JSON.stringify(indexPath)}\n`);
    chmodSync(join(world.binDir, 'curl'), 0o755);

    const result = runPublisher({
      world,
      repoRoot: repo,
      args: ['--binary', world.binary, '--source-commit', descendant, '--dry-run', '--no-smoke'],
    });
    assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
    const report = JSON.parse(result.stdout);
    assert.equal(report.lineage, 'descendant');
    assert.equal(report.supersedesCommit, liveCommit);
  } finally {
    rmSync(world.dir, { recursive: true, force: true });
  }
});

test('re-publishing the SAME commit is a descendant, not a revert', () => {
  const world = makeWorld({});
  try {
    const { repo, liveCommit } = makeRepo(world.dir);
    stageLaneInto(repo);
    const indexPath = join(world.dir, 'index.json');
    writeFileSync(indexPath, JSON.stringify(feedWith(liveCommit)));
    writeFileSync(join(world.binDir, 'curl'), `#!/bin/sh\ncat ${JSON.stringify(indexPath)}\n`);
    chmodSync(join(world.binDir, 'curl'), 0o755);

    const result = runPublisher({
      world,
      repoRoot: repo,
      args: ['--binary', world.binary, '--source-commit', liveCommit, '--dry-run', '--no-smoke'],
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(JSON.parse(result.stdout).lineage, 'descendant');
  } finally {
    rmSync(world.dir, { recursive: true, force: true });
  }
});

test('--rollback allows the revert and records it as a rollback with its reason', () => {
  const world = makeWorld({});
  try {
    const { repo, liveCommit, staleCommit } = makeRepo(world.dir);
    stageLaneInto(repo);
    const indexPath = join(world.dir, 'index.json');
    writeFileSync(indexPath, JSON.stringify(feedWith(liveCommit)));
    writeFileSync(join(world.binDir, 'curl'), `#!/bin/sh\ncat ${JSON.stringify(indexPath)}\n`);
    chmodSync(join(world.binDir, 'curl'), 0o755);

    const result = runPublisher({
      world,
      repoRoot: repo,
      args: [
        '--binary',
        world.binary,
        '--source-commit',
        staleCommit,
        '--rollback',
        'per-call change regressed host-01 boot; reverting deliberately',
        '--dry-run',
        '--no-smoke',
      ],
    });

    assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
    const report = JSON.parse(result.stdout);
    assert.equal(report.lineage, 'rollback');
    assert.equal(report.supersedesCommit, liveCommit);
    assert.match(report.rollbackReason, /reverting deliberately/);
    // The operator is told what the fleet will now demand of them.
    assert.match(result.stderr, /DECLARED ROLLBACK/);
    assert.match(result.stderr, /--allow-rollback/);
  } finally {
    rmSync(world.dir, { recursive: true, force: true });
  }
});

// ---------------------------------------------------------------------------
// The escape hatch is not an escape hatch on a real publish.
// ---------------------------------------------------------------------------

test('--no-smoke is refused without --dry-run', async () => {
  await withWorld({}, (world) => {
    const result = runPublisher({
      world,
      args: ['--binary', world.binary, '--source-commit', 'deadbeef', '--no-smoke'],
    });
    assert.equal(result.status, 2, result.stderr);
    assert.match(result.stderr, /only permitted with --dry-run/);
  });
});
