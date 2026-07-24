import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const scriptPath = resolve(scriptsDir, 'purge-legacy-supplemental-omm-state.mjs');

test('dry run finds legacy state without changing the node repository', (t) => {
  const fixture = createFixture(t);
  const before = readFileSync(fixture.moduleRegistry, 'utf8');

  const result = run(['--repo', fixture.repo, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(result.stdout);
  assert.equal(report.mode, 'dry-run');
  assert.equal(report.legacyModules.length, 1);
  assert.equal(report.legacyFlows.length, 2);
  assert.equal(report.approvalsToRevoke, 3);
  assert.equal(readFileSync(fixture.moduleRegistry, 'utf8'), before);
  assert.equal(existsSync(fixture.ledger), true);
});

test('apply requires an explicit backup directory', (t) => {
  const fixture = createFixture(t);

  const result = run(['--repo', fixture.repo, '--apply']);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /--backup-dir is required/i);
});

test('apply removes only legacy state, revokes its hashes, and preserves a recovery backup', (t) => {
  const fixture = createFixture(t);
  const backup = join(fixture.root, 'backup');

  const result = run([
    '--repo', fixture.repo,
    '--apply',
    '--backup-dir', backup,
    '--json',
  ]);

  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(result.stdout);
  assert.equal(report.mode, 'apply');
  assert.equal(report.approvalsToRevoke, 3);

  const modules = readJSON(fixture.moduleRegistry).modules;
  assert.deepEqual(modules.map(({ id }) => id), ['org.example.weather']);

  const flows = readJSON(fixture.flowRegistry).flows;
  assert.deepEqual(flows.map(({ id }) => id), ['org.example.neutral-flow']);

  const approvals = readJSON(fixture.policy).approvals;
  assert.deepEqual(approvals.map(({ module_hash }) => module_hash), [fixture.neutralHash]);

  assert.equal(existsSync(join(fixture.repo, 'sdn/modules/com.orbpro.celestrak-supgp.json')), false);
  assert.equal(existsSync(join(fixture.repo, 'sdn/modules/org.example.weather.json')), true);
  assert.equal(existsSync(fixture.dropin), false);
  assert.equal(existsSync(fixture.ledger), false);
  assert.equal(existsSync(join(fixture.repo, 'sdn/flows/od-supplemental-omm')), false);
  assert.equal(existsSync(join(fixture.repo, 'sdn/flows/org.example.neutral-flow/runtime.wasm')), true);

  assert.equal(existsSync(join(backup, 'sdn/modules/installed.json')), true);
  assert.equal(existsSync(join(backup, 'sdn/flows/installed-flows.json')), true);
  assert.equal(existsSync(join(backup, 'sdn/modules/com.orbpro.celestrak-supgp.json')), true);
  assert.equal(existsSync(join(backup, 'sdn/modules/install/legacy.wasm')), true);
  assert.equal(existsSync(join(backup, 'sdn/flows/od-supplemental-omm/runtime.wasm')), true);
  assert.equal(existsSync(join(backup, 'sdn/celestrak_fetch_ledger.json')), true);
});

test('a completed purge is idempotent in dry-run mode', (t) => {
  const fixture = createFixture(t);
  const backup = join(fixture.root, 'backup');
  const first = run(['--repo', fixture.repo, '--apply', '--backup-dir', backup, '--json']);
  assert.equal(first.status, 0, first.stderr);

  const second = run(['--repo', fixture.repo, '--json']);

  assert.equal(second.status, 0, second.stderr);
  const report = JSON.parse(second.stdout);
  assert.equal(report.clean, true);
  assert.deepEqual(report.legacyModules, []);
  assert.deepEqual(report.legacyFlows, []);
  assert.equal(report.approvalsToRevoke, 0);
  assert.deepEqual(report.filesToQuarantine, []);
});

function createFixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'sdn-legacy-state-purge-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const repo = join(root, 'node');
  const moduleRegistry = join(repo, 'sdn/modules/installed.json');
  const flowRegistry = join(repo, 'sdn/flows/installed-flows.json');
  const policy = join(repo, 'sdn/capability_policy.json');
  const ledger = join(repo, 'sdn/celestrak_fetch_ledger.json');

  const legacyModule = Buffer.from('legacy-celestrak-module');
  const legacyModuleHash = sha256(legacyModule);
  const supplementalRuntime = Buffer.from('supplemental-flow-runtime');
  const supplementalHash = sha256(supplementalRuntime);
  const neutralRuntime = Buffer.from('neutral-flow-runtime');
  const neutralHash = sha256(neutralRuntime);

  writeJSON(moduleRegistry, {
    modules: [
      {
        id: 'com.orbpro.celestrak-supgp',
        content_hash: legacyModuleHash,
        enabled: true,
        source: 'role:celestrak',
      },
      {
        id: 'org.example.weather',
        content_hash: neutralHash,
        enabled: true,
        source: 'admin',
      },
    ],
  });
  writeJSON(flowRegistry, {
    flows: [
      {
        id: 'org.sdn.flows.od-supplemental-omm',
        ref: 'sdn/flows/od-supplemental-omm',
        enabled: true,
        source: 'role:omm',
      },
      {
        id: 'com.digitalarsenal.flows.celestrak-gp-ingest',
        ref: 'sdn/flows/celestrak-gp-ingest',
        enabled: true,
        source: 'role:celestrak',
      },
      {
        id: 'org.example.neutral-flow',
        ref: 'sdn/flows/org.example.neutral-flow',
        enabled: true,
        source: 'admin',
      },
    ],
  });
  writeJSON(policy, {
    version: 1,
    approvals: [
      { module_hash: legacyModuleHash, capability: 'http' },
      { module_hash: supplementalHash, capability: 'http' },
      {
        module_hash: sha256(Buffer.from('old-flow-with-metadata')),
        capability: 'storage_ingest',
        plugin_id: 'com.digitalarsenal.flows.celestrak-gp-ingest',
      },
      {
        module_hash: neutralHash,
        capability: 'http',
        plugin_id: 'org.example.neutral-flow',
        approved_by: 'operator',
      },
    ],
  });

  writeFile(join(repo, 'sdn/modules/com.orbpro.celestrak-supgp.json'), '{}\n');
  writeFile(join(repo, 'sdn/modules/org.example.weather.json'), '{}\n');
  const dropin = join(repo, 'sdn/modules/install/legacy.wasm');
  writeFile(dropin, withPublicationTrailer(legacyModule));
  writeFile(join(repo, 'sdn/modules/install/neutral.wasm'), neutralRuntime);
  writeFile(join(repo, 'sdn/flows/od-supplemental-omm/runtime.wasm'), withPublicationTrailer(supplementalRuntime));
  writeFile(join(repo, 'sdn/flows/celestrak-gp-ingest/runtime.wasm'), Buffer.from('other-celestrak-flow'));
  writeFile(join(repo, 'sdn/flows/org.example.neutral-flow/runtime.wasm'), neutralRuntime);
  writeJSON(ledger, { last_fetch: '2026-07-20T00:00:00Z' });

  return {
    root,
    repo,
    moduleRegistry,
    flowRegistry,
    policy,
    ledger,
    dropin,
    neutralHash,
  };
}

function run(args) {
  return spawnSync(process.execPath, [scriptPath, ...args], {
    encoding: 'utf8',
  });
}

function writeFile(path, contents) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, contents);
}

function writeJSON(path, value) {
  writeFile(path, `${JSON.stringify(value, null, 2)}\n`);
}

function readJSON(path) {
  return JSON.parse(readFileSync(path, 'utf8'));
}

function sha256(value) {
  return createHash('sha256').update(value).digest('hex');
}

function withPublicationTrailer(portable) {
  const record = Buffer.from('fixture-record');
  const footer = Buffer.alloc(8);
  footer.writeUInt32LE(record.length, 0);
  footer.write('$REC', 4, 'ascii');
  return Buffer.concat([portable, record, footer]);
}
