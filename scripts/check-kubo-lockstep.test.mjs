import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import {
  checkLockstep,
  parseGoMod,
  DECLARED_DIVERGENCES,
  INTEROP_PREFIXES,
} from './check-kubo-lockstep.mjs';

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(scriptsDir, '..');
const checkerPath = resolve(scriptsDir, 'check-kubo-lockstep.mjs');

function goMod(moduleName, requires) {
  const lines = [`module ${moduleName}`, '', 'go 1.25', '', 'require ('];
  for (const [path, version, indirect] of requires) {
    lines.push(`\t${path} ${version}${indirect ? ' // indirect' : ''}`);
  }
  lines.push(')', '');
  return lines.join('\n');
}

function createFixture(t, { serverRequires, kuboRequires }) {
  const root = mkdtempSync(join(tmpdir(), 'kubo-lockstep-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, 'sdn-server'), { recursive: true });
  mkdirSync(join(root, 'kubo'), { recursive: true });
  writeFileSync(
    join(root, 'sdn-server/go.mod'),
    goMod('github.com/spacedatanetwork/sdn-server', serverRequires),
  );
  writeFileSync(join(root, 'kubo/go.mod'), goMod('github.com/ipfs/kubo', kuboRequires));
  return root;
}

test('parseGoMod reads require pins and ignores module/go/replace lines', () => {
  const pins = parseGoMod(
    [
      'module github.com/ipfs/kubo',
      '',
      'go 1.25',
      '',
      'require (',
      '\tgithub.com/libp2p/go-libp2p v0.46.0',
      '\tgithub.com/ipfs/boxo v0.35.2 // indirect',
      ')',
      '',
      'replace github.com/DigitalArsenal/spacedatastandards.org/lib/go => ./sdn/third_party/spacedatastandards-go',
    ].join('\n'),
  );
  assert.deepEqual(pins.get('github.com/libp2p/go-libp2p'), {
    version: 'v0.46.0',
    indirect: false,
  });
  assert.deepEqual(pins.get('github.com/ipfs/boxo'), { version: 'v0.35.2', indirect: true });
  assert.equal(pins.has('github.com/ipfs/kubo'), false, 'the module line is not a pin');
  assert.equal(pins.size, 2);
});

test('passes when the shared interop pins agree', (t) => {
  const shared = [
    ['github.com/libp2p/go-libp2p', 'v0.46.0'],
    ['github.com/libp2p/go-libp2p-kad-dht', 'v0.36.0'],
    ['github.com/multiformats/go-multiaddr', 'v0.16.1'],
  ];
  const root = createFixture(t, {
    serverRequires: [...shared, ['golang.org/x/crypto', 'v0.51.0']],
    kuboRequires: [...shared, ['golang.org/x/crypto', 'v0.47.0']],
  });
  const { violations, comparedCount } = checkLockstep({ cwd: root });
  assert.deepEqual(violations, [], 'non-interop modules like x/crypto are allowed to diverge');
  assert.equal(comparedCount, 3);
});

test('catches a go-libp2p divergence — the one that silently breaks interop', (t) => {
  const root = createFixture(t, {
    serverRequires: [['github.com/libp2p/go-libp2p', 'v0.46.0']],
    kuboRequires: [['github.com/libp2p/go-libp2p', 'v0.49.0']],
  });
  const { violations } = checkLockstep({ cwd: root });
  assert.equal(violations.length, 1);
  assert.match(
    violations[0],
    /go-libp2p: sdn-server\/go\.mod pins v0\.46\.0, kubo\/go\.mod pins v0\.49\.0/,
  );
});

test('catches a kad-dht divergence', (t) => {
  const root = createFixture(t, {
    serverRequires: [['github.com/libp2p/go-libp2p-kad-dht', 'v0.36.0', true]],
    kuboRequires: [['github.com/libp2p/go-libp2p-kad-dht', 'v0.42.2']],
  });
  const { violations } = checkLockstep({ cwd: root });
  assert.equal(violations.length, 1);
  assert.match(violations[0], /pins v0\.36\.0 \(indirect\), kubo\/go\.mod pins v0\.42\.2/);
});

test('ignores modules only one tree depends on', (t) => {
  const root = createFixture(t, {
    serverRequires: [['github.com/libp2p/go-libp2p-only-here', 'v1.0.0']],
    kuboRequires: [['github.com/ipfs/go-only-in-kubo', 'v2.0.0']],
  });
  const { violations, comparedCount } = checkLockstep({ cwd: root });
  assert.deepEqual(violations, []);
  assert.equal(comparedCount, 0);
});

test('an allowlisted divergence is tolerated, and goes stale when the trees re-converge', (t) => {
  const allowed = DECLARED_DIVERGENCES[0].module;
  assert.ok(
    INTEROP_PREFIXES.some((p) => allowed.startsWith(p)),
    'the allowlist only makes sense for interop-critical modules',
  );

  const diverged = createFixture(t, {
    serverRequires: [[allowed, 'v0.35.2', true]],
    kuboRequires: [[allowed, 'v0.35.3-0.20260109213916-89dc184784f2']],
  });
  assert.deepEqual(checkLockstep({ cwd: diverged }).violations, []);

  const converged = createFixture(t, {
    serverRequires: [[allowed, 'v0.35.2', true]],
    kuboRequires: [[allowed, 'v0.35.2']],
  });
  const { violations } = checkLockstep({ cwd: converged });
  assert.equal(violations.length, 1);
  assert.match(violations[0], /listed in DECLARED_DIVERGENCES but the two trees now agree/);
});

test('every declared divergence carries a reason and a review date', () => {
  for (const entry of DECLARED_DIVERGENCES) {
    assert.match(entry.reviewed, /^\d{4}-\d{2}-\d{2}$/, `${entry.module} needs a review date`);
    assert.ok((entry.reason ?? '').length > 40, `${entry.module} needs a real reason`);
  }
});

test('the tracked repository trees are in lockstep', () => {
  const { violations, comparedCount } = checkLockstep({ cwd: repoRoot });
  assert.deepEqual(violations, [], violations.join('\n'));
  assert.ok(comparedCount > 20, `expected a broad shared surface, compared ${comparedCount}`);
});

test('CLI exits non-zero on drift and zero on the tracked trees', (t) => {
  const drifted = createFixture(t, {
    serverRequires: [['github.com/libp2p/go-libp2p', 'v0.46.0']],
    kuboRequires: [['github.com/libp2p/go-libp2p', 'v0.49.0']],
  });
  const bad = spawnSync(process.execPath, [checkerPath, '--repo', drifted], { encoding: 'utf8' });
  assert.equal(bad.status, 1);
  assert.match(bad.stderr, /FAIL: sdn-server and the vendored kubo tree drifted apart/);

  const good = spawnSync(process.execPath, [checkerPath, '--repo', repoRoot], { encoding: 'utf8' });
  assert.equal(good.status, 0, good.stderr);
  assert.match(good.stdout, /PASS: \d+ shared libp2p\/IPFS\/multiformats pins agree/);
});
