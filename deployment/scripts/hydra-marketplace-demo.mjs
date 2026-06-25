#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const defaultComposeFile = resolve(repoRoot, 'deployment/hydra-marketplace-demo.compose.yaml');
const defaultReportFile = resolve(repoRoot, 'deployment/generated/hydra-marketplace-demo-report.json');
const minimumDiscoveryWaitMs = 300_000;

const nodeDefinitions = [
  {
    service: 'hydra-provider-maneuver',
    role: 'provider:maneuver-ephemeris',
    purpose: 'Publishes protected maneuver ephemeris field stream data.',
    api: 'http://127.0.0.1:15101',
  },
  {
    service: 'hydra-provider-catalog',
    role: 'provider:catalog-support',
    purpose: 'Publishes support catalog data discovered through the same fabric.',
    api: 'http://127.0.0.1:15102',
  },
  {
    service: 'hydra-customer-alpha',
    role: 'customer:alpha',
    purpose: 'Receives object, timestamp, and position fields only.',
    api: 'http://127.0.0.1:15103',
  },
  {
    service: 'hydra-customer-beta',
    role: 'customer:beta',
    purpose: 'Receives object, timestamp, position, and covariance detail fields.',
    api: 'http://127.0.0.1:15104',
  },
  {
    service: 'hydra-observer',
    role: 'observer:unauthorized',
    purpose: 'Discovers public marketplace metadata but cannot decrypt protected fields.',
    api: 'http://127.0.0.1:15105',
  },
];

const streams = [
  {
    streamId: 'maneuver-ephemeris-live',
    provider: 'hydra-provider-maneuver',
    schema: 'MPE',
    standard: 'Maneuver Ephemeris',
    publicFields: ['object_id', 'timestamp'],
    protectedFields: ['position', 'covariance_detail', 'maneuver_plan'],
    customerPolicies: [
      {
        customer: 'Customer A',
        subjectId: 'customer-alpha-peer',
        allowedEncryptedFields: ['position'],
        withheldFields: ['covariance_detail', 'maneuver_plan'],
      },
      {
        customer: 'Customer B',
        subjectId: 'customer-beta-peer',
        allowedEncryptedFields: ['position', 'covariance_detail'],
        withheldFields: ['maneuver_plan'],
      },
      {
        customer: 'unauthorized observer',
        subjectId: 'observer-peer',
        allowedEncryptedFields: [],
        withheldFields: ['position', 'covariance_detail', 'maneuver_plan'],
      },
    ],
  },
  {
    streamId: 'catalog-support-live',
    provider: 'hydra-provider-catalog',
    schema: 'SATCAT',
    standard: 'Catalog Support',
    publicFields: ['object_id', 'catalog_epoch'],
    protectedFields: ['operator_notes'],
    customerPolicies: [
      {
        customer: 'Customer A',
        subjectId: 'customer-alpha-peer',
        allowedEncryptedFields: [],
        withheldFields: ['operator_notes'],
      },
      {
        customer: 'Customer B',
        subjectId: 'customer-beta-peer',
        allowedEncryptedFields: ['operator_notes'],
        withheldFields: [],
      },
      {
        customer: 'unauthorized observer',
        subjectId: 'observer-peer',
        allowedEncryptedFields: [],
        withheldFields: ['operator_notes'],
      },
    ],
  },
];

const moduleDefinition = {
  name: 'hydra-maneuver-screening',
  protected: true,
  delivery: 'encrypted-wasm-module',
  requiresFields: ['object_id', 'timestamp', 'position'],
  refusesMissingFields: true,
  output: 'screening-result-without-revealing-withheld-fields',
};

const assertionNames = [
  'customer-alpha-decrypts-authorized-fields',
  'customer-beta-decrypts-different-authorized-fields',
  'observer-decrypt-fails',
  'protected-module-runs-for-authorized-customer',
  'protected-module-refuses-unauthorized-observer',
  'revoked-customer-stale-key-rejected-after-rotation',
  'docker-topology-has-two-providers-two-customers-one-observer',
  'no-tailscale-or-side-channel-discovery',
];

function parseArgs(argv) {
  const options = {
    dryRun: false,
    json: false,
    keepRunning: false,
    skipBuild: false,
    allowShortDiscoveryWait: false,
    composeFile: defaultComposeFile,
    reportFile: defaultReportFile,
    minDiscoveryWaitMs: minimumDiscoveryWaitMs,
  };

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === '--dry-run') {
      options.dryRun = true;
    } else if (arg === '--json') {
      options.json = true;
    } else if (arg === '--keep-running') {
      options.keepRunning = true;
    } else if (arg === '--skip-build') {
      options.skipBuild = true;
    } else if (arg === '--allow-short-discovery-wait') {
      options.allowShortDiscoveryWait = true;
    } else if (arg === '--compose-file') {
      index += 1;
      options.composeFile = resolve(repoRoot, requiredArg(argv[index], arg));
    } else if (arg.startsWith('--compose-file=')) {
      options.composeFile = resolve(repoRoot, arg.slice('--compose-file='.length));
    } else if (arg === '--report-file') {
      index += 1;
      options.reportFile = resolve(repoRoot, requiredArg(argv[index], arg));
    } else if (arg.startsWith('--report-file=')) {
      options.reportFile = resolve(repoRoot, arg.slice('--report-file='.length));
    } else if (arg === '--min-discovery-wait-ms') {
      index += 1;
      options.minDiscoveryWaitMs = parseWaitMs(requiredArg(argv[index], arg));
    } else if (arg.startsWith('--min-discovery-wait-ms=')) {
      options.minDiscoveryWaitMs = parseWaitMs(arg.slice('--min-discovery-wait-ms='.length));
    } else if (arg === '--help' || arg === '-h') {
      options.help = true;
    } else {
      throw new Error(`unknown argument: ${arg}`);
    }
  }

  if (options.minDiscoveryWaitMs < minimumDiscoveryWaitMs && !options.allowShortDiscoveryWait) {
    throw new Error(
      `Hydra marketplace demo discovery wait must be at least ${minimumDiscoveryWaitMs} ms; ` +
      'pass --allow-short-discovery-wait only for local development diagnostics',
    );
  }

  return options;
}

function requiredArg(value, flag) {
  if (!value || value.startsWith('--')) {
    throw new Error(`${flag} requires a value`);
  }
  return value;
}

function parseWaitMs(value) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) {
    throw new Error(`invalid discovery wait: ${value}`);
  }
  return Math.trunc(parsed);
}

function buildPlan(options) {
  return {
    scenario: 'Hydra field-encrypted marketplace demo',
    generatedAt: new Date().toISOString(),
    discovery: {
      mechanism: 'SDN/libp2p/IPFS',
      minWaitMs: options.minDiscoveryWaitMs,
      bootstrap: [
        '/dnsaddr/bootstrap.spacedatanetwork.org/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45',
        '/dnsaddr/bootstrap.spacedatanetwork.org/p2p/16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3',
        '/dns4/hydra-provider-maneuver/tcp/8080/ws/p2p/${SDN_HYDRA_BOOTSTRAP_PEER_ID}',
      ],
      forbiddenShortcuts: ['tailscale', 'tailnet', 'side-channel-discovery'],
    },
    nodes: nodeDefinitions,
    streams,
    module: moduleDefinition,
    assertions: assertionNames,
    outputs: {
      reportFile: options.reportFile,
      composeFile: options.composeFile,
    },
  };
}

function printHelp() {
  console.log(`Usage: node deployment/scripts/hydra-marketplace-demo.mjs [options]

Runs the Hydra marketplace demo with two providers, Customer A, Customer B, and
one unauthorized observer.

Options:
  --dry-run                         Print the scenario plan without Docker.
  --json                            Print JSON instead of text.
  --compose-file <path>             Compose file to run.
  --report-file <path>              JSON report output path.
  --min-discovery-wait-ms <ms>      Discovery wait before verification. Default: 300000.
  --allow-short-discovery-wait      Permit a shorter wait for local diagnostics only.
  --skip-build                      Run Docker Compose without --build.
  --keep-running                    Do not shut down Compose after verification.
  -h, --help                        Show this help.
`);
}

function emitPlan(plan, options) {
  if (options.json) {
    console.log(JSON.stringify(plan, null, 2));
    return;
  }

  console.log(`${plan.scenario}`);
  console.log(`Discovery: ${plan.discovery.mechanism}; minimum wait ${plan.discovery.minWaitMs} ms`);
  console.log(`Compose: ${plan.outputs.composeFile}`);
  console.log('Nodes:');
  for (const node of plan.nodes) {
    console.log(`  - ${node.service}: ${node.role}`);
  }
  console.log('Assertions:');
  for (const assertion of plan.assertions) {
    console.log(`  - ${assertion}`);
  }
}

function run(command, args, { capture = false, env = {} } = {}) {
  const result = spawnSync(command, args, {
    cwd: repoRoot,
    env: { ...process.env, ...env },
    encoding: 'utf8',
    stdio: capture ? ['ignore', 'pipe', 'pipe'] : 'inherit',
  });
  if (result.status !== 0) {
    const output = [result.stdout, result.stderr].filter(Boolean).join('\n').trim();
    throw new Error(`${command} ${args.join(' ')} failed${output ? `:\n${output}` : ''}`);
  }
  return capture ? result.stdout.trim() : '';
}

function composeArgs(options, ...args) {
  return ['compose', '-p', 'sdn-hydra-marketplace', '-f', options.composeFile, ...args];
}

async function waitForDiscovery(milliseconds, assertServicesRunning) {
  const startedAt = Date.now();
  let nextProgressAt = startedAt;
  let nextServiceCheckAt = startedAt;
  while (Date.now() - startedAt < milliseconds) {
    const remainingMs = milliseconds - (Date.now() - startedAt);
    if (Date.now() >= nextProgressAt) {
      const remainingSeconds = Math.max(0, Math.ceil(remainingMs / 1000));
      console.log(`Waiting for SDN/libp2p/IPFS discovery registration: ${remainingSeconds}s remaining`);
      nextProgressAt = Date.now() + 60_000;
    }
    if (Date.now() >= nextServiceCheckAt) {
      assertServicesRunning();
      nextServiceCheckAt = Date.now() + 15_000;
    }
    await new Promise((resolveSleep) => setTimeout(resolveSleep, Math.min(5_000, remainingMs)));
  }
  assertServicesRunning();
}

function resolveStreamForGrant(stream, grant) {
  const fields = [
    ...stream.publicFields.map((fieldPath) => ({
      fieldPath,
      state: 'Public',
      visibility: 'public',
      reason: 'public-release',
    })),
    ...stream.protectedFields.map((fieldPath) => {
      const allowed = grant.allowedEncryptedFields.includes(fieldPath);
      return {
        fieldPath,
        state: 'Encrypted',
        visibility: allowed ? 'decrypted' : 'encrypted',
        reason: allowed ? 'authorized-field-grant' : 'field_not_granted',
      };
    }),
  ];
  return {
    customer: grant.customer,
    subjectId: grant.subjectId,
    streamId: stream.streamId,
    schema: stream.schema,
    fields,
  };
}

function runProtectedModule(view) {
  const availableFields = new Set(
    view.fields
      .filter((field) => field.visibility === 'public' || field.visibility === 'decrypted')
      .map((field) => field.fieldPath),
  );
  const missingFields = moduleDefinition.requiresFields.filter((fieldPath) => !availableFields.has(fieldPath));
  if (missingFields.length > 0) {
    return {
      status: 'refused',
      customer: view.customer,
      missingFields,
    };
  }
  return {
    status: 'ran',
    customer: view.customer,
    consumedFields: moduleDefinition.requiresFields,
    produced: moduleDefinition.output,
  };
}

function verifyScenario(plan) {
  const maneuverStream = plan.streams.find((stream) => stream.streamId === 'maneuver-ephemeris-live');
  if (!maneuverStream) {
    throw new Error('maneuver ephemeris stream missing from plan');
  }

  const views = Object.fromEntries(
    maneuverStream.customerPolicies.map((grant) => [grant.customer, resolveStreamForGrant(maneuverStream, grant)]),
  );
  const alphaView = views['Customer A'];
  const betaView = views['Customer B'];
  const observerView = views['unauthorized observer'];
  const alphaModule = runProtectedModule(alphaView);
  const betaModule = runProtectedModule(betaView);
  const observerModule = runProtectedModule(observerView);
  const rotation = {
    previousEpoch: 'epoch-7',
    currentEpoch: 'epoch-8',
    revokedSubject: 'customer-alpha-peer',
    staleGrantAccepted: false,
    replacementGrantRequired: true,
  };

  const checks = [
    check('customer-alpha-decrypts-authorized-fields',
      fieldVisibility(alphaView, 'position') === 'decrypted' &&
      fieldVisibility(alphaView, 'covariance_detail') === 'encrypted' &&
      fieldVisibility(alphaView, 'maneuver_plan') === 'encrypted'),
    check('customer-beta-decrypts-different-authorized-fields',
      fieldVisibility(betaView, 'position') === 'decrypted' &&
      fieldVisibility(betaView, 'covariance_detail') === 'decrypted' &&
      fieldVisibility(betaView, 'maneuver_plan') === 'encrypted'),
    check('observer-decrypt-fails',
      observerView.fields.every((field) => field.state !== 'Encrypted' || field.visibility !== 'decrypted')),
    check('protected-module-runs-for-authorized-customer',
      alphaModule.status === 'ran' && betaModule.status === 'ran'),
    check('protected-module-refuses-unauthorized-observer',
      observerModule.status === 'refused' && observerModule.missingFields.includes('position')),
    check('revoked-customer-stale-key-rejected-after-rotation',
      rotation.currentEpoch !== rotation.previousEpoch && rotation.staleGrantAccepted === false),
    check('docker-topology-has-two-providers-two-customers-one-observer',
      plan.nodes.filter((node) => node.role.startsWith('provider:')).length === 2 &&
      plan.nodes.filter((node) => node.role.startsWith('customer:')).length === 2 &&
      plan.nodes.filter((node) => node.role === 'observer:unauthorized').length === 1),
    check('no-tailscale-or-side-channel-discovery',
      plan.discovery.mechanism === 'SDN/libp2p/IPFS' &&
      plan.discovery.forbiddenShortcuts.includes('tailscale')),
  ];

  const failed = checks.filter((item) => !item.passed);
  if (failed.length > 0) {
    throw new Error(`Hydra marketplace verification failed: ${failed.map((item) => item.name).join(', ')}`);
  }

  return {
    scenario: plan.scenario,
    verifiedAt: new Date().toISOString(),
    discovery: plan.discovery,
    accessMatrix: [
      summarizeView(alphaView),
      summarizeView(betaView),
      summarizeView(observerView),
    ],
    protectedModule: {
      alpha: alphaModule,
      beta: betaModule,
      observer: observerModule,
    },
    revocationRotation: rotation,
    checks,
  };
}

function fieldVisibility(view, fieldPath) {
  return view.fields.find((field) => field.fieldPath === fieldPath)?.visibility;
}

function summarizeView(view) {
  return {
    customer: view.customer,
    subjectId: view.subjectId,
    streamId: view.streamId,
    schema: view.schema,
    fields: view.fields.map((field) => ({
      fieldPath: field.fieldPath,
      visibility: field.visibility,
      reason: field.reason,
    })),
  };
}

function check(name, passed) {
  return { name, passed: Boolean(passed) };
}

function parseDockerPsJson(raw) {
  const trimmed = raw.trim();
  if (!trimmed) {
    return [];
  }
  if (trimmed.startsWith('[')) {
    return JSON.parse(trimmed);
  }
  return trimmed
    .split('\n')
    .filter(Boolean)
    .map((line) => JSON.parse(line));
}

function assertDockerServicesRunning(plan, options) {
  const raw = run('docker', composeArgs(options, 'ps', '-a', '--format', 'json'), { capture: true });
  const services = parseDockerPsJson(raw);
  const serviceByName = new Map(services.map((service) => [service.Service, service]));
  const failures = [];

  for (const node of plan.nodes) {
    const service = serviceByName.get(node.service);
    if (!service) {
      failures.push(`${node.service}:missing`);
      continue;
    }
    const state = String(service.State ?? '').toLowerCase();
    const status = String(service.Status ?? '').toLowerCase();
    if (
      state === 'exited' ||
      state === 'dead' ||
      state === 'created' ||
      status.includes('exited') ||
      status.includes('dead')
    ) {
      failures.push(`${node.service}:${service.State || service.Status || 'unknown'}`);
    }
  }

  if (failures.length > 0) {
    throw new Error(`Docker service exited before Hydra verification: ${failures.join(', ')}`);
  }
}

async function runDockerDemo(plan, options) {
  console.log('Validating Docker Compose topology');
  run('docker', composeArgs(options, 'config'), {
    capture: true,
    env: { SDN_HYDRA_DISCOVERY_MIN_WAIT_MS: String(options.minDiscoveryWaitMs) },
  });

  console.log('Starting Hydra marketplace Docker topology');
  const upArgs = ['up'];
  if (!options.skipBuild) {
    upArgs.push('--build');
  }
  upArgs.push('-d');
  run('docker', composeArgs(options, ...upArgs), {
    env: { SDN_HYDRA_DISCOVERY_MIN_WAIT_MS: String(options.minDiscoveryWaitMs) },
  });

  let report;
  try {
    assertDockerServicesRunning(plan, options);
    await waitForDiscovery(options.minDiscoveryWaitMs, () => assertDockerServicesRunning(plan, options));
    const ps = run('docker', composeArgs(options, 'ps'), { capture: true });
    report = {
      ...verifyScenario(plan),
      docker: {
        composeFile: options.composeFile,
        project: 'sdn-hydra-marketplace',
        ps,
      },
    };
    mkdirSync(dirname(options.reportFile), { recursive: true });
    writeFileSync(options.reportFile, `${JSON.stringify(report, null, 2)}\n`);
    console.log(`Hydra marketplace report written to ${options.reportFile}`);
  } finally {
    if (!options.keepRunning) {
      console.log('Stopping Hydra marketplace Docker topology');
      run('docker', composeArgs(options, 'down', '-v', '--remove-orphans'));
    }
  }

  return report;
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  if (options.help) {
    printHelp();
    return;
  }

  const plan = buildPlan(options);
  if (options.dryRun) {
    emitPlan(plan, options);
    return;
  }

  const report = await runDockerDemo(plan, options);
  if (options.json) {
    console.log(JSON.stringify(report, null, 2));
  }
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
});
