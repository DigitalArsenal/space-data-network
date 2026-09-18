import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import {
  checkLayering,
  REVIEWED_LAYERING,
  REVIEWED_LIBP2P_COPIES,
} from './check-sdn-js-dependency-layering.mjs';

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(scriptsDir, '..');
const checkerPath = resolve(scriptsDir, 'check-sdn-js-dependency-layering.mjs');

/** Build a fixture tree that mirrors the reviewed layering exactly. */
function createFixture(t, { mutateLock, mutatePkg } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'sdn-js-layering-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));

  const packages = { '': { name: 'fixture' } };
  for (const { path, version } of REVIEWED_LAYERING) packages[path] = { version };
  for (const path of REVIEWED_LIBP2P_COPIES) {
    packages[path] ??= { version: '3.2.0' };
  }
  const lock = { packages };

  const pkg = {
    name: 'fixture',
    dependencies: {
      libp2p: '^1.9.4',
      helia: '^6.0.22',
      '@helia/unixfs': '^7.1.0',
      '@libp2p/interface': '^1.7.0',
      // a non-networking dep must be ignored by the honest-floor rule
      multiformats: '^13.3.7',
    },
  };
  packages['node_modules/multiformats'] = { version: '13.4.2' };

  mutateLock?.(lock);
  mutatePkg?.(pkg);

  mkdirSync(join(root, 'sdn-js'), { recursive: true });
  writeFileSync(join(root, 'sdn-js/package-lock.json'), JSON.stringify(lock, null, 2));
  writeFileSync(join(root, 'sdn-js/package.json'), JSON.stringify(pkg, null, 2));
  return root;
}

test('passes on the reviewed dual-stack layering', (t) => {
  const root = createFixture(t);
  assert.deepEqual(checkLayering({ cwd: root }), []);
});

test('the real repository tree matches its reviewed layering', () => {
  const violations = checkLayering({ cwd: repoRoot });
  assert.deepEqual(
    violations,
    [],
    `tracked sdn-js/package-lock.json drifted from REVIEWED_LAYERING:\n${violations.join('\n')}`,
  );
});

test('catches a helia minor bump that the helia.ts shims were never reviewed against', (t) => {
  // The exact scenario the check exists for: `npm update` walks helia 6.0.22 ->
  // 6.1.4 inside the caret, unreviewed, into the code the shims are pinned to.
  const root = createFixture(t, {
    mutateLock: (lock) => {
      lock.packages['node_modules/helia'].version = '6.1.4';
      lock.packages['node_modules/@helia/unixfs'].version = '7.2.1';
    },
  });
  const violations = checkLayering({ cwd: root }).join('\n');
  assert.match(violations, /node_modules\/helia: reviewed 6\.0\.22, lockfile has 6\.1\.4/);
  assert.match(violations, /@helia\/unixfs: reviewed 7\.1\.0, lockfile has 7\.2\.1/);
  // The same bump also makes the declared caret floors stale, so both rules fire.
  assert.match(violations, /declares "helia": "\^6\.0\.22" but resolves 6\.1\.4/);
});

test('catches either half of the dual stack moving', (t) => {
  const ours = checkLayering({
    cwd: createFixture(t, {
      mutateLock: (lock) => {
        lock.packages['node_modules/libp2p'].version = '1.9.5';
      },
    }),
  }).join('\n');
  assert.match(ours, /node_modules\/libp2p: reviewed 1\.9\.4, lockfile has 1\.9\.5/);

  // helia's nested copy is invisible to our package.json, so ONLY the pin rule
  // can catch it moving — which is exactly why the pin table exists.
  const helias = checkLayering({
    cwd: createFixture(t, {
      mutateLock: (lock) => {
        lock.packages['node_modules/helia/node_modules/libp2p'].version = '3.3.11';
      },
    }),
  });
  assert.equal(helias.length, 1);
  assert.match(helias[0], /helia\/node_modules\/libp2p: reviewed 3\.2\.0, lockfile has 3\.3\.11/);
});

test('catches a package vanishing from the tree', (t) => {
  const root = createFixture(t, {
    mutateLock: (lock) => {
      delete lock.packages['node_modules/@helia/routers'];
    },
  });
  const violations = checkLayering({ cwd: root });
  assert.equal(violations.length, 1);
  assert.match(violations[0], /@helia\/routers: expected 5\.0\.3 but the package is ABSENT/);
});

test('catches a third libp2p copy appearing', (t) => {
  const root = createFixture(t, {
    mutateLock: (lock) => {
      lock.packages['node_modules/@libp2p/kad-dht/node_modules/libp2p'] = { version: '2.8.0' };
    },
  });
  const violations = checkLayering({ cwd: root });
  assert.equal(violations.length, 1);
  assert.match(violations[0], /libp2p copies changed/);
  assert.match(violations[0], /kad-dht\/node_modules\/libp2p/);
});

test('catches a declared range whose floor lies about what is resolved', (t) => {
  const root = createFixture(t, {
    mutatePkg: (pkg) => {
      // the pre-audit state: ^1.2.0 advertises a libp2p we never build against
      pkg.dependencies.libp2p = '^1.2.0';
    },
  });
  const violations = checkLayering({ cwd: root });
  assert.equal(violations.length, 1);
  assert.match(violations[0], /declares "libp2p": "\^1\.2\.0" but resolves 1\.9\.4/);
  assert.match(violations[0], /raise the floor to \^1\.9\.4/);
});

test('leaves non-networking ranges alone', (t) => {
  // multiformats resolves 13.4.2 under a ^13.3.7 floor in the fixture. Widening
  // this rule to every dependency would force nested copies on consumers of
  // dedup-sensitive packages, so the honest-floor rule is scoped on purpose.
  const root = createFixture(t);
  const violations = checkLayering({ cwd: root });
  assert.deepEqual(violations, []);
});

test('CLI exits non-zero and points at the shims when the layering moved', (t) => {
  const root = createFixture(t, {
    mutateLock: (lock) => {
      lock.packages['node_modules/helia'].version = '7.1.12';
    },
  });
  const result = spawnSync(process.execPath, [checkerPath, '--repo', root], { encoding: 'utf8' });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /FAIL: the reviewed helia\/libp2p layering moved/);
  assert.match(result.stderr, /reviewed 6\.0\.22, lockfile has 7\.1\.12/);
  assert.match(result.stderr, /sdn-js\/src\/helia\.ts carries hand-written shims/);
});

test('CLI exits zero against the tracked repository tree', () => {
  const result = spawnSync(process.execPath, [checkerPath, '--repo', repoRoot], {
    encoding: 'utf8',
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /PASS: reviewed dual-stack layering intact/);
});
