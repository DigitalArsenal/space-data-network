import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const contract = JSON.parse(readFileSync(new URL('./sdn-parity-contract.json', import.meta.url), 'utf8'));
const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

test('SDN parity contract covers first-version product surfaces', () => {
  assert.equal(contract.version, 1);
  assert.deepEqual(contract.surfaces, ['cli', 'desktop', 'ui', 'release', 'docs']);

  const ids = new Set(contract.capabilities.map((capability) => capability.id));
  for (const required of [
    'install.user_scoped',
    'identity.bootstrap',
    'identity.export',
    'identity.directory',
    'desktop.route.node_epm_vcard',
    'desktop.route.peer_epm',
    'desktop.route.auth_user_update',
    'data.search',
    'data.query',
    'provider.search',
    'provider.interaction',
    'encrypted_ca.maneuver_ephemeris',
    'lifecycle.service',
    'lifecycle.remove',
    'update.daemon_in_place',
    'release.desktop_artifacts',
    'ci.live_dht_cross_platform'
  ]) {
    assert.ok(ids.has(required), `missing parity capability ${required}`);
  }
});

test('every parity capability names surfaces and tests', () => {
  for (const capability of contract.capabilities) {
    assert.equal(typeof capability.id, 'string');
    assert.ok(capability.id.length > 0);
    assert.ok(Array.isArray(capability.surfaces), `${capability.id} surfaces must be an array`);
    assert.ok(capability.surfaces.length > 0, `${capability.id} must name at least one surface`);
    assert.ok(Array.isArray(capability.tests), `${capability.id} tests must be an array`);
    assert.ok(capability.tests.length > 0, `${capability.id} must name at least one proving test`);
  }
});

test('contract maps every objective requirement to explicit acceptance evidence', () => {
  const requiredRequirementIds = [
    'R01.shared_acceptance_matrix',
    'R02.desktop_api_routes',
    'R03.shared_provider_data_search',
    'R04.provider_cli_commands',
    'R05.encrypted_ca_private_mpe',
    'R06.identity_epm_vcard',
    'R07.lifecycle_parity',
    'R08.installer_parity',
    'R09.signed_update_parity',
    'R10.desktop_release_artifacts',
    'R11.docs_help_website',
    'R12.live_dht_cross_platform',
    'R13.desktop_ui_smoke'
  ];
  const requirementIds = new Set((contract.objectiveRequirements ?? []).map((requirement) => requirement.id));
  for (const required of requiredRequirementIds) {
    assert.ok(requirementIds.has(required), `missing objective requirement ${required}`);
  }

  const coveredRequirementIds = new Set();
  for (const capability of contract.capabilities) {
    assert.ok(
      Array.isArray(capability.requirementIds) && capability.requirementIds.length > 0,
      `${capability.id} must map to at least one objective requirement`
    );
    assert.ok(
      Array.isArray(capability.acceptance) && capability.acceptance.length > 0,
      `${capability.id} must list acceptance checks`
    );
    for (const requirementId of capability.requirementIds) {
      assert.ok(requirementIds.has(requirementId), `${capability.id} references unknown requirement ${requirementId}`);
      coveredRequirementIds.add(requirementId);
    }
  }

  for (const required of requiredRequirementIds) {
    assert.ok(coveredRequirementIds.has(required), `no capability covers objective requirement ${required}`);
  }
});

test('search, installer, update, and live-DHT capabilities encode required modes', () => {
  const capabilities = new Map(contract.capabilities.map((capability) => [capability.id, capability]));

  for (const id of ['data.search', 'provider.search']) {
    const capability = capabilities.get(id);
    assert.ok(capability, `missing ${id}`);
    assert.deepEqual(capability.modes, ['local', 'daemon', 'live-dht'], `${id} modes must match product contract`);
    assert.deepEqual(capability.outputs, ['row', 'json', 'csv'], `${id} outputs must match product contract`);
  }

  const providerInteraction = capabilities.get('provider.interaction');
  assert.ok(providerInteraction);
  assert.deepEqual(
    providerInteraction.commands,
    ['list', 'search', 'show', 'connect-query', 'descriptor-lookup', 'data-standard-filter'],
    'provider interaction commands must cover requester-facing CLI/API flows'
  );

  const installer = capabilities.get('install.user_scoped');
  assert.ok(installer);
  assert.deepEqual(installer.platforms, ['macos', 'linux', 'windows']);
  assert.deepEqual(installer.installCommands, [
    'curl -fsSL https://spacedatanetwork.org/install.sh | bash',
    'irm https://spacedatanetwork.org/install.ps1 | iex'
  ]);
  assert.equal(installer.requiresGh, false);
  assert.equal(installer.requiresElevatedPrivileges, false);
  assert.ok(
    installer.tests.includes('deployment/release/published-install-smoke.test.mjs'),
    'installer parity must include published-endpoint smoke coverage'
  );

  const update = capabilities.get('update.daemon_in_place');
  assert.ok(update);
  assert.equal(update.providerServer, 'https://sdn.spaceaware.io/api/v1/updates');
  assert.ok(update.acceptance.some((item) => item.includes('rollback')), 'update acceptance must include rollback');
  assert.ok(update.acceptance.some((item) => item.includes('running daemon')), 'update acceptance must include running daemon');

  const liveDht = capabilities.get('ci.live_dht_cross_platform');
  assert.ok(liveDht);
  assert.deepEqual(liveDht.platforms, ['linux-docker', 'macos', 'windows']);
  assert.equal(liveDht.registrationWaitSeconds, 300);
  assert.deepEqual(liveDht.proves, [
    'peer-discovery',
    'identity-exchange',
    'provider-search',
    'data-search',
    'retrieval-query'
  ]);
});

test('docs and encrypted CA capabilities reference direct parity tests', () => {
  const capabilities = new Map(contract.capabilities.map((capability) => [capability.id, capability]));

  const docs = capabilities.get('docs.help_website');
  assert.ok(docs);
  for (const testPath of [
    'deployment/release/docs-parity.test.mjs',
    'desktop/test/unit/app-menu.spec.js',
    'desktop/test/unit/tray-sdn-links.spec.js',
    'desktop/test/unit/dashboard.spec.js'
  ]) {
    assert.ok(docs.tests.includes(testPath), `docs.help_website must reference ${testPath}`);
  }

  const encryptedCA = capabilities.get('encrypted_ca.maneuver_ephemeris');
  assert.ok(encryptedCA);
  for (const testPath of [
    'sdn-js/src/ui/conjunction-ui-source.test.ts',
    'sdn-js/src/ui/runtime/sdn-backend-desktop.test.ts',
    'sdn-js/src/ui/runtime/sdn-backend-remote.test.ts'
  ]) {
    assert.ok(encryptedCA.tests.includes(testPath), `encrypted_ca.maneuver_ephemeris must reference ${testPath}`);
  }

  const liveDht = capabilities.get('ci.live_dht_cross_platform');
  assert.ok(liveDht);
  assert.deepEqual(liveDht.surfaces, ['cli', 'release'], 'live-DHT release smoke must not overclaim Desktop UI execution');
});

test('every parity contract test reference exists in the repo', () => {
  for (const capability of contract.capabilities) {
    for (const testPath of capability.tests) {
      assert.equal(
        existsSync(join(repoRoot, testPath)),
        true,
        `${capability.id} references missing test file ${testPath}`
      );
    }
  }
});
