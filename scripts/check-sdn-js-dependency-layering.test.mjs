import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import {
  checkLayering,
  KNOWN_DUPLICATE_LEAVES,
  RETIRED_DEPENDENCIES,
  SECURITY_FLOORS,
  SINGLE_COPY_PACKAGES,
} from './check-sdn-js-dependency-layering.mjs';

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(scriptsDir, '..');
const checkerPath = resolve(scriptsDir, 'check-sdn-js-dependency-layering.mjs');

/** Build a fixture tree with one copy of every critical package. */
function createFixture(t, { mutateLock, mutatePkg } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'sdn-js-layering-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));

  const versions = {
    libp2p: '3.3.11',
    '@libp2p/interface': '3.3.0',
    '@libp2p/crypto': '5.1.23',
    '@libp2p/peer-id': '6.0.15',
    '@libp2p/peer-store': '12.0.28',
    '@multiformats/multiaddr': '13.0.3',
    helia: '7.1.12',
    '@helia/interface': '7.1.1',
    '@helia/utils': '3.0.5',
  };
  const packages = { '': { name: 'fixture' } };
  for (const { name } of SINGLE_COPY_PACKAGES) {
    packages[`node_modules/${name}`] = { version: versions[name] };
  }
  packages['node_modules/@libp2p/kad-dht'] = { version: '16.4.5' };
  packages['node_modules/multiformats'] = { version: '14.0.5' };
  const lock = { packages };

  const pkg = {
    name: 'fixture',
    dependencies: {
      libp2p: '^3.3.11',
      helia: '^7.1.12',
      '@libp2p/interface': '^3.3.0',
      // a non-networking dep must be ignored by the honest-floor rule
      multiformats: '^14.0.0',
    },
  };

  mutateLock?.(lock);
  mutatePkg?.(pkg);

  mkdirSync(join(root, 'sdn-js'), { recursive: true });
  writeFileSync(join(root, 'sdn-js/package-lock.json'), JSON.stringify(lock, null, 2));
  writeFileSync(join(root, 'sdn-js/package.json'), JSON.stringify(pkg, null, 2));
  return root;
}

test('passes on a single-major tree', (t) => {
  const root = createFixture(t);
  assert.deepEqual(checkLayering({ cwd: root }), []);
});

test('the real repository tree still has one copy of each critical package', () => {
  const violations = checkLayering({ cwd: repoRoot });
  assert.deepEqual(
    violations,
    [],
    `tracked sdn-js/package-lock.json re-split the libp2p/helia layering:\n${violations.join('\n')}`,
  );
});

test('catches the split that the helia.ts shims existed for', (t) => {
  // The exact regression this check now guards: a dependency that still wants
  // an older libp2p nests one, and the two majors are back in one tree.
  const root = createFixture(t, {
    mutateLock: (lock) => {
      lock.packages['node_modules/some-dep/node_modules/libp2p'] = { version: '1.9.4' };
    },
  });
  const violations = checkLayering({ cwd: root }).join('\n');
  assert.match(violations, /libp2p: expected ONE copy, found 2/);
  assert.match(violations, /some-dep\/node_modules\/libp2p/);
});

test('catches a second @libp2p/interface, which is how capability checks start failing', (t) => {
  const root = createFixture(t, {
    mutateLock: (lock) => {
      lock.packages['node_modules/helia/node_modules/@libp2p/interface'] = { version: '2.11.0' };
    },
  });
  const violations = checkLayering({ cwd: root }).join('\n');
  assert.match(violations, /@libp2p\/interface: expected ONE copy, found 2/);
});

test('catches a critical package vanishing from the tree', (t) => {
  const root = createFixture(t, {
    mutateLock: (lock) => {
      delete lock.packages['node_modules/@helia/utils'];
    },
  });
  const violations = checkLayering({ cwd: root });
  assert.equal(violations.length, 1);
  assert.match(violations[0], /@helia\/utils: expected exactly one copy but the package is ABSENT/);
});

test('catches a nested copy sliding back under a security floor', (t) => {
  // GHSA-vrf4-mx87-p53w survived because the vulnerable copy was nested where
  // our direct range could not reach it, so the floor applies to EVERY copy.
  const root = createFixture(t, {
    mutateLock: (lock) => {
      lock.packages['node_modules/@libp2p/peer-store'].version = '12.0.15';
    },
  });
  const violations = checkLayering({ cwd: root });
  assert.equal(violations.length, 1);
  assert.match(violations[0], /@libp2p\/peer-store: .* resolves 12\.0\.15, below the 12\.0\.24 security floor/);
  assert.match(violations[0], /GHSA-vrf4-mx87-p53w/);
});

test('catches a retired dependency being declared again', (t) => {
  const root = createFixture(t, {
    mutatePkg: (pkg) => {
      pkg.dependencies['@spacedatanetwork/libp2p-webrtc-v1'] = 'npm:@libp2p/webrtc@^4.1.10';
      pkg.dependencies['@chainsafe/libp2p-gossipsub'] = '^14.1.2';
    },
  });
  const violations = checkLayering({ cwd: root }).join('\n');
  assert.match(violations, /@spacedatanetwork\/libp2p-webrtc-v1: retired by the libp2p 3 upgrade/);
  assert.match(violations, /@chainsafe\/libp2p-gossipsub: retired by the libp2p 3 upgrade/);
});

test('tolerates the recorded duplicate leaves but not one more', (t) => {
  const multiformats = KNOWN_DUPLICATE_LEAVES.find((entry) => entry.name === 'multiformats');
  assert.ok(multiformats, 'multiformats must be recorded as a known duplicate leaf');

  const atLimit = createFixture(t, {
    mutateLock: (lock) => {
      for (let index = 1; index < multiformats.maxCopies; index += 1) {
        lock.packages[`node_modules/leaf-${index}/node_modules/multiformats`] = { version: '13.4.2' };
      }
    },
  });
  assert.deepEqual(checkLayering({ cwd: atLimit }), []);

  const overLimit = createFixture(t, {
    mutateLock: (lock) => {
      for (let index = 1; index <= multiformats.maxCopies; index += 1) {
        lock.packages[`node_modules/leaf-${index}/node_modules/multiformats`] = { version: '13.4.2' };
      }
    },
  });
  const violations = checkLayering({ cwd: overLimit }).join('\n');
  assert.match(violations, /multiformats: \d+ copies, more than the \d+ recorded as understood/);
});

test('catches a declared range whose floor lies about what is resolved', (t) => {
  const root = createFixture(t, {
    mutatePkg: (pkg) => {
      pkg.dependencies.libp2p = '^3.0.6';
    },
  });
  const violations = checkLayering({ cwd: root });
  assert.equal(violations.length, 1);
  assert.match(violations[0], /declares "libp2p": "\^3\.0\.6" but resolves 3\.3\.11/);
  assert.match(violations[0], /raise the floor to \^3\.3\.11/);
});

test('leaves non-networking ranges alone', (t) => {
  // multiformats resolves 14.0.5 under a ^14.0.0 floor in the fixture. Widening
  // this rule to every dependency would force nested copies on consumers of
  // dedup-sensitive packages, so the honest-floor rule is scoped on purpose.
  const root = createFixture(t);
  assert.deepEqual(checkLayering({ cwd: root }), []);
});

test('every security floor names an advisory', () => {
  for (const floor of SECURITY_FLOORS) {
    assert.match(floor.why, /GHSA-/, `${floor.name} floor must cite the advisory it exists for`);
  }
});

test('every retired dependency says why it cannot come back', () => {
  for (const retired of RETIRED_DEPENDENCIES) {
    assert.ok(retired.why.length > 20, `${retired.name} needs a reason`);
  }
});

test('CLI exits non-zero and explains the split when a package re-splits', (t) => {
  const root = createFixture(t, {
    mutateLock: (lock) => {
      lock.packages['node_modules/old-dep/node_modules/libp2p'] = { version: '1.9.4' };
    },
  });
  const result = spawnSync(process.execPath, [checkerPath, '--repo', root], { encoding: 'utf8' });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /FAIL: the single-major libp2p\/helia layering moved/);
  assert.match(result.stderr, /libp2p: expected ONE copy, found 2/);
  assert.match(result.stderr, /GHSA-vrf4-mx87-p53w/);
});

test('CLI exits zero against the tracked repository tree', () => {
  const result = spawnSync(process.execPath, [checkerPath, '--repo', repoRoot], {
    encoding: 'utf8',
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /PASS: one copy each of \d+ critical packages/);
});
