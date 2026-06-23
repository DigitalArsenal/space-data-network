#!/usr/bin/env node

import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:net';
import {
  createWriteStream,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync
} from 'node:fs';
import { basename, dirname, join, resolve, sep } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';

export const DEFAULT_EXPECTED_ROLES = ['linux-docker', 'macos-native', 'windows-native'];
export const DEFAULT_DHT_REGISTRATION_WAIT_MS = 300_000;
export const DEFAULT_TOTAL_TIMEOUT_MS = 1_200_000;
export const DEFAULT_PUBLISH_INTERVAL_MS = 15_000;
export const LIVE_DHT_BOOTSTRAP_PEERS = [
  '/dnsaddr/bootstrap.spacedatanetwork.org/p2p/16Uiu2HAmP8KTvYP2i7Ef2Lf7Vbn5beZf2aMTpq4pmQAK6SjRphYT',
  '/dnsaddr/bootstrap.spacedatanetwork.org/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
  '/ip4/159.203.150.8/tcp/4001/p2p/16Uiu2HAmP8KTvYP2i7Ef2Lf7Vbn5beZf2aMTpq4pmQAK6SjRphYT',
  '/ip4/167.172.219.213/tcp/4001/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3'
];

function log(message) {
  console.log(`[live-dht-smoke] ${message}`);
}

function sleep(ms) {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, ms));
}

function parsePositiveInt(value, fallback) {
  const parsed = Number.parseInt(String(value ?? ''), 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

export function resolveDHTRegistrationWaitMs(value) {
  return Math.max(DEFAULT_DHT_REGISTRATION_WAIT_MS, parsePositiveInt(value, DEFAULT_DHT_REGISTRATION_WAIT_MS));
}

function normalizeList(input) {
  if (Array.isArray(input)) {
    return input;
  }
  return String(input ?? '')
    .split(',')
    .map((part) => part.trim())
    .filter(Boolean);
}

export function normalizeExpectedRoles(input) {
  const roles = normalizeList(input);
  return roles.length > 0 ? roles : [...DEFAULT_EXPECTED_ROLES];
}

function normalizeBootstrapPeers(input) {
  const peers = normalizeList(input);
  return peers.length > 0 ? peers : [...LIVE_DHT_BOOTSTRAP_PEERS];
}

function yamlString(value) {
  return JSON.stringify(String(value));
}

export function generateLiveDHTDaemonConfig({ adminPort, storagePath, bootstrapPeers = LIVE_DHT_BOOTSTRAP_PEERS }) {
  const bootstrapYaml = bootstrapPeers.map((peer) => `    - ${peer}`).join('\n');
  const peerRegistryPath = join(storagePath, 'peers.db').split(sep).join('/');
  const normalizedStoragePath = storagePath.split(sep).join('/');

  return `mode: full

network:
  listen:
    - /ip4/0.0.0.0/tcp/0
    - /ip4/0.0.0.0/udp/0/quic-v1
  bootstrap:
${bootstrapYaml}
  max_connections: 1000
  enable_relay: true

storage:
  path: ${yamlString(normalizedStoragePath)}
  max_size: 10GB
  gc_interval: 1h

schemas:
  validate: true
  strict: true

admin:
  enabled: true
  listen_addr: 127.0.0.1:${adminPort}
  require_auth: false
  session_expiry: 24h
  totp_required: false
  tls_enabled: false
  tls_mode: disabled

peers:
  registry_path: ${yamlString(peerRegistryPath)}
  strict_mode: false
  enable_dht: true
  enable_mdns: false

tor:
  enabled: false
  hidden_service_enabled: false

flows:
  enabled: false

setup:
  token_expiry: 10m
  data_path: ${yamlString(normalizedStoragePath)}
`;
}

function safeFileIDToken(value, name) {
  const token = String(value ?? '').trim();
  if (!token) {
    throw new Error(`${name} is required`);
  }
  return token.replace(/[^A-Za-z0-9_.-]/g, '_');
}

export function buildSmokeFileID({ runId, role, schema = 'PNM.fbs', nonce }) {
  return [
    'sdn-ci',
    safeFileIDToken(runId, 'runId'),
    safeFileIDToken(role, 'role'),
    safeFileIDToken(schema, 'schema'),
    safeFileIDToken(nonce ?? Date.now().toString(36), 'nonce')
  ].join(':');
}

export function extractSmokeRolesFromDatasetPNMEntries(entries, runId) {
  const safeRun = safeFileIDToken(runId, 'runId');
  const roles = new Set();
  for (const entry of entries ?? []) {
    const fileID = String(entry?.fileId ?? entry?.FileID ?? entry?.file_id ?? '').trim();
    const parts = fileID.split(':');
    if (parts.length < 5 || parts[0] !== 'sdn-ci' || parts[1] !== safeRun || parts[3] !== 'PNM.fbs') {
      continue;
    }
    if (parts[2]) {
      roles.add(parts[2]);
    }
  }
  return roles;
}

export function buildDockerSmokeCommand({
  image = 'node:24-bookworm',
  workspace,
  archivePath,
  role,
  reportPath,
  runId,
  pnmBase64Env = 'SDN_CI_PNM_BASE64',
  expectedRoles = DEFAULT_EXPECTED_ROLES,
  dhtRegistrationWaitMs = DEFAULT_DHT_REGISTRATION_WAIT_MS,
  timeoutMs = DEFAULT_TOTAL_TIMEOUT_MS
}) {
  return [
    'docker',
    'run',
    '--rm',
    '-e',
    `${pnmBase64Env}`,
    '-v',
    `${workspace}:/workspace`,
    '-w',
    '/workspace',
    image,
    'node',
    'deployment/release/live-dht-client-smoke.mjs',
    '--archive',
    archivePath,
    '--role',
    role,
    '--report',
    reportPath,
    '--run-id',
    runId,
    '--pnm-base64-env',
    pnmBase64Env,
    '--expect-roles',
    expectedRoles.join(','),
    '--dht-registration-wait-ms',
    String(dhtRegistrationWaitMs),
    '--timeout-ms',
    String(timeoutMs)
  ];
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const key = argv[index];
    if (!key.startsWith('--')) {
      throw new Error(`Unexpected argument: ${key}`);
    }
    const value = argv[index + 1];
    if (!value || value.startsWith('--')) {
      throw new Error(`Missing value for ${key}`);
    }
    options[key.slice(2).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = value;
    index += 1;
  }
  return options;
}

function runCommand(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env,
    encoding: 'utf8',
    stdio: options.stdio ?? ['ignore', 'pipe', 'pipe']
  });
  if (result.status !== 0 && !options.allowFailure) {
    const stdout = result.stdout ? `\nstdout:\n${result.stdout}` : '';
    const stderr = result.stderr ? `\nstderr:\n${result.stderr}` : '';
    throw new Error(`${command} ${args.join(' ')} failed with exit ${result.status}${stdout}${stderr}`);
  }
  return {
    status: result.status ?? 1,
    stdout: result.stdout ?? '',
    stderr: result.stderr ?? ''
  };
}

function powershellLiteral(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

export function buildWindowsExpandArchiveCommand(archivePath, extractDir) {
  return `Expand-Archive -LiteralPath ${powershellLiteral(archivePath)} -DestinationPath ${powershellLiteral(extractDir)} -Force`;
}

function extractArchive(archivePath, extractDir) {
  if (/\.tar\.gz$/.test(archivePath)) {
    runCommand('tar', ['-xzf', archivePath, '-C', extractDir]);
    return;
  }
  if (/\.zip$/.test(archivePath)) {
    if (process.platform === 'win32') {
      runCommand('powershell', [
        '-NoLogo',
        '-NoProfile',
        '-Command',
        buildWindowsExpandArchiveCommand(archivePath, extractDir)
      ]);
      return;
    }
    runCommand('unzip', ['-q', archivePath, '-d', extractDir]);
    return;
  }
  throw new Error(`Unsupported archive type: ${archivePath}`);
}

function walkFiles(root) {
  const files = [];
  const stack = [root];
  while (stack.length > 0) {
    const current = stack.pop();
    if (!current || !existsSync(current)) {
      continue;
    }
    const currentStat = statSync(current);
    if (currentStat.isFile()) {
      files.push(current);
      continue;
    }
    if (!currentStat.isDirectory()) {
      continue;
    }
    for (const entry of readdirSync(current)) {
      stack.push(join(current, entry));
    }
  }
  return files.sort();
}

function findBundleBinary(extractDir) {
  const binaryName = process.platform === 'win32' ? 'spacedatanetwork.exe' : 'spacedatanetwork';
  const matches = walkFiles(extractDir).filter((file) => basename(file) === binaryName && basename(dirname(file)) === 'bin');
  if (matches.length === 0) {
    throw new Error(`Unable to locate bin/${binaryName} in ${extractDir}`);
  }
  return matches[0];
}

async function findFreePort() {
  return new Promise((resolvePort, reject) => {
    const server = createServer();
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      const port = typeof address === 'object' && address ? address.port : 0;
      server.close(() => resolvePort(port));
    });
  });
}

async function fetchJSON(url, options = {}) {
  const timeoutMs = parsePositiveInt(options.timeoutMs, 10_000);
  const response = await fetch(url, {
    method: options.method ?? 'GET',
    headers: options.headers,
    body: options.body,
    signal: AbortSignal.timeout(timeoutMs)
  });
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`${options.method ?? 'GET'} ${url} failed with ${response.status}: ${text}`);
  }
  return text ? JSON.parse(text) : {};
}

async function waitForAdmin(baseURL, deadline, daemonState) {
  let lastError;
  while (Date.now() < deadline) {
    if (daemonState.exited) {
      throw new Error(`daemon exited before admin API was ready: ${daemonState.exitSummary}`);
    }
    try {
      return await fetchJSON(`${baseURL}/api/v1/id`, { timeoutMs: 5_000 });
    } catch (error) {
      lastError = error;
      await sleep(1_000);
    }
  }
  throw new Error(`admin API was not ready before timeout: ${lastError?.message ?? 'unknown error'}`);
}

async function currentPeerCount(baseURL) {
  const stats = await fetchJSON(`${baseURL}/api/v1/stats`, { timeoutMs: 5_000 });
  return parsePositiveInt(stats.connected_peers, 0);
}

async function publishPNM(baseURL, pnmBase64) {
  return fetchJSON(`${baseURL}/api/v1/pubsub/publish`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ schema: 'PNM.fbs', data: pnmBase64 }),
    timeoutMs: 10_000
  });
}

function listDatasetPNMs(binaryPath, configPath, runId) {
  const result = runCommand(binaryPath, [
    '--config',
    configPath,
    'dataset-pnms',
    'list',
    '--limit',
    '2000',
    '--file-id-contains',
    `sdn-ci:${safeFileIDToken(runId, 'runId')}:`
  ], { allowFailure: true });
  if (result.status !== 0) {
    throw new Error(result.stderr || result.stdout || 'dataset-pnms list failed');
  }
  return JSON.parse(result.stdout || '[]');
}

function writeReport(reportPath, report) {
  mkdirSync(dirname(reportPath), { recursive: true });
  writeFileSync(reportPath, `${JSON.stringify(report, null, 2)}\n`);
}

function startDaemon(binaryPath, configPath, logPath) {
  mkdirSync(dirname(logPath), { recursive: true });
  const logStream = createWriteStream(logPath, { flags: 'a' });
  const child = spawn(binaryPath, ['--config', configPath, 'daemon'], {
    env: {
      ...process.env,
      SDN_KEY_PASSWORD: process.env.SDN_KEY_PASSWORD || 'sdn-live-dht-ci'
    },
    stdio: ['ignore', 'pipe', 'pipe']
  });
  const state = { exited: false, exitSummary: '' };
  child.stdout.pipe(logStream);
  child.stderr.pipe(logStream);
  child.on('exit', (code, signal) => {
    state.exited = true;
    state.exitSummary = `code=${code} signal=${signal}`;
  });
  return { child, state, logStream };
}

async function stopDaemon(child) {
  if (!child || child.killed || child.exitCode !== null) {
    return;
  }
  child.kill('SIGTERM');
  await Promise.race([
    new Promise((resolveStop) => child.once('exit', resolveStop)),
    sleep(10_000).then(() => {
      if (child.exitCode === null) {
        child.kill('SIGKILL');
      }
    })
  ]);
}

export async function runLiveDHTSmoke(options) {
  const archivePath = resolve(options.archive);
  if (!existsSync(archivePath)) {
    throw new Error(`Archive not found: ${archivePath}`);
  }
  const role = safeFileIDToken(options.role, 'role');
  const expectedRoles = normalizeExpectedRoles(options.expectRoles ?? process.env.SDN_LIVE_DHT_EXPECT_ROLES);
  const runId = safeFileIDToken(options.runId ?? process.env.SDN_LIVE_DHT_RUN_ID ?? `${Date.now()}`, 'runId');
  const pnmBase64 = String(
    options.pnmBase64 ??
    (options.pnmBase64Env ? process.env[options.pnmBase64Env] : undefined) ??
    process.env.SDN_CI_PNM_BASE64 ??
    ''
  ).trim();
  if (!pnmBase64) {
    throw new Error('--pnm-base64, --pnm-base64-env, or SDN_CI_PNM_BASE64 is required');
  }

  const dhtRegistrationWaitMs = resolveDHTRegistrationWaitMs(options.dhtRegistrationWaitMs);
  const timeoutMs = Math.max(
    dhtRegistrationWaitMs + 180_000,
    parsePositiveInt(options.timeoutMs ?? process.env.SDN_LIVE_DHT_TIMEOUT_MS, DEFAULT_TOTAL_TIMEOUT_MS)
  );
  const publishIntervalMs = parsePositiveInt(options.publishIntervalMs, DEFAULT_PUBLISH_INTERVAL_MS);
  const reportPath = resolve(options.report ?? join('dist', 'live-dht', 'reports', `${role}.json`));
  const workDir = mkdtempSync(join(tmpdir(), `sdn-live-dht-${role}-`));
  const extractDir = join(workDir, 'bundle');
  const storagePath = join(workDir, 'store');
  mkdirSync(extractDir, { recursive: true });
  mkdirSync(storagePath, { recursive: true });
  const logPath = resolve(options.log ?? join(dirname(reportPath), '..', 'logs', `${role}.log`));

  let daemon;
  const seenRoles = new Set([role]);
  const observedFileIDs = [];
  const peerCounts = [];
  const publishErrors = [];
  let peerID = '';
  let success = false;
  let failure = '';

  try {
    extractArchive(archivePath, extractDir);
    const binaryPath = findBundleBinary(extractDir);
    const adminPort = await findFreePort();
    const configPath = join(workDir, 'config.yaml');
    const bootstrapPeers = normalizeBootstrapPeers(options.bootstrapPeers ?? process.env.SDN_LIVE_DHT_BOOTSTRAP_PEERS);
    writeFileSync(configPath, generateLiveDHTDaemonConfig({ adminPort, storagePath, bootstrapPeers }));

    daemon = startDaemon(binaryPath, configPath, logPath);
    const startedAt = Date.now();
    const deadline = startedAt + timeoutMs;
    const registrationReadyAt = startedAt + dhtRegistrationWaitMs;
    const baseURL = `http://127.0.0.1:${adminPort}`;

    log(`${role}: waiting for daemon admin API on ${baseURL}`);
    const identity = await waitForAdmin(baseURL, Math.min(deadline, startedAt + 120_000), daemon.state);
    peerID = identity.peer_id ?? '';
    log(`${role}: admin API ready peer=${peerID || 'unknown'}; waiting at least ${dhtRegistrationWaitMs}ms for DHT registration`);

    let nextPublishAt = 0;
    let nextStatusAt = 0;
    while (Date.now() < deadline) {
      if (daemon.state.exited) {
        throw new Error(`daemon exited during smoke test: ${daemon.state.exitSummary}`);
      }
      const now = Date.now();
      if (now >= nextPublishAt) {
        try {
          await publishPNM(baseURL, pnmBase64);
          log(`${role}: published smoke PNM`);
        } catch (error) {
          publishErrors.push(error.message);
          log(`${role}: publish failed: ${error.message}`);
        }
        nextPublishAt = now + publishIntervalMs;
      }

      if (now >= nextStatusAt) {
        try {
          const count = await currentPeerCount(baseURL);
          peerCounts.push(count);
          const entries = listDatasetPNMs(binaryPath, configPath, runId);
          for (const entry of entries) {
            if (entry?.fileId) {
              observedFileIDs.push(entry.fileId);
            }
          }
          for (const observedRole of extractSmokeRolesFromDatasetPNMEntries(entries, runId)) {
            seenRoles.add(observedRole);
          }
          log(`${role}: peers=${count} seenRoles=${[...seenRoles].sort().join(',')}`);
        } catch (error) {
          log(`${role}: status poll failed: ${error.message}`);
        }
        nextStatusAt = now + 10_000;
      }

      const missingRoles = expectedRoles.filter((expectedRole) => !seenRoles.has(expectedRole));
      if (Date.now() >= registrationReadyAt && missingRoles.length === 0) {
        success = true;
        break;
      }
      await sleep(1_000);
    }

    if (!success) {
      const missingRoles = expectedRoles.filter((expectedRole) => !seenRoles.has(expectedRole));
      failure = `timed out after ${timeoutMs}ms; missing roles: ${missingRoles.join(', ') || 'none'}`;
      throw new Error(failure);
    }

    return {
      success: true,
      role,
      runId,
      peerID,
      expectedRoles,
      seenRoles: [...seenRoles].sort(),
      maxConnectedPeers: Math.max(0, ...peerCounts),
      dhtRegistrationWaitMs,
      timeoutMs,
      logPath,
      observedFileIDs: [...new Set(observedFileIDs)].sort(),
      publishErrors
    };
  } catch (error) {
    failure = failure || error.message;
    return {
      success: false,
      role,
      runId,
      peerID,
      expectedRoles,
      seenRoles: [...seenRoles].sort(),
      maxConnectedPeers: Math.max(0, ...peerCounts),
      dhtRegistrationWaitMs,
      timeoutMs,
      logPath,
      observedFileIDs: [...new Set(observedFileIDs)].sort(),
      publishErrors,
      error: failure
    };
  } finally {
    if (daemon) {
      await stopDaemon(daemon.child);
      daemon.logStream.end();
    }
    if (!options.keepWorkDir) {
      rmSync(workDir, { recursive: true, force: true });
    }
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const options = parseArgs(process.argv.slice(2));
  const report = await runLiveDHTSmoke(options);
  writeReport(resolve(options.report ?? join('dist', 'live-dht', 'reports', `${report.role}.json`)), report);
  if (!report.success) {
    console.error(report.error);
    process.exit(1);
  }
}
