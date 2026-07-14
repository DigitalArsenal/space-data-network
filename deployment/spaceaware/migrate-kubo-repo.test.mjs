import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import {
  chmod,
  mkdir,
  mkdtemp,
  readFile,
  rm,
  stat,
  writeFile,
} from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { delimiter, join } from 'node:path';
import { test } from 'node:test';

const scriptPath = new URL('./migrate-kubo-repo.sh', import.meta.url);
const deployScriptPath = new URL('../scripts/deploy.sh', import.meta.url);

const sourcePins = [
  'bafy-zeta',
  'bafy-alpha',
  'bafy-echo',
  'bafy-bravo',
  'bafy-delta',
  'bafy-charlie',
];

const stubDriver = String.raw`#!/usr/bin/env node
const { appendFileSync, cpSync, mkdirSync, readFileSync, readdirSync, writeFileSync } = require('node:fs');
const { basename, join } = require('node:path');

const command = process.argv[2];
const args = process.argv.slice(3);
const env = process.env;

appendFileSync(env.STUB_CALL_LOG, JSON.stringify({
  command,
  args,
  ipfsPath: env.IPFS_PATH || '',
}) + '\n');

function readStates() {
  return JSON.parse(readFileSync(env.STUB_SYSTEMCTL_STATE, 'utf8'));
}

function writeStates(states) {
  writeFileSync(env.STUB_SYSTEMCTL_STATE, JSON.stringify(states));
}

function finish(code = 0) {
  process.exit(code);
}

if (command === 'systemctl') {
  const action = args[0];
  const unit = args.at(-1);
  const states = readStates();
  if (action === 'is-active') {
    finish(states[unit] === 'active' ? 0 : 3);
  }
  if (action === 'start' || action === 'stop') {
    states[unit] = action === 'start' ? 'active' : 'inactive';
    writeStates(states);
  }
  finish();
}

if (command === 'findmnt') {
  if (env.STUB_MOUNT_PRESENT === '0') finish(1);
  process.stdout.write((env.SDN_KUBO_MIGRATION_VOLUME_MOUNT || '/mnt/volume_nyc3_01') + '\n');
  finish();
}

if (command === 'df') {
  const available = env.STUB_DF_AVAILABLE_KIB || '999999999';
  const mount = env.SDN_KUBO_MIGRATION_VOLUME_MOUNT || '/mnt/volume_nyc3_01';
  process.stdout.write('Filesystem 1024-blocks Used Available Capacity Mounted on\n');
  process.stdout.write('/dev/fixture 1000000000 1 ' + available + ' 1% ' + mount + '\n');
  finish();
}

if (command === 'rsync') {
  if (env.STUB_RSYNC_FAIL === '1') finish(23);
  const source = args.at(-2).replace(/\/$/, '');
  const destination = args.at(-1).replace(/\/$/, '');
  mkdirSync(destination, { recursive: true });
  for (const entry of readdirSync(source)) {
    cpSync(join(source, entry), join(destination, entry), {
      recursive: true,
      force: true,
      preserveTimestamps: true,
    });
  }
  finish();
}

if (command === 'ipfs') {
  const sourceRepo = env.SDN_KUBO_MIGRATION_SOURCE_REPO;
  const isSource = env.IPFS_PATH === sourceRepo;
  if (args[0] === 'id') {
    process.stdout.write(isSource
      ? (env.STUB_SOURCE_PEER_ID || '12D3KooWFixturePeer')
      : (env.STUB_DEST_PEER_ID || env.STUB_SOURCE_PEER_ID || '12D3KooWFixturePeer'));
    finish();
  }
  if (args[0] === 'pin' && args[1] === 'ls') {
    const pins = JSON.parse(isSource ? env.STUB_SOURCE_PINS : env.STUB_DEST_PINS);
    const requestedCid = args.slice(2).find((argument) => !argument.startsWith('-'));
    if (requestedCid) {
      if (!pins.includes(requestedCid)) finish(1);
      process.stdout.write(requestedCid + '\n');
      finish();
    }
    process.stdout.write(pins.join('\n') + (pins.length ? '\n' : ''));
    finish();
  }
  if (args[0] === 'config' && args[1] === 'Datastore.StorageMax') finish();
  finish(2);
}

if (command === 'curl') {
  const url = args.find((argument) => /^https?:/.test(argument)) || '';
  if (env.STUB_CURL_FAILURE === 'api' && url.includes('/api/')) finish(22);
  if (env.STUB_CURL_FAILURE === 'gateway' && url.includes('/ipfs/')) finish(22);
  finish();
}

if (command === 'chown') finish();

process.stderr.write('unexpected fixture command: ' + basename(command) + '\n');
finish(127);
`;

async function run(command, args, options) {
  return await new Promise((resolve, reject) => {
    const child = spawn(command, args, options);
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => { stdout += chunk; });
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.on('error', reject);
    child.on('close', (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
}

async function makeFixture(t, {
  mountPresent = true,
  availableKib = 999_999_999,
  destinationPeerId = '12D3KooWFixturePeer',
  destinationPins = sourcePins,
  destinationFiles = [],
  previousDropIn = '[Service]\nEnvironment=IPFS_PATH=/srv/old-kubo\n',
  previousDropInMode = 0o640,
  sdnState = 'active',
  kuboState = 'active',
} = {}) {
  const root = await mkdtemp(join(tmpdir(), 'sdn-kubo-migration-'));
  t.after(async () => rm(root, { recursive: true, force: true }));

  const bin = join(root, 'bin');
  const sourceRepo = join(root, 'var-lib-ipfs');
  const volumeMount = join(root, 'volume');
  const destinationRepo = join(volumeMount, 'ipfs');
  const dropIn = join(root, 'systemd', 'kubo.service.d', '20-volume-repo.conf');
  const stateFile = join(root, 'systemctl-state.json');
  const callLog = join(root, 'calls.jsonl');
  const driver = join(root, 'stub-driver.cjs');

  await mkdir(bin, { recursive: true });
  await mkdir(join(sourceRepo, 'blocks'), { recursive: true });
  await mkdir(volumeMount, { recursive: true });
  for (const [relativePath, contents] of destinationFiles) {
    const path = join(destinationRepo, relativePath);
    await mkdir(join(path, '..'), { recursive: true });
    await writeFile(path, contents);
  }
  await writeFile(join(sourceRepo, 'config'), '{"Identity":{"PeerID":"fixture"}}\n');
  await writeFile(join(sourceRepo, 'blocks', 'source-only'), 'source repository must stay unchanged\n');
  await writeFile(stateFile, JSON.stringify({
    'space-data-network.service': sdnState,
    'kubo.service': kuboState,
  }));
  await writeFile(callLog, '');
  await writeFile(driver, stubDriver);
  await chmod(driver, 0o755);

  for (const command of ['systemctl', 'ipfs', 'rsync', 'findmnt', 'df', 'curl', 'chown']) {
    const wrapper = join(bin, command);
    await writeFile(
      wrapper,
      '#!/bin/sh\nexec ' + JSON.stringify(process.execPath) + ' "$STUB_DRIVER" ' + command + ' "$@"\n',
    );
    await chmod(wrapper, 0o755);
  }

  if (previousDropIn !== null) {
    await mkdir(join(root, 'systemd', 'kubo.service.d'), { recursive: true });
    await writeFile(dropIn, previousDropIn);
    await chmod(dropIn, previousDropInMode);
  }

  const env = {
    ...process.env,
    PATH: bin + delimiter + process.env.PATH,
    STUB_DRIVER: driver,
    STUB_CALL_LOG: callLog,
    STUB_SYSTEMCTL_STATE: stateFile,
    STUB_MOUNT_PRESENT: mountPresent ? '1' : '0',
    STUB_DF_AVAILABLE_KIB: String(availableKib),
    STUB_SOURCE_PEER_ID: '12D3KooWFixturePeer',
    STUB_DEST_PEER_ID: destinationPeerId,
    STUB_SOURCE_PINS: JSON.stringify(sourcePins),
    STUB_DEST_PINS: JSON.stringify(destinationPins),
    SDN_KUBO_MIGRATION_SOURCE_REPO: sourceRepo,
    SDN_KUBO_MIGRATION_DESTINATION_REPO: destinationRepo,
    SDN_KUBO_MIGRATION_VOLUME_MOUNT: volumeMount,
    SDN_KUBO_MIGRATION_DROP_IN: dropIn,
    SDN_KUBO_MIGRATION_HEADROOM_KIB: '1024',
    SDN_KUBO_MIGRATION_HTTP_ATTEMPTS: '1',
    SDN_KUBO_MIGRATION_HTTP_DELAY_SECONDS: '0',
    SDN_KUBO_MIGRATION_ALLOW_NON_ROOT: '1',
  };

  return {
    root,
    sourceRepo,
    destinationRepo,
    dropIn,
    stateFile,
    callLog,
    previousDropIn,
    previousDropInMode,
    env,
    async run(extraEnv = {}) {
      return run('/bin/bash', [scriptPath.pathname], {
        env: { ...env, ...extraEnv },
        cwd: root,
      });
    },
    async calls() {
      const contents = await readFile(callLog, 'utf8');
      return contents.trim() ? contents.trim().split('\n').map(JSON.parse) : [];
    },
    async states() {
      return JSON.parse(await readFile(stateFile, 'utf8'));
    },
  };
}

async function assertPriorStateRestored(fixture, expectedStates) {
  assert.deepEqual(await fixture.states(), expectedStates);
  if (fixture.previousDropIn === null) {
    await assert.rejects(stat(fixture.dropIn), { code: 'ENOENT' });
    return;
  }
  assert.equal(await readFile(fixture.dropIn, 'utf8'), fixture.previousDropIn);
  assert.equal((await stat(fixture.dropIn)).mode & 0o777, fixture.previousDropInMode);
}

test('production defaults and SDN systemd allowlist use the hosted volume paths', async () => {
  const migration = await readFile(scriptPath, 'utf8');
  const deploy = await readFile(deployScriptPath, 'utf8');

  assert.match(migration, /^set -Eeuo pipefail$/m);
  assert.match(migration, /:-\/var\/lib\/ipfs}/);
  assert.match(migration, /:-\/mnt\/volume_nyc3_01\/ipfs}/);
  assert.match(migration, /:-\/etc\/systemd\/system\/kubo\.service\.d\/20-volume-repo\.conf}/);
  assert.match(migration, /:-ipfs:ipfs}/);
  assert.match(migration, /:-120GB}/);
  assert.match(deploy, /ReadWritePaths=\/opt\/data \/var\/lib\/spacedatanetwork \/mnt\/volume_nyc3_01\/ipfs/);
});

test('refuses to migrate when the destination volume is not mounted', async (t) => {
  const fixture = await makeFixture(t, { mountPresent: false });
  const result = await fixture.run();

  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /not a mounted filesystem/i);
  assert.equal((await fixture.calls()).some(({ command }) => command === 'rsync'), false);
  await assertPriorStateRestored(fixture, {
    'space-data-network.service': 'active',
    'kubo.service': 'active',
  });
});

test('refuses to migrate when free space is below source size plus headroom', async (t) => {
  const fixture = await makeFixture(t, { availableKib: 1 });
  const result = await fixture.run();

  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /insufficient free space/i);
  assert.equal((await fixture.calls()).some(({ command }) => command === 'rsync'), false);
  await assertPriorStateRestored(fixture, {
    'space-data-network.service': 'active',
    'kubo.service': 'active',
  });
});

test('refuses to merge a source repository into a non-empty destination', async (t) => {
  const fixture = await makeFixture(t, {
    destinationFiles: [['unrelated-data', 'must not be overwritten\n']],
  });
  const result = await fixture.run();

  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /destination repository is not empty/i);
  assert.equal((await fixture.calls()).some(({ command }) => command === 'rsync'), false);
  await assertPriorStateRestored(fixture, {
    'space-data-network.service': 'active',
    'kubo.service': 'active',
  });
});

test('peer-ID mismatch restores the exact drop-in and every prior service-state combination', async (t) => {
  for (const sdnState of ['active', 'inactive']) {
    for (const kuboState of ['active', 'inactive']) {
      await t.test(`${sdnState} SDN, ${kuboState} Kubo`, async (subtest) => {
        const fixture = await makeFixture(subtest, {
          destinationPeerId: '12D3KooWDifferentPeer',
          sdnState,
          kuboState,
        });
        const result = await fixture.run();

        assert.notEqual(result.code, 0);
        assert.match(result.stderr, /peer ID mismatch/i);
        await assertPriorStateRestored(fixture, {
          'space-data-network.service': sdnState,
          'kubo.service': kuboState,
        });
      });
    }
  }
});

test('lower post-copy recursive pin count fails and removes a newly installed drop-in', async (t) => {
  const fixture = await makeFixture(t, {
    destinationPins: sourcePins.slice(0, -1),
    previousDropIn: null,
    sdnState: 'inactive',
    kuboState: 'active',
  });
  const result = await fixture.run();

  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /recursive pin count decreased/i);
  await assertPriorStateRestored(fixture, {
    'space-data-network.service': 'inactive',
    'kubo.service': 'active',
  });
});

test('same pin count still fails when one deterministic sample pin is absent', async (t) => {
  const fixture = await makeFixture(t, {
    destinationPins: [
      'bafy-zeta',
      'bafy-alpha',
      'bafy-theta',
      'bafy-bravo',
      'bafy-delta',
      'bafy-charlie',
    ],
  });
  const result = await fixture.run();

  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /sample recursive pin missing/i);
  await assertPriorStateRestored(fixture, {
    'space-data-network.service': 'active',
    'kubo.service': 'active',
  });
});

test('copies without deleting source, verifies Kubo, then starts SDN', async (t) => {
  const fixture = await makeFixture(t);
  const sourceConfigBefore = await readFile(join(fixture.sourceRepo, 'config'));
  const sourceBlockBefore = await readFile(join(fixture.sourceRepo, 'blocks', 'source-only'));
  const result = await fixture.run();

  assert.equal(result.code, 0, result.stderr);
  assert.equal(await readFile(join(fixture.sourceRepo, 'config'), 'utf8'), sourceConfigBefore.toString());
  assert.equal(
    await readFile(join(fixture.sourceRepo, 'blocks', 'source-only'), 'utf8'),
    sourceBlockBefore.toString(),
  );
  assert.equal(
    await readFile(fixture.dropIn, 'utf8'),
    `[Service]\nEnvironment=IPFS_PATH=${fixture.destinationRepo}\nReadWritePaths=${fixture.destinationRepo}\n`,
  );
  assert.deepEqual(await fixture.states(), {
    'space-data-network.service': 'active',
    'kubo.service': 'active',
  });

  const calls = await fixture.calls();
  const indexOf = (predicate) => calls.findIndex(predicate);
  const sdnStop = indexOf(({ command, args }) => command === 'systemctl' && args[0] === 'stop' && args[1] === 'space-data-network.service');
  const kuboStop = indexOf(({ command, args }) => command === 'systemctl' && args[0] === 'stop' && args[1] === 'kubo.service');
  const rsync = indexOf(({ command }) => command === 'rsync');
  const kuboStart = indexOf(({ command, args }) => command === 'systemctl' && args[0] === 'start' && args[1] === 'kubo.service');
  const apiCheck = indexOf(({ command, args }) => command === 'curl' && args.some((arg) => arg.includes('/api/v0/id')));
  const destinationIdentityCheck = indexOf(({ command, args, ipfsPath }) =>
    command === 'ipfs'
      && args[0] === 'id'
      && ipfsPath === fixture.destinationRepo);
  const gatewayCheck = indexOf(({ command, args }) => command === 'curl' && args.some((arg) => arg.includes('/ipfs/')));
  const sdnStart = indexOf(({ command, args }) => command === 'systemctl' && args[0] === 'start' && args[1] === 'space-data-network.service');

  assert.ok(sdnStop >= 0 && sdnStop < kuboStop, 'SDN must stop before Kubo');
  assert.ok(kuboStop < rsync && rsync < kuboStart, 'copy must happen while Kubo is stopped');
  assert.ok(
    kuboStart < apiCheck
      && apiCheck < destinationIdentityCheck
      && destinationIdentityCheck < gatewayCheck
      && gatewayCheck < sdnStart,
    'Kubo API readiness must precede repository checks, gateway checks, and SDN start',
  );

  const rsyncCall = calls[rsync];
  assert.deepEqual(rsyncCall.args.slice(0, 2), ['-aHAX', '--numeric-ids']);
  assert.equal(rsyncCall.args.includes('--delete'), false);
  assert.ok(calls.some(({ command, args, ipfsPath }) =>
    command === 'ipfs'
      && args.join(' ') === 'config Datastore.StorageMax 120GB'
      && ipfsPath === fixture.destinationRepo));
  assert.ok(calls.some(({ command, args }) =>
    command === 'chown'
      && args.join(' ') === `-R ipfs:ipfs ${fixture.destinationRepo}`));

  const verifiedSamples = calls
    .filter(({ command, args, ipfsPath }) =>
      command === 'ipfs'
        && args[0] === 'pin'
        && args[1] === 'ls'
        && args.some((arg) => arg.startsWith('bafy-'))
        && ipfsPath === fixture.destinationRepo)
    .map(({ args }) => args.find((arg) => arg.startsWith('bafy-')));
  assert.deepEqual(verifiedSamples, [
    'bafy-alpha',
    'bafy-bravo',
    'bafy-charlie',
    'bafy-delta',
    'bafy-echo',
  ]);
});
