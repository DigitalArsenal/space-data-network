import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

const contract = JSON.parse(readFileSync(new URL('./sdn-parity-contract.json', import.meta.url), 'utf8'));

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
