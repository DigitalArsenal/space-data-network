#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync
} from 'node:fs';
import { basename, dirname, join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';

const modulePath = fileURLToPath(import.meta.url);
const repoRoot = resolve(dirname(modulePath), '../..');
const defaultPlatform = 'linux/amd64';
const dummyPinnedBootstrapPeer = '/ip4/127.0.0.1/tcp/9/p2p/16Uiu2HAmP8KTvYP2i7Ef2Lf7Vbn5beZf2aMTpq4pmQAK6SjRphYT';

function log(message) {
  console.log(`[artifact-docker] ${message}`);
}

function runCommand(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env,
    encoding: 'utf8',
    stdio: options.stdio === 'inherit' ? 'inherit' : ['ignore', 'pipe', 'pipe']
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

function runDocker(args, options = {}) {
  return runCommand('docker', args, options);
}

function walkFiles(root) {
  const out = [];
  const stack = [resolve(root)];

  while (stack.length > 0) {
    const current = stack.pop();
    if (!current || !existsSync(current)) {
      continue;
    }
    const currentStat = statSync(current);
    if (currentStat.isFile()) {
      out.push(current);
      continue;
    }
    if (!currentStat.isDirectory()) {
      continue;
    }
    for (const entry of readdirSync(current)) {
      stack.push(join(current, entry));
    }
  }

  return out.sort();
}

function pickArtifact(files, key, pattern) {
  const matches = files.filter((file) => pattern.test(basename(file)));
  if (matches.length === 0) {
    throw new Error(`Missing required release artifact: ${key}`);
  }
  if (matches.length > 1) {
    throw new Error(`Found multiple release artifacts for ${key}: ${matches.map((file) => basename(file)).join(', ')}`);
  }
  return {
    path: matches[0],
    name: basename(matches[0])
  };
}

export function discoverArtifacts(releaseRoot) {
  const files = walkFiles(releaseRoot);
  return {
    fullDeb: pickArtifact(files, 'fullDeb', /^spacedatanetwork-full_.*_amd64\.deb$/),
    edgeDeb: pickArtifact(files, 'edgeDeb', /^spacedatanetwork-edge_.*_amd64\.deb$/),
    fullRpm: pickArtifact(files, 'fullRpm', /^spacedatanetwork-full-.*\.x86_64\.rpm$/),
    edgeRpm: pickArtifact(files, 'edgeRpm', /^spacedatanetwork-edge-.*\.x86_64\.rpm$/),
    linuxVm: pickArtifact(files, 'linuxVm', /^spacedatanetwork-linux-vm-.*\.tar\.gz$/),
    linuxCli: pickArtifact(files, 'linuxCli', /^spacedatanetwork-(?!container-).*-linux-amd64\.tar\.gz$/),
    container: pickArtifact(files, 'container', /^spacedatanetwork-container-.*-linux-amd64\.tar\.gz$/),
    sdnJs: pickArtifact(files, 'sdnJs', /^spacedatanetwork-sdn-js-.*\.tgz$/),
    sbom: pickArtifact(files, 'sbom', /^spacedatanetwork-sbom\.cdx\.json$/),
    ipfsDeployment: pickArtifact(files, 'ipfsDeployment', /^ipfs-deployment\.json$/)
  };
}

export function parseDockerLoadImage(output) {
  const loadedImage = output.match(/Loaded image:\s*(\S+)/);
  if (loadedImage) {
    return loadedImage[1];
  }
  const loadedImageId = output.match(/Loaded image ID:\s*(\S+)/);
  if (loadedImageId) {
    return loadedImageId[1];
  }
  throw new Error(`Unable to determine loaded Docker image from docker load output: ${output}`);
}

export function generateInstallDockerfile({ artifactName, artifactType }) {
  switch (artifactType) {
    case 'full-deb':
      return `FROM debian:bookworm-slim
COPY ${artifactName} /tmp/${artifactName}
RUN apt-get update \\
  && apt-get install -y --no-install-recommends ca-certificates curl /tmp/${artifactName} \\
  && rm -rf /var/lib/apt/lists/*
ENV WASMEDGE_DIR=/opt/spacedatanetwork/.wasmedge
ENV LD_LIBRARY_PATH=/opt/spacedatanetwork/.wasmedge/lib
RUN test -x /opt/spacedatanetwork/bin/spacedatanetwork \\
  && test -d /opt/spacedatanetwork/.wasmedge/lib \\
  && WASMEDGE_DIR=/opt/spacedatanetwork/.wasmedge LD_LIBRARY_PATH=/opt/spacedatanetwork/.wasmedge/lib /opt/spacedatanetwork/bin/spacedatanetwork --help >/tmp/spacedatanetwork-help.txt
`;

    case 'edge-deb':
      return `FROM debian:bookworm-slim
COPY ${artifactName} /tmp/${artifactName}
RUN apt-get update \\
  && apt-get install -y --no-install-recommends ca-certificates curl /tmp/${artifactName} \\
  && rm -rf /var/lib/apt/lists/*
RUN test -x /opt/spacedatanetwork/bin/spacedatanetwork-edge \\
  && /opt/spacedatanetwork/bin/spacedatanetwork-edge --help >/tmp/spacedatanetwork-edge-help.txt
`;

    case 'full-rpm':
      return `FROM rockylinux:9
COPY ${artifactName} /tmp/${artifactName}
RUN dnf install -y /tmp/${artifactName} \\
  && dnf clean all \\
  && command -v curl
ENV WASMEDGE_DIR=/opt/spacedatanetwork/.wasmedge
ENV LD_LIBRARY_PATH=/opt/spacedatanetwork/.wasmedge/lib
RUN test -x /opt/spacedatanetwork/bin/spacedatanetwork \\
  && test -d /opt/spacedatanetwork/.wasmedge/lib \\
  && WASMEDGE_DIR=/opt/spacedatanetwork/.wasmedge LD_LIBRARY_PATH=/opt/spacedatanetwork/.wasmedge/lib /opt/spacedatanetwork/bin/spacedatanetwork --help >/tmp/spacedatanetwork-help.txt
`;

    case 'edge-rpm':
      return `FROM rockylinux:9
COPY ${artifactName} /tmp/${artifactName}
RUN dnf install -y /tmp/${artifactName} \\
  && dnf clean all \\
  && command -v curl
RUN test -x /opt/spacedatanetwork/bin/spacedatanetwork-edge \\
  && /opt/spacedatanetwork/bin/spacedatanetwork-edge --help >/tmp/spacedatanetwork-edge-help.txt
`;

    case 'linux-vm':
      return `FROM debian:bookworm-slim
COPY ${artifactName} /tmp/${artifactName}
RUN apt-get update \\
  && apt-get install -y --no-install-recommends ca-certificates curl tar \\
  && tar -C / -xzf /tmp/${artifactName} \\
  && rm -rf /var/lib/apt/lists/*
ENV WASMEDGE_DIR=/opt/spacedatanetwork/.wasmedge
ENV LD_LIBRARY_PATH=/opt/spacedatanetwork/.wasmedge/lib
RUN test -x /opt/spacedatanetwork/bin/spacedatanetwork \\
  && test -d /opt/spacedatanetwork/.wasmedge/lib \\
  && WASMEDGE_DIR=/opt/spacedatanetwork/.wasmedge LD_LIBRARY_PATH=/opt/spacedatanetwork/.wasmedge/lib /opt/spacedatanetwork/bin/spacedatanetwork --help >/tmp/spacedatanetwork-help.txt
`;

    case 'linux-cli': {
      const bundleRoot = artifactName.replace(/\.tar\.gz$/, '');
      return `FROM debian:bookworm-slim
COPY ${artifactName} /tmp/${artifactName}
RUN apt-get update \\
  && apt-get install -y --no-install-recommends ca-certificates curl tar \\
  && mkdir -p /opt \\
  && tar -C /opt -xzf /tmp/${artifactName} \\
  && rm -rf /var/lib/apt/lists/*
ENV WASMEDGE_DIR=/opt/${bundleRoot}/runtime/wasmedge
ENV LD_LIBRARY_PATH=/opt/${bundleRoot}/runtime/wasmedge/lib
RUN test -x /opt/${bundleRoot}/bin/spacedatanetwork \\
  && test -x /opt/${bundleRoot}/bin/sdn \\
  && test -x /opt/${bundleRoot}/runtime/kubo/ipfs \\
  && test -f /opt/${bundleRoot}/runtime/modules/org.spacedatanetwork.updater.wasm \\
  && test -d /opt/${bundleRoot}/runtime/ui/sdn \\
  && test -d /opt/${bundleRoot}/runtime/ui/webui \\
  && /opt/${bundleRoot}/bin/spacedatanetwork --help >/tmp/spacedatanetwork-help.txt \\
  && /opt/${bundleRoot}/bin/sdn --help >/tmp/sdn-help.txt
`;
    }

    case 'sdn-js':
      return `FROM node:24-bookworm-slim
WORKDIR /app
COPY ${artifactName} /tmp/${artifactName}
RUN npm init -y >/dev/null 2>&1 \\
  && npm install --no-audit --no-fund /tmp/${artifactName}
RUN node --input-type=module -e "const root = await import('@spacedatanetwork/sdn-js'); const ui = await import('@spacedatanetwork/sdn-js/ui'); const storefront = await import('@spacedatanetwork/sdn-js/storefront'); if (typeof root.SDNNode?.create !== 'function') throw new Error('sdn-js root export missing SDNNode.create'); if (typeof ui.mountWalletUI !== 'function') throw new Error('sdn-js UI export missing mountWalletUI'); if (typeof storefront.createStorefrontClient !== 'function') throw new Error('sdn-js storefront export missing createStorefrontClient'); console.log('sdn-js published imports ok')"
`;

    default:
      throw new Error(`Unsupported artifact type: ${artifactType}`);
  }
}

export function generateFullNodeConfig({ bootstrapPeers = [] } = {}) {
  const bootstrapYaml = bootstrapPeers.length > 0
    ? bootstrapPeers.map((peer) => `    - ${peer}`).join('\n')
    : '    []';

  return `mode: full

network:
  listen:
    - /ip4/0.0.0.0/tcp/4001
    - /ip4/0.0.0.0/tcp/8080/ws
  bootstrap:
${bootstrapYaml}
  max_connections: 1000
  enable_relay: true

storage:
  path: /var/lib/spacedatanetwork/data
  max_size: 10GB
  gc_interval: 1h

admin:
  enabled: true
  listen_addr: 0.0.0.0:5001
  require_auth: false
  session_expiry: 24h
  totp_required: false
  tls_enabled: false
  frontend_path: /opt/spacedatanetwork/admin-ui
  admin_ui_path: /opt/spacedatanetwork/admin-ui
  webui_path: /opt/spacedatanetwork/webui

peers:
  registry_path: /var/lib/spacedatanetwork/data/peers.db
  strict_mode: false
  enable_dht: true
  enable_mdns: false

tor:
  enabled: false
  hidden_service_enabled: false

flows:
  enabled: false
  storage_path: /var/lib/spacedatanetwork/data/flows

setup:
  token_expiry: 10m
  data_path: /var/lib/spacedatanetwork/data
`;
}

export function generateContainerNodeConfig({ bootstrapPeers = [] } = {}) {
  return generateFullNodeConfig({ bootstrapPeers })
    .replaceAll('/var/lib/spacedatanetwork/data', '/app/data')
    .replaceAll('/opt/spacedatanetwork/admin-ui', '/app/admin-ui')
    .replaceAll('/opt/spacedatanetwork/webui', '/app/webui');
}

export function generateEdgeArgs({ bootstrapPeer, healthPort }) {
  return ['--bootstrap', bootstrapPeer, '--health-port', String(healthPort)];
}

function parseArgs(argv) {
  const options = {
    platform: defaultPlatform,
    keep: false,
    skipNetwork: false,
    timeoutMs: 120_000
  };

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    switch (arg) {
      case '--release-dir':
        options.releaseDir = resolve(argv[++index] ?? '');
        break;
      case '--act-artifacts':
        options.actArtifacts = resolve(argv[++index] ?? '');
        break;
      case '--work-dir':
        options.workDir = resolve(argv[++index] ?? '');
        break;
      case '--platform':
        options.platform = argv[++index] ?? defaultPlatform;
        break;
      case '--timeout-ms':
        options.timeoutMs = Number(argv[++index] ?? options.timeoutMs);
        break;
      case '--keep':
        options.keep = true;
        break;
      case '--skip-network':
        options.skipNetwork = true;
        break;
      case '-h':
      case '--help':
        options.help = true;
        break;
      default:
        throw new Error(`Unknown argument: ${arg}`);
    }
  }

  if (!Number.isFinite(options.timeoutMs) || options.timeoutMs <= 0) {
    throw new Error('--timeout-ms must be a positive number');
  }

  return options;
}

function usage() {
  return `Usage: node deployment/release/test-release-artifacts-docker.mjs (--release-dir DIR | --act-artifacts DIR) [options]

Options:
  --release-dir DIR      Directory containing assembled release artifacts.
  --act-artifacts DIR    act/GitHub artifact server directory containing artifact zip files.
  --work-dir DIR         Temporary work directory. Defaults to an OS temp directory.
  --platform PLATFORM    Docker platform for native artifacts. Defaults to ${defaultPlatform}.
  --timeout-ms MS        Per-service startup timeout. Defaults to 120000.
  --skip-network         Only install/import artifacts; do not launch the SDN network.
  --keep                 Keep Docker images, containers, networks, and work files for debugging.
`;
}

function prepareArtifactRoot(options) {
  const workDir = options.workDir || mkdtempSync(join(tmpdir(), 'sdn-release-artifacts-docker-'));
  mkdirSync(workDir, { recursive: true });

  if (options.releaseDir) {
    return {
      artifactRoot: options.releaseDir,
      workDir
    };
  }

  if (!options.actArtifacts) {
    throw new Error('Either --release-dir or --act-artifacts is required');
  }

  const archives = walkFiles(options.actArtifacts).filter((file) => basename(file).endsWith('.zip'));
  if (archives.length === 0) {
    throw new Error(`No artifact zip files found under ${options.actArtifacts}`);
  }

  const extractedDir = join(workDir, 'extracted');
  mkdirSync(extractedDir, { recursive: true });
  for (const archive of archives) {
    log(`extracting ${archive}`);
    runCommand('unzip', ['-q', '-o', archive, '-d', extractedDir]);
  }

  return {
    artifactRoot: extractedDir,
    workDir
  };
}

function validateMetadataArtifacts(artifacts) {
  const sbom = JSON.parse(readFileSync(artifacts.sbom.path, 'utf8'));
  if (sbom.bomFormat !== 'CycloneDX') {
    throw new Error(`Expected CycloneDX SBOM, found ${sbom.bomFormat ?? '<missing>'}`);
  }

  const ipfsDeployment = JSON.parse(readFileSync(artifacts.ipfsDeployment.path, 'utf8'));
  if (typeof ipfsDeployment !== 'object' || ipfsDeployment === null || Array.isArray(ipfsDeployment)) {
    throw new Error('ipfs-deployment.json must contain a JSON object');
  }
}

function buildInstallImage({ artifact, artifactType, imageName, buildRoot, platform }) {
  const contextDir = join(buildRoot, artifactType);
  mkdirSync(contextDir, { recursive: true });
  copyFileSync(artifact.path, join(contextDir, artifact.name));
  writeFileSync(join(contextDir, 'Dockerfile'), generateInstallDockerfile({
    artifactName: artifact.name,
    artifactType
  }));

  log(`building ${imageName} from ${artifact.name}`);
  runDocker(['build', '--progress', 'plain', '--platform', platform, '-t', imageName, contextDir], { stdio: 'inherit' });
}

function buildImages({ artifacts, workDir, platform, prefix, keep }) {
  const buildRoot = join(workDir, 'docker-build');
  mkdirSync(buildRoot, { recursive: true });

  const imageSpecs = {
    fullDeb: { artifact: artifacts.fullDeb, artifactType: 'full-deb', imageName: `${prefix}-full-deb:latest` },
    edgeDeb: { artifact: artifacts.edgeDeb, artifactType: 'edge-deb', imageName: `${prefix}-edge-deb:latest` },
    fullRpm: { artifact: artifacts.fullRpm, artifactType: 'full-rpm', imageName: `${prefix}-full-rpm:latest` },
    edgeRpm: { artifact: artifacts.edgeRpm, artifactType: 'edge-rpm', imageName: `${prefix}-edge-rpm:latest` },
    linuxVm: { artifact: artifacts.linuxVm, artifactType: 'linux-vm', imageName: `${prefix}-linux-vm:latest` },
    linuxCli: { artifact: artifacts.linuxCli, artifactType: 'linux-cli', imageName: `${prefix}-linux-cli:latest` },
    sdnJs: { artifact: artifacts.sdnJs, artifactType: 'sdn-js', imageName: `${prefix}-sdn-js:latest` }
  };

  const builtImageSpecs = {};
  try {
    for (const [key, spec] of Object.entries(imageSpecs)) {
      buildInstallImage({
        artifact: spec.artifact,
        artifactType: spec.artifactType,
        imageName: spec.imageName,
        buildRoot,
        platform
      });
      builtImageSpecs[key] = spec;
    }
  } catch (error) {
    if (!keep) {
      cleanupDocker({ images: builtImageSpecs });
    }
    throw error;
  }

  return imageSpecs;
}

function loadContainerImage({ artifact, label, platform }) {
  log(`loading downloadable Docker image ${artifact.name}`);
  const loaded = runDocker(['load', '--input', artifact.path]);
  const imageName = parseDockerLoadImage(`${loaded.stdout}\n${loaded.stderr}`);
  log(`smoke-testing ${label} image ${imageName}`);
  runDocker(['run', '--rm', '--platform', platform, imageName, '--help'], { stdio: 'inherit' });
  return {
    imageName
  };
}

function loadContainerImages({ artifacts, platform }) {
  return {
    container: loadContainerImage({
      artifact: artifacts.container,
      label: 'node',
      platform
    })
  };
}

function writeFullConfig(workDir, name, bootstrapPeers) {
  const configPath = join(workDir, 'configs', `${name}.yaml`);
  mkdirSync(dirname(configPath), { recursive: true });
  writeFileSync(configPath, generateFullNodeConfig({ bootstrapPeers }));
  return configPath;
}

function writeContainerConfig(workDir, name, bootstrapPeers) {
  const configPath = join(workDir, 'configs', `${name}.yaml`);
  mkdirSync(dirname(configPath), { recursive: true });
  writeFileSync(configPath, generateContainerNodeConfig({ bootstrapPeers }));
  return configPath;
}

export function buildFullNodeRunArgs({
  containerName,
  imageName,
  configPath,
  networkName,
  platform,
  binaryPath = '/opt/spacedatanetwork/bin/spacedatanetwork',
  configTargetPath = '/etc/spacedatanetwork/config.yaml',
  entrypoint
}) {
  const args = [
    'run',
    '-d',
    '--platform',
    platform,
    '--name',
    containerName,
    '--network',
    networkName,
    '-v',
    `${configPath}:${configTargetPath}:ro`
  ];
  if (entrypoint) {
    args.push('--entrypoint', entrypoint);
  }
  args.push(
    imageName,
    ...(binaryPath === null ? [] : [binaryPath]),
    'daemon',
    '--config',
    configTargetPath
  );
  return args;
}

function startFullNode(options) {
  log(`starting ${options.containerName}`);
  runDocker(buildFullNodeRunArgs(options));
}

export function buildEdgeNodeRunArgs({
  containerName,
  imageName,
  bootstrapPeer,
  networkName,
  platform,
  binaryPath = '/opt/spacedatanetwork/bin/spacedatanetwork-edge',
  entrypoint
}) {
  const args = [
    'run',
    '-d',
    '--platform',
    platform,
    '--name',
    containerName,
    '--network',
    networkName,
  ];
  if (entrypoint) {
    args.push('--entrypoint', entrypoint);
  }
  args.push(
    imageName,
    ...(entrypoint ? [] : [binaryPath]),
    '--listen',
    '/ip4/0.0.0.0/tcp/8080/ws',
    ...generateEdgeArgs({ bootstrapPeer, healthPort: 8081 })
  );
  return args;
}

function startEdgeNode(options) {
  log(`starting ${options.containerName}`);
  runDocker(buildEdgeNodeRunArgs(options));
}

function sleep(ms) {
  return new Promise((resolveSleep) => {
    setTimeout(resolveSleep, ms);
  });
}

async function waitForJson({ containerName, url, label, predicate, timeoutMs }) {
  const deadline = Date.now() + timeoutMs;
  let last = '';

  while (Date.now() < deadline) {
    const response = runDocker(
      ['exec', containerName, 'curl', '-fsS', url],
      { allowFailure: true }
    );

    if (response.status === 0) {
      last = response.stdout.trim();
      try {
        const parsed = JSON.parse(last);
        if (predicate(parsed)) {
          return parsed;
        }
      } catch (error) {
        last = `${last}\nparse error: ${error instanceof Error ? error.message : String(error)}`;
      }
    } else {
      last = `${response.stderr}\n${response.stdout}`.trim();
    }

    await sleep(2000);
  }

  throw new Error(`Timed out waiting for ${label} at ${containerName} (${url}). Last response: ${last}`);
}

function dumpContainerLogs(containerNames) {
  for (const containerName of containerNames) {
    const logs = runDocker(['logs', '--tail', '160', containerName], { allowFailure: true });
    if (logs.stdout || logs.stderr) {
      console.error(`\n--- docker logs ${containerName} ---`);
      if (logs.stdout) {
        console.error(logs.stdout);
      }
      if (logs.stderr) {
        console.error(logs.stderr);
      }
    }
  }
}

async function runNetworkTest({ images, workDir, platform, prefix, timeoutMs }) {
  const networkName = `${prefix}-net`;
  const containers = [
    `${prefix}-full-deb`,
    `${prefix}-full-rpm`,
    `${prefix}-linux-vm`,
    `${prefix}-edge-deb`,
    `${prefix}-edge-rpm`,
    `${prefix}-container-full`,
    `${prefix}-container-edge`
  ];

  runDocker(['network', 'create', networkName]);

  try {
    const seedConfig = writeFullConfig(workDir, containers[0], [dummyPinnedBootstrapPeer]);
    startFullNode({
      containerName: containers[0],
      imageName: images.fullDeb.imageName,
      configPath: seedConfig,
      networkName,
      platform
    });

    await waitForJson({
      containerName: containers[0],
      url: 'http://127.0.0.1:5001/api/v1/data/health',
      label: 'full DEB health',
      timeoutMs,
      predicate: (body) => body.status === 'ok'
    });

    const seedStatus = await waitForJson({
      containerName: containers[0],
      url: 'http://127.0.0.1:5001/api/relay/status',
      label: 'full DEB relay status',
      timeoutMs,
      predicate: (body) => typeof body.peer_id === 'string' && body.peer_id.length > 0
    });

    const seedBootstrap = `/dns4/${containers[0]}/tcp/4001/p2p/${seedStatus.peer_id}`;
    const joinedConfig = writeFullConfig(workDir, containers[1], [seedBootstrap]);
    const vmConfig = writeFullConfig(workDir, containers[2], [seedBootstrap]);
    const containerConfig = writeContainerConfig(workDir, containers[5], [seedBootstrap]);

    startFullNode({
      containerName: containers[1],
      imageName: images.fullRpm.imageName,
      configPath: joinedConfig,
      networkName,
      platform
    });
    startFullNode({
      containerName: containers[2],
      imageName: images.linuxVm.imageName,
      configPath: vmConfig,
      networkName,
      platform
    });
    startEdgeNode({
      containerName: containers[3],
      imageName: images.edgeDeb.imageName,
      bootstrapPeer: seedBootstrap,
      networkName,
      platform
    });
    startEdgeNode({
      containerName: containers[4],
      imageName: images.edgeRpm.imageName,
      bootstrapPeer: seedBootstrap,
      networkName,
      platform
    });
    startFullNode({
      containerName: containers[5],
      imageName: images.container.imageName,
      configPath: containerConfig,
      networkName,
      platform,
      binaryPath: null,
      configTargetPath: '/app/config/full-docker.yaml'
    });
    startEdgeNode({
      containerName: containers[6],
      imageName: images.container.imageName,
      bootstrapPeer: seedBootstrap,
      networkName,
      platform,
      entrypoint: '/app/spacedatanetwork-edge'
    });

    await waitForJson({
      containerName: containers[1],
      url: 'http://127.0.0.1:5001/api/relay/status',
      label: 'full RPM peer connection',
      timeoutMs,
      predicate: (body) => Number(body.connections) >= 1
    });
    await waitForJson({
      containerName: containers[2],
      url: 'http://127.0.0.1:5001/api/relay/status',
      label: 'VM bundle peer connection',
      timeoutMs,
      predicate: (body) => Number(body.connections) >= 1
    });
    await waitForJson({
      containerName: containers[3],
      url: 'http://127.0.0.1:8081/health',
      label: 'edge DEB peer connection',
      timeoutMs,
      predicate: (body) => Number(body.peers) >= 1
    });
    await waitForJson({
      containerName: containers[4],
      url: 'http://127.0.0.1:8081/health',
      label: 'edge RPM peer connection',
      timeoutMs,
      predicate: (body) => Number(body.peers) >= 1
    });
    await waitForJson({
      containerName: containers[5],
      url: 'http://127.0.0.1:5001/api/relay/status',
      label: 'container full-node peer connection',
      timeoutMs,
      predicate: (body) => Number(body.connections) >= 1
    });
    await waitForJson({
      containerName: containers[6],
      url: 'http://127.0.0.1:8081/health',
      label: 'container edge-mode peer connection',
      timeoutMs,
      predicate: (body) => Number(body.peers) >= 1
    });
    await waitForJson({
      containerName: containers[0],
      url: 'http://127.0.0.1:5001/api/relay/status',
      label: 'seed node inbound peer connections',
      timeoutMs,
      predicate: (body) => Number(body.connections) >= 3
    });

    runDocker([
      'run',
      '--rm',
      '--platform',
      platform,
      '--network',
      networkName,
      images.fullDeb.imageName,
      'curl',
      '-fsS',
      `http://${containers[1]}:5001/api/v1/data/health`
    ], { stdio: 'inherit' });
    runDocker([
      'run',
      '--rm',
      '--platform',
      platform,
      '--network',
      networkName,
      images.fullDeb.imageName,
      'curl',
      '-fsS',
      `http://${containers[3]}:8081/health`
    ], { stdio: 'inherit' });
    runDocker([
      'run',
      '--rm',
      '--platform',
      platform,
      '--network',
      networkName,
      images.fullDeb.imageName,
      'curl',
      '-fsS',
      `http://${containers[5]}:5001/api/v1/data/health`
    ], { stdio: 'inherit' });
    runDocker([
      'run',
      '--rm',
      '--platform',
      platform,
      '--network',
      networkName,
      images.fullDeb.imageName,
      'curl',
      '-fsS',
      `http://${containers[6]}:8081/health`
    ], { stdio: 'inherit' });

    log('Docker SDN network check passed');
  } catch (error) {
    dumpContainerLogs(containers);
    throw error;
  }

  return {
    networkName,
    containers
  };
}

function cleanupDocker({ images, networkName, containers }) {
  for (const containerName of containers ?? []) {
    runDocker(['rm', '-f', containerName], { allowFailure: true });
  }
  if (networkName) {
    runDocker(['network', 'rm', networkName], { allowFailure: true });
  }
  for (const image of Object.values(images ?? {})) {
    runDocker(['rmi', '-f', image.imageName], { allowFailure: true });
  }
}

export async function runHarness(options) {
  const prefix = `sdn-artifact-${process.pid}-${Date.now()}`.toLowerCase();
  const prepared = prepareArtifactRoot(options);
  let images;
  let networkResult;

  try {
    log(`using artifact root ${prepared.artifactRoot}`);
    const artifacts = discoverArtifacts(prepared.artifactRoot);
    validateMetadataArtifacts(artifacts);

    images = buildImages({
      artifacts,
      workDir: prepared.workDir,
      platform: options.platform ?? defaultPlatform,
      prefix,
      keep: options.keep
    });
    images = {
      ...images,
      ...loadContainerImages({
        artifacts,
        platform: options.platform ?? defaultPlatform
      })
    };

    if (!options.skipNetwork) {
      networkResult = await runNetworkTest({
        images,
        workDir: prepared.workDir,
        platform: options.platform ?? defaultPlatform,
        prefix,
        timeoutMs: options.timeoutMs ?? 120_000
      });
    }

    log('release artifacts installed and verified in Docker');
  } finally {
    if (!options.keep) {
      cleanupDocker({
        images,
        networkName: networkResult?.networkName,
        containers: networkResult?.containers
      });
      if (!options.releaseDir && prepared.workDir) {
        rmSync(prepared.workDir, { recursive: true, force: true });
      }
    } else {
      log(`kept work directory ${prepared.workDir}`);
    }
  }
}

async function main(argv) {
  const options = parseArgs(argv);
  if (options.help) {
    console.log(usage());
    return;
  }
  await runHarness(options);
}

if (process.argv[1] === modulePath) {
  main(process.argv.slice(2)).catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    console.error(usage());
    process.exit(1);
  });
}
