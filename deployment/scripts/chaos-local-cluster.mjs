#!/usr/bin/env node

import { execFileSync, spawnSync } from 'node:child_process';
import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(scriptDir, '../..');
const artifactPath = resolve(repoRoot, 'artifacts/chaos/docker-cluster-chaos-report.json');
const datastoreCheckpointPath = resolve(repoRoot, 'artifacts/chaos/docker-cluster-datastore-checkpoint.json');
const datastoreChaosScript = resolve(repoRoot, 'sdn-js/scripts/chaos-local-network.mjs');
const networkName = 'deployment_sdn-network';

const startedAt = new Date();
const report = {
  startedAt: startedAt.toISOString(),
  finishedAt: null,
  environment: {
    dockerVersion: capture('docker', ['version', '--format', '{{.Server.Version}}']).trim(),
    dockerInfo: capture('docker', ['info', '--format', '{{.OperatingSystem}} {{.Architecture}} {{.NCPU}} CPUs']).trim(),
  },
  steps: [],
  datastore: {},
  passed: false,
};

function capture(command, args) {
  const result = spawnSync(command, args, {
    cwd: repoRoot,
    encoding: 'utf8',
  });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} failed: ${result.stderr || result.stdout}`);
  }
  return `${result.stdout}${result.stderr}`;
}

function run(command, args) {
  execFileSync(command, args, { cwd: repoRoot, stdio: 'pipe' });
}

async function recordStep(name, action) {
  const started = performance.now();
  const step = { name, passed: false, durationMs: 0, error: null, evidence: null };
  report.steps.push(step);
  try {
    const evidence = await action();
    if (evidence !== undefined) {
      step.evidence = evidence;
    }
    step.passed = true;
  } catch (error) {
    step.error = error instanceof Error ? error.message : String(error);
    throw error;
  } finally {
    step.durationMs = Math.round(performance.now() - started);
  }
}

function runDatastoreChaos(args) {
  const result = spawnSync(process.execPath, [datastoreChaosScript, '--json', ...args], {
    cwd: repoRoot,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
  });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(`datastore chaos failed: ${result.stderr || result.stdout}`);
  }
  if (result.stderr.trim()) {
    throw new Error(`datastore chaos wrote stderr: ${result.stderr}`);
  }
  return JSON.parse(result.stdout);
}

function validateDatastoreChaosReport(datastoreReport, options = {}) {
  const {
    expectedRows,
    expectedNewPins,
    expectedReusedPins,
    expectedDownloadedBytes,
    maxDownloadedBytes,
    requireFaults = false,
    requireTargetMet = false,
  } = options;
  if (datastoreReport?.transport?.protocol !== '/space-data-network/flatsql-sync/1.0.0') {
    throw new Error(`unexpected datastore protocol: ${datastoreReport?.transport?.protocol}`);
  }
  if (datastoreReport.transport.remoteHttpFallback || datastoreReport.transport.sshFallback) {
    throw new Error('datastore chaos enabled an HTTP or SSH FlatSQL fallback');
  }
  if (!datastoreReport.summary.allVerified) {
    throw new Error('datastore chaos did not verify every consumer');
  }
  if (typeof expectedRows === 'number' && datastoreReport.summary.totalRows !== expectedRows) {
    throw new Error(`datastore rows ${datastoreReport.summary.totalRows}, want ${expectedRows}`);
  }
  if (typeof expectedNewPins === 'number' && datastoreReport.replication.newlyPinnedShards !== expectedNewPins) {
    throw new Error(`newly pinned shards ${datastoreReport.replication.newlyPinnedShards}, want ${expectedNewPins}`);
  }
  if (typeof expectedReusedPins === 'number' && datastoreReport.replication.reusedVerifiedShards !== expectedReusedPins) {
    throw new Error(`reused verified shards ${datastoreReport.replication.reusedVerifiedShards}, want ${expectedReusedPins}`);
  }
  if (typeof expectedDownloadedBytes === 'number' && datastoreReport.replication.downloadedBytes !== expectedDownloadedBytes) {
    throw new Error(`downloaded bytes ${datastoreReport.replication.downloadedBytes}, want ${expectedDownloadedBytes}`);
  }
  if (typeof maxDownloadedBytes === 'number' && datastoreReport.replication.downloadedBytes >= maxDownloadedBytes) {
    throw new Error(`downloaded bytes ${datastoreReport.replication.downloadedBytes} exceeded ceiling ${maxDownloadedBytes}`);
  }
  if (requireFaults) {
    for (const key of ['droppedRequests', 'corruptedResponses', 'partitionFailures', 'restartEvents', 'retryCount', 'verificationFailures']) {
      if (datastoreReport.chaos[key] <= 0) {
        throw new Error(`datastore chaos did not exercise ${key}`);
      }
    }
  }
  if (requireTargetMet && !datastoreReport.replication.targetMet) {
    throw new Error(`datastore chaos missed target utilization: ${datastoreReport.replication.wireSpeedUtilization}`);
  }
  if (datastoreReport.replication.wireSpeedUtilization > 1) {
    throw new Error(`datastore chaos reported impossible utilization: ${datastoreReport.replication.wireSpeedUtilization}`);
  }
  return summarizeDatastoreReport(datastoreReport);
}

function summarizeDatastoreReport(datastoreReport) {
  return {
    feedHead: datastoreReport.manifest.feedHead,
    segments: datastoreReport.manifest.segmentCount,
    rows: datastoreReport.summary.totalRows,
    allVerified: datastoreReport.summary.allVerified,
    downloadedBytes: datastoreReport.replication.downloadedBytes,
    providerBytes: datastoreReport.replication.providerBytes,
    peerBytes: datastoreReport.replication.peerBytes,
    newlyPinnedShards: datastoreReport.replication.newlyPinnedShards,
    reusedVerifiedShards: datastoreReport.replication.reusedVerifiedShards,
    targetMet: datastoreReport.replication.targetMet,
    wireSpeedUtilization: datastoreReport.replication.wireSpeedUtilization,
    remoteHttpFallback: datastoreReport.transport.remoteHttpFallback,
    sshFallback: datastoreReport.transport.sshFallback,
  };
}

function isRunning(container) {
  return capture('docker', ['inspect', '-f', '{{.State.Running}}', container]).trim() === 'true';
}

async function httpStatus(url, timeoutMs = 2000) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetch(url, { signal: controller.signal });
    return response.status;
  } finally {
    clearTimeout(timeout);
  }
}

async function waitFor(name, check, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs;
  let lastError;
  while (Date.now() < deadline) {
    try {
      if (await check()) {
        return;
      }
    } catch (error) {
      lastError = error;
    }
    await sleep(500);
  }
  const suffix = lastError ? `: ${lastError.message ?? lastError}` : '';
  throw new Error(`${name} did not recover within ${timeoutMs}ms${suffix}`);
}

async function waitForHealth(port) {
  await waitFor(`health endpoint ${port}`, async () => {
    const status = await httpStatus(`http://localhost:${port}/health`);
    return status === 200;
  });
}

async function waitForWsEndpoint(port) {
  await waitFor(`websocket endpoint ${port}`, async () => {
    const status = await httpStatus(`http://localhost:${port}/`);
    return status === 400 || status === 426;
  });
}

async function main() {
  let datastoreFaultReport;
  let datastoreResumeReport;
  const datastoreBaseArgs = [
    '--shards', '64',
    '--shard-bytes', '1048576',
    '--rows-per-shard', '2000',
    '--consumers', '4',
    '--concurrency', '16',
    '--bandwidth-mbps', '2000',
    '--checkpoint-file', datastoreCheckpointPath,
  ];
  rmSync(datastoreCheckpointPath, { force: true });

  await recordStep('baseline cluster is running', async () => {
    for (const container of ['sdn-full-1', 'sdn-full-2', 'sdn-edge-us', 'sdn-edge-eu', 'sdn-edge-asia', 'sdn-registry']) {
      if (!isRunning(container)) {
        throw new Error(`${container} is not running`);
      }
    }
    await waitForWsEndpoint(18080);
    await waitForWsEndpoint(8081);
    await waitForHealth(8091);
    await waitForHealth(8093);
    await waitForHealth(8095);
  });

  await recordStep('datastore convergence under FlatSQL sync chaos', async () => {
    datastoreFaultReport = runDatastoreChaos([
      ...datastoreBaseArgs,
      '--drop-every', '37',
      '--corrupt-every', '53',
      '--partition-every', '71',
      '--restart-every', '89',
    ]);
    const evidence = validateDatastoreChaosReport(datastoreFaultReport, {
      expectedRows: 64 * 2000 * 4,
      expectedNewPins: 64 * 4,
      expectedReusedPins: 0,
      requireFaults: true,
      requireTargetMet: true,
    });
    report.datastore.faultedConvergence = evidence;
    return evidence;
  });

  await recordStep('datastore resume skips verified feed-head shards', async () => {
    datastoreResumeReport = runDatastoreChaos(datastoreBaseArgs);
    if (datastoreResumeReport.manifest.feedHead !== datastoreFaultReport.manifest.feedHead) {
      throw new Error('datastore resume did not use the same feed head');
    }
    const evidence = validateDatastoreChaosReport(datastoreResumeReport, {
      expectedRows: 64 * 2000 * 4,
      expectedNewPins: 0,
      expectedReusedPins: 64 * 4,
      expectedDownloadedBytes: 0,
    });
    report.datastore.completedResume = evidence;
    return evidence;
  });

  await recordStep('datastore feed advance downloads only new shards', async () => {
    const advancedReport = runDatastoreChaos([
      ...datastoreBaseArgs,
      '--shards', '72',
    ]);
    if (advancedReport.manifest.feedHead === datastoreResumeReport.manifest.feedHead) {
      throw new Error('datastore feed advance did not produce a new feed head');
    }
    const evidence = validateDatastoreChaosReport(advancedReport, {
      expectedRows: 72 * 2000 * 4,
      expectedNewPins: 8 * 4,
      expectedReusedPins: 64 * 4,
      maxDownloadedBytes: advancedReport.manifest.byteCount * 4,
    });
    report.datastore.feedAdvance = evidence;
    return evidence;
  });

  await recordStep('pause and unpause edge relay', async () => {
    run('docker', ['pause', 'sdn-edge-eu']);
    try {
      await sleep(1000);
      const paused = capture('docker', ['inspect', '-f', '{{.State.Paused}}', 'sdn-edge-eu']).trim();
      if (paused !== 'true') {
        throw new Error('sdn-edge-eu did not enter paused state');
      }
    } finally {
      run('docker', ['unpause', 'sdn-edge-eu']);
    }
    await waitForHealth(8093);
  });

  await recordStep('restart secondary full node', async () => {
    run('docker', ['restart', '--time', '2', 'sdn-full-2']);
    await waitFor('sdn-full-2 running', () => isRunning('sdn-full-2'));
    await waitForWsEndpoint(8081);
  });

  await recordStep('disconnect and reconnect edge relay network', async () => {
    run('docker', ['network', 'disconnect', networkName, 'sdn-edge-asia']);
    try {
      await sleep(1000);
      const networks = capture('docker', ['inspect', '-f', '{{json .NetworkSettings.Networks}}', 'sdn-edge-asia']);
      if (networks.includes(networkName)) {
        throw new Error('sdn-edge-asia remained attached to the SDN network');
      }
    } finally {
      run('docker', ['network', 'connect', networkName, 'sdn-edge-asia']);
    }
    await waitForHealth(8095);
  });

  await recordStep('restart registry builder', async () => {
    run('docker', ['restart', '--time', '2', 'sdn-registry']);
    await waitFor('sdn-registry running', () => isRunning('sdn-registry'));
  });

  await recordStep('post-chaos peer connectivity still present', async () => {
    const logs = capture('docker', ['logs', '--tail=400', 'sdn-full-2']);
    if (!/Connected to bootstrap peer|peer:connect/.test(logs)) {
      throw new Error('sdn-full-2 logs do not show bootstrap peer connectivity');
    }
  });

  report.passed = report.steps.every((step) => step.passed);
}

try {
  await main();
} finally {
  report.finishedAt = new Date().toISOString();
  mkdirSync(dirname(artifactPath), { recursive: true });
  writeFileSync(artifactPath, `${JSON.stringify(report, null, 2)}\n`);
  console.log(`Wrote ${artifactPath}`);
}

if (!report.passed) {
  process.exitCode = 1;
}
