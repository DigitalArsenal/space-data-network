import test from 'node:test';
import assert from 'node:assert/strict';

import {
  DEFAULT_EXPECTED_ROLES,
  DEFAULT_DHT_REGISTRATION_WAIT_MS,
  LIVE_DHT_BOOTSTRAP_PEERS,
  buildDockerSmokeCommand,
  buildSmokeFileID,
  extractSmokeRolesFromDatasetPNMEntries,
  generateLiveDHTDaemonConfig,
  normalizeExpectedRoles,
  resolveDHTRegistrationWaitMs
} from './live-dht-client-smoke.mjs';

test('live DHT daemon config uses public bootstrap and disables private discovery', () => {
  const config = generateLiveDHTDaemonConfig({
    adminPort: 5147,
    storagePath: '/tmp/sdn-live-dht-store',
    bootstrapPeers: LIVE_DHT_BOOTSTRAP_PEERS
  });

  assert.match(config, /listen_addr: 127\.0\.0\.1:5147/);
  assert.match(config, /path: "?\/tmp\/sdn-live-dht-store"?/);
  assert.match(config, /enable_dht: true/);
  assert.match(config, /enable_mdns: false/);
  assert.match(config, /\/dnsaddr\/bootstrap\.spacedatanetwork\.org/);
  assert.doesNotMatch(config, /tailscale/i);
  assert.doesNotMatch(config, /private/i);
});

test('registration wait enforces at least five minutes', () => {
  assert.equal(DEFAULT_DHT_REGISTRATION_WAIT_MS, 300_000);
  assert.equal(resolveDHTRegistrationWaitMs(1_000), 300_000);
  assert.equal(resolveDHTRegistrationWaitMs(300_000), 300_000);
  assert.equal(resolveDHTRegistrationWaitMs(420_000), 420_000);
});

test('smoke file IDs encode run, role, schema, and nonce for cross-run filtering', () => {
  assert.equal(
    buildSmokeFileID({
      runId: '12345-2',
      role: 'windows-native',
      schema: 'PNM.fbs',
      nonce: 'abcdef'
    }),
    'sdn-ci:12345-2:windows-native:PNM.fbs:abcdef'
  );
});

test('dataset PNM listings are reduced to the roles observed for this run', () => {
  const roles = extractSmokeRolesFromDatasetPNMEntries([
    { fileId: 'sdn-ci:run-7:linux-docker:PNM.fbs:aaa' },
    { fileId: 'sdn-ci:run-7:windows-native:PNM.fbs:bbb' },
    { fileId: 'sdn-ci:other:macos-native:PNM.fbs:ccc' },
    { fileId: 'celestrak:gp:OMM.fbs:2026-06-23T00:00:00Z' },
    {}
  ], 'run-7');

  assert.deepEqual([...roles].sort(), ['linux-docker', 'windows-native']);
});

test('expected roles default to Linux Docker plus native macOS and Windows', () => {
  assert.deepEqual(DEFAULT_EXPECTED_ROLES, ['linux-docker', 'macos-native', 'windows-native']);
  assert.deepEqual(
    normalizeExpectedRoles(' linux-docker,macos-native,windows-native '),
    DEFAULT_EXPECTED_ROLES
  );
});

test('Linux Docker command runs the same smoke harness from a Node container', () => {
  const command = buildDockerSmokeCommand({
    image: 'node:24-bookworm',
    workspace: '/workspace/sdn',
    archivePath: '/workspace/sdn/dist/spacedatanetwork-linux-amd64.tar.gz',
    role: 'linux-docker',
    reportPath: '/workspace/sdn/dist/live-dht/linux.json',
    runId: 'run-7',
    pnmBase64Env: 'SDN_CI_PNM_BASE64',
    expectedRoles: DEFAULT_EXPECTED_ROLES
  });

  assert.deepEqual(command.slice(0, 3), ['docker', 'run', '--rm']);
  assert(command.includes('node:24-bookworm'));
  assert(command.includes('deployment/release/live-dht-client-smoke.mjs'));
  assert(command.includes('--dht-registration-wait-ms'));
  assert(command.includes(String(DEFAULT_DHT_REGISTRATION_WAIT_MS)));
  assert.doesNotMatch(command.join(' '), /tailscale/i);
});
