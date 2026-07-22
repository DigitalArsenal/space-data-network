import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import { scanTrackedGo } from './check-no-app-specific-go.mjs';

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const scannerPath = resolve(scriptsDir, 'check-no-app-specific-go.mjs');

test('allows neutral Go runtime code and canonical OMM schema support', (t) => {
  const repo = createTempRepo(t);
  trackGo(repo, 'runtime/loader.go', [
    'package runtime',
    'type OMM struct{}',
    'const canonicalFileID = "$OMM"',
    'const historicalProvider = "CelesTrak"',
    'func loadModule(path string) error { return nil }',
    'func clampNonNegative(value int) int { if value < 0 { return 0 }; return value }',
    '',
  ].join('\n'));
  trackGo(repo, 'vendor/example/bad.go', 'package example\nconst id = "supplemental-omm"\n');
  trackGo(repo, 'third_party/example/bad.go', 'package example\nfunc OMMRunControl() {}\n');

  const violations = scanTrackedGo({ cwd: repo });

  assert.deepEqual(violations, []);
});

test('rejects every forbidden Go path or content form', (t) => {
  const samples = [
    ['operator_omm_flow.go', 'package runtime\n'],
    ['install_operator.go', 'package runtime\nfunc maybeInstallOperatorOMMFlow() {}\n'],
    ['flow_id.go', 'package runtime\nconst id = "org.sdn.flows.od-supplemental-omm"\n'],
    ['app_id.go', 'package runtime\nconst id = "io.spaceaware.supplemental-omm"\n'],
    ['omm_compat.go', 'package api\n'],
    ['sdnruns/runner.go', 'package sdnruns\n'],
    ['sdnodresults/reader.go', 'package sdnodresults\n'],
    ['reverse_name.go', 'package runtime\nfunc OMMSupplemental() {}\n'],
    ['od_name.go', 'package runtime\nfunc ODSupplemental() {}\n'],
    ['compat_name.go', 'package runtime\nfunc OMMCompat() {}\n'],
    ['control_name.go', 'package runtime\nfunc OMMRunControl() {}\n'],
    ['role_name.go', 'package runtime\nfunc OMMRole() {}\n'],
    ['import.go', 'package runtime\nimport _ "github.com/ipfs/kubo/sdn/sdnruns"\n'],
    ['omm_runcontrol.go', 'package api\n'],
    ['kubo/plugin/plugins/sdnruntime/celestrak_set.go', 'package sdnruntime\n'],
    ['kubo/sdn/sdnflows/celestrak_set.go', 'package sdnflows\n'],
    ['celestrak_boot.go', 'package runtime\nfunc maybeInstallCelestrakReferenceSet() {}\n'],
    ['celestrak_flows.go', 'package runtime\nfunc InstallCelestrakFlows() {}\n'],
    ['kubo/sdn/flowrt/firehistory.go', 'package flowrt\n'],
    ['kubo/sdn/flowrt/firehistory_test.go', 'package flowrt\n'],
    ['runtime/fire.go', 'package runtime\nfunc FireNow() {}\n'],
    ['runtime/abort.go', 'package runtime\nfunc AbortFire() {}\n'],
    ['runtime/reset.go', 'package runtime\nfunc ClearBatch() {}\n'],
    ['runtime/trigger.go', 'package runtime\nconst triggerFirePortID = "trigger"\n'],
    ['runtime/providers.go', 'package runtime\nfunc SourceProviderPluginIDs() {}\n'],
    ['runtime/config.go', 'package runtime\nfunc SetConfigLive() {}\n'],
    ['runtime/flow_config.go', 'package runtime\nfunc SetFlowNodeConfig() {}\n'],
    ['runtime/stored_config.go', 'package runtime\nfunc StoredConfig() {}\n'],
    ['kubo/sdn/wasmrt/od_thread_proof_test.go', 'package wasmrt\n'],
    ['kubo/sdn/wasmrt/zz_od_aot_bench_test.go', 'package wasmrt\n'],
    ['runtime/od_env.go', 'package runtime\nconst env = "SDN_OD_MODULE_WASM_PATH"\n'],
    ['runtime/od_path.go', 'package runtime\nconst path = "analysis/od/dist/isomorphic/module.wasm"\n'],
    ['runtime/od_method.go', 'package runtime\nconst method = "od.fit"\n'],
    ['ingest/runner.go', 'package ingest\nfunc syncCelestrakGP() {}\n'],
    ['ingest/defaults.go', 'package ingest\nconst defaultCelestrakCatalogURL = "x"\n'],
    ['ingest/config.go', 'package ingest\ntype Config struct { CelestrakCatalogURL string }\n'],
    ['publish/announcement.go', 'package publish\ntype A struct { CombinedCelesTrak bool }\n'],
    ['publish/schemas.go', 'package publish\nvar celesTrakDatasetSchemas []string\n'],
    ['scheduler/cadence.go', 'package scheduler\nfunc f(haystack string) bool { return strings.Contains(haystack, "celestrak") }\n'],
    ['storage/omm_table.go', 'package storage\nconst table = "sds_omm"\n'],
    ['storage/ocm_table.go', 'package storage\nconst table = "sds_ocm"\n'],
    ['storage/obd_table.go', 'package storage\nconst table = "sds_obd"\n'],
    ['storage/omm_wrapper.go', 'package storage\nconst fileID = "SOMM"\n'],
    ['storage/ocm_wrapper.go', 'package storage\nconst fileID = "SOCM"\n'],
    ['storage/obd_wrapper.go', 'package storage\nconst fileID = "SOBD"\n'],
    ['storage/schema.go', 'package storage\nconst flatsqlStoreSchema = "table app_state {}"\n'],
    ['storage/file_ids.go', 'package storage\nvar flatsqlStoreFileIDs = []string{"SOMM"}\n'],
    ['storage/test_wrapper.go', 'package storage\nfunc BuildTestWrapperRow() []byte { return nil }\n'],
    ['storage/test_ingest.go', 'package storage\nfunc IngestTestRow() error { return nil }\n'],
  ];

  for (const [relativePath, contents] of samples) {
    const repo = createTempRepo(t);
    trackGo(repo, relativePath, contents);

    const violations = scanTrackedGo({ cwd: repo });

    assert.notEqual(violations.length, 0, `${relativePath} should be rejected`);
  }
});

test('rejects ignored reactor initialization errors and descriptor-count error masking only', (t) => {
  const repo = createTempRepo(t);
  trackGo(repo, 'runtime/ignored_initialize.go', [
    'package runtime',
    'func load(mod Module) {',
    '  mod.Execute("_initialize")',
    '}',
    '',
  ].join('\n'));
  trackGo(repo, 'runtime/masked_descriptor_count.go', [
    'package runtime',
    'func (rt *Runtime) callUint32(name string) uint32 {',
    '  result, err := rt.mod.Execute(name)',
    '  if err != nil {',
    '    return 0',
    '  }',
    '  return uint32(result[0])',
    '}',
    '',
  ].join('\n'));
  trackGo(repo, 'runtime/masked_named_descriptor_count.go', [
    'package runtime',
    'func (rt *Runtime) descriptorCount(name string) uint32 {',
    '  result, exportErr := rt.mod.Execute(name)',
    '  if exportErr != nil {',
    '    return 0',
    '  }',
    '  return uint32(result[0])',
    '}',
    '',
  ].join('\n'));
  trackGo(repo, 'runtime/checked_initialize.go', [
    'package runtime',
    'func loadChecked(mod Module) error {',
    '  if _, err := mod.Execute("_initialize"); err != nil {',
    '    return err',
    '  }',
    '  return nil',
    '}',
    '',
  ].join('\n'));
  trackGo(repo, 'runtime/fallible_descriptor_count.go', [
    'package runtime',
    'func (rt *Runtime) descriptorCount(name string) (uint32, error) {',
    '  result, err := rt.mod.Execute(name)',
    '  if err != nil {',
    '    return 0, err',
    '  }',
    '  return uint32(result[0]), nil',
    '}',
    '',
  ].join('\n'));

  const violations = scanTrackedGo({ cwd: repo });
  const paths = new Set(violations.map(({ path }) => path));

  assert.deepEqual(paths, new Set([
    'runtime/ignored_initialize.go',
    'runtime/masked_descriptor_count.go',
    'runtime/masked_named_descriptor_count.go',
  ]));
});

test('matches forbidden paths and contents case-insensitively', (t) => {
  const repo = createTempRepo(t);
  trackGo(repo, 'Runtime/MiXeD.go', 'package runtime\nconst id = "SuPpLeMeNtAl_OMM"\n');
  trackGo(repo, 'Runtime/SuPpLeMeNtAl_OMM.go', 'package runtime\n');

  const violations = scanTrackedGo({ cwd: repo });

  assert.equal(violations.length, 2);
  assert.ok(violations.some(({ path, line }) => path === 'Runtime/MiXeD.go' && line === 2));
  assert.ok(violations.some(({ path, line }) => path === 'Runtime/SuPpLeMeNtAl_OMM.go' && line === 0));
});

test('scans tracked Go only and excludes no handwritten directory besides vendor and third_party', (t) => {
  const repo = createTempRepo(t);
  trackGo(repo, 'generated/forbidden.go', 'package generated\nfunc OMMRole() {}\n');
  writeGo(repo, 'untracked/forbidden.go', 'package untracked\nfunc OMMRole() {}\n');

  const violations = scanTrackedGo({ cwd: repo });

  assert.equal(violations.length, 1);
  assert.equal(violations[0].path, 'generated/forbidden.go');
});

test('ignores tracked Go files deleted from the working tree', (t) => {
  const repo = createTempRepo(t);
  const relativePath = 'runtime/deleted.go';
  trackGo(repo, relativePath, 'package runtime\nfunc OMMRole() {}\n');
  rmSync(join(repo, relativePath));

  const violations = scanTrackedGo({ cwd: repo });

  assert.deepEqual(violations, []);
});

test('rejects production CelesTrak URLs but permits offline test fixtures', (t) => {
  const repo = createTempRepo(t);
  trackGo(repo, 'runtime/fetch.go', 'package runtime\nconst endpoint = "https://celestrak.org/data"\n');
  trackGo(repo, 'runtime/fetch_test.go', 'package runtime\nconst fixture = "https://celestrak.org/data"\n');

  const violations = scanTrackedGo({ cwd: repo });

  assert.equal(violations.length, 1);
  assert.equal(violations[0].path, 'runtime/fetch.go');
});

test('rejects host-owned store hooks in the generic legacy cron mount', (t) => {
  const repo = createTempRepo(t);
  trackGo(repo, 'kubo/sdn/flowrt/cronmount.go', [
    'package flowrt',
    'func fire(rt *FlowRuntime) error {',
    '  if err := rt.SnapshotStore(); err != nil { return err }',
    '  _ = rt.Store()',
    '  return nil',
    '}',
    '',
  ].join('\n'));

  const violations = scanTrackedGo({ cwd: repo });

  assert.equal(violations.length, 2);
  assert.deepEqual(violations.map(({ line }) => line), [3, 4]);
});

test('rejects a swallowed trigger-enqueue export error', (t) => {
  const repo = createTempRepo(t);
  trackGo(repo, 'kubo/sdn/flowrt/runtime.go', [
    'package flowrt',
    'func (rt *FlowRuntime) enqueue(triggerIndex, framePtr uint32) {',
    '  rt.mod.Execute(runtimeExportEnqueueTriggerFrame, int32(triggerIndex), int32(framePtr))',
    '}',
    '',
  ].join('\n'));

  const violations = scanTrackedGo({ cwd: repo });

  assert.equal(violations.length, 1);
  assert.equal(violations[0].line, 3);
});

test('CLI prints each violation as path:line:pattern and exits one', (t) => {
  const repo = createTempRepo(t);
  trackGo(repo, 'runtime/control.go', [
    'package runtime',
    'func OMMRole() {}',
    'func OMMRunControl() {}',
    '',
  ].join('\n'));

  const result = spawnSync(process.execPath, [scannerPath], {
    cwd: repo,
    encoding: 'utf8',
  });
  const output = `${result.stdout}${result.stderr}`;

  assert.equal(result.status, 1, output);
  assert.match(output, /runtime\/control\.go:2:ommRole/i);
  assert.match(output, /runtime\/control\.go:3:ommRunControl/i);
});

function createTempRepo(t) {
  const repo = mkdtempSync(join(tmpdir(), 'sdn-no-app-specific-go-'));
  t.after(() => rmSync(repo, { recursive: true, force: true }));
  git(repo, ['init', '--quiet']);
  return repo;
}

function trackGo(repo, relativePath, contents) {
  writeGo(repo, relativePath, contents);
  git(repo, ['add', '--', relativePath]);
}

function writeGo(repo, relativePath, contents) {
  const absolutePath = join(repo, relativePath);
  mkdirSync(dirname(absolutePath), { recursive: true });
  writeFileSync(absolutePath, contents);
}

function git(repo, args) {
  const result = spawnSync('git', args, {
    cwd: repo,
    encoding: 'utf8',
  });
  assert.equal(result.status, 0, result.stdout + result.stderr);
}
