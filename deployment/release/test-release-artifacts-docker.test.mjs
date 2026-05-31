import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  discoverArtifacts,
  generateInstallDockerfile,
  generateFullNodeConfig,
  generateEdgeArgs,
  parseDockerLoadImage,
  buildFullNodeRunArgs,
  buildEdgeNodeRunArgs
} from './test-release-artifacts-docker.mjs';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

function writeFixture(root, relativePath, contents = 'fixture') {
  const filePath = join(root, relativePath);
  mkdirSync(dirname(filePath), { recursive: true });
  writeFileSync(filePath, contents);
  return filePath;
}

test('discovers all release artifact types from a release directory', () => {
  const releaseDir = mkdtempSync(join(tmpdir(), 'sdn-release-artifacts-'));
  writeFixture(releaseDir, 'spacedatanetwork-full_1.0.3~beta.1_amd64.deb');
  writeFixture(releaseDir, 'spacedatanetwork-edge_1.0.3~beta.1_amd64.deb');
  writeFixture(releaseDir, 'spacedatanetwork-full-1.0.3~beta.1-1.x86_64.rpm');
  writeFixture(releaseDir, 'spacedatanetwork-edge-1.0.3~beta.1-1.x86_64.rpm');
  writeFixture(releaseDir, 'spacedatanetwork-linux-vm-1.0.3~beta.1.tar.gz');
  writeFixture(releaseDir, 'spacedatanetwork-container-1.0.3~beta.1-linux-amd64.tar.gz');
  writeFixture(releaseDir, 'spacedatanetwork-sdn-js-2.0.12.tgz');
  writeFixture(releaseDir, 'spacedatanetwork-sbom.cdx.json', '{"bomFormat":"CycloneDX"}');
  writeFixture(releaseDir, 'ipfs-deployment.json', '{"targets":[]}');

  const artifacts = discoverArtifacts(releaseDir);

  assert.equal(artifacts.fullDeb.name, 'spacedatanetwork-full_1.0.3~beta.1_amd64.deb');
  assert.equal(artifacts.edgeDeb.name, 'spacedatanetwork-edge_1.0.3~beta.1_amd64.deb');
  assert.equal(artifacts.fullRpm.name, 'spacedatanetwork-full-1.0.3~beta.1-1.x86_64.rpm');
  assert.equal(artifacts.edgeRpm.name, 'spacedatanetwork-edge-1.0.3~beta.1-1.x86_64.rpm');
  assert.equal(artifacts.linuxVm.name, 'spacedatanetwork-linux-vm-1.0.3~beta.1.tar.gz');
  assert.equal(artifacts.container.name, 'spacedatanetwork-container-1.0.3~beta.1-linux-amd64.tar.gz');
  assert.equal(artifacts.sdnJs.name, 'spacedatanetwork-sdn-js-2.0.12.tgz');
  assert.equal(artifacts.sbom.name, 'spacedatanetwork-sbom.cdx.json');
  assert.equal(artifacts.ipfsDeployment.name, 'ipfs-deployment.json');
});

test('parses docker load output for downloadable container image tars', () => {
  assert.equal(
    parseDockerLoadImage('Loaded image: dockerdigitalarsenal/space-data-network:v1.0.3-beta.1\n'),
    'dockerdigitalarsenal/space-data-network:v1.0.3-beta.1'
  );
  assert.equal(
    parseDockerLoadImage('Loaded image ID: sha256:abc123\n'),
    'sha256:abc123'
  );
});

test('full-node install Dockerfiles assert the packaged WasmEdge runtime is present', () => {
  const fullDeb = generateInstallDockerfile({
    artifactName: 'spacedatanetwork-full_1.0.3~beta.1_amd64.deb',
    artifactType: 'full-deb'
  });
  const edgeDeb = generateInstallDockerfile({
    artifactName: 'spacedatanetwork-edge_1.0.3~beta.1_amd64.deb',
    artifactType: 'edge-deb'
  });
  const fullRpm = generateInstallDockerfile({
    artifactName: 'spacedatanetwork-full-1.0.3~beta.1-1.x86_64.rpm',
    artifactType: 'full-rpm'
  });

  assert.match(fullDeb, /WASMEDGE_DIR=\/opt\/spacedatanetwork\/\.wasmedge/);
  assert.match(fullDeb, /test -d \/opt\/spacedatanetwork\/\.wasmedge\/lib/);
  assert.match(fullDeb, /\/opt\/spacedatanetwork\/bin\/spacedatanetwork --help/);
  assert.doesNotMatch(edgeDeb, /WASMEDGE_DIR=\/opt\/spacedatanetwork\/\.wasmedge/);
  assert.match(edgeDeb, /\/opt\/spacedatanetwork\/bin\/spacedatanetwork-edge --help/);
  assert.match(fullRpm, /dnf install -y/);
  assert.doesNotMatch(fullRpm, /dnf install -y ca-certificates curl/);
  assert.match(fullRpm, /command -v curl/);
  assert.match(fullRpm, /test -d \/opt\/spacedatanetwork\/\.wasmedge\/lib/);
});

test('sdn-js install Dockerfile imports all published package subpaths on Node 24', () => {
  const sdnJs = generateInstallDockerfile({
    artifactName: 'spacedatanetwork-sdn-js-2.0.12.tgz',
    artifactType: 'sdn-js'
  });

  assert.match(sdnJs, /FROM node:24-bookworm-slim/);
  assert.match(sdnJs, /npm install --no-audit --no-fund \/tmp\/spacedatanetwork-sdn-js-2\.0\.12\.tgz/);
  assert.match(sdnJs, /import\('@spacedatanetwork\/sdn-js'\)/);
  assert.match(sdnJs, /import\('@spacedatanetwork\/sdn-js\/ui'\)/);
  assert.match(sdnJs, /import\('@spacedatanetwork\/sdn-js\/storefront'\)/);
});

test('network configs bootstrap non-seed nodes to the seed peer', () => {
  const seedConfig = generateFullNodeConfig({ bootstrapPeers: [] });
  const joinedConfig = generateFullNodeConfig({
    bootstrapPeers: ['/dns4/sdn-full-deb/tcp/4001/p2p/12D3KooWSeed']
  });
  const edgeArgs = generateEdgeArgs({
    bootstrapPeer: '/dns4/sdn-full-deb/tcp/4001/p2p/12D3KooWSeed',
    healthPort: 8081
  });

  assert.match(seedConfig, /require_auth: false/);
  assert.match(seedConfig, /listen_addr: 0\.0\.0\.0:5001/);
  assert.match(joinedConfig, /- \/dns4\/sdn-full-deb\/tcp\/4001\/p2p\/12D3KooWSeed/);
  assert.deepEqual(edgeArgs, [
    '--bootstrap',
    '/dns4/sdn-full-deb/tcp/4001/p2p/12D3KooWSeed',
    '--health-port',
    '8081'
  ]);
});

test('container image run args respect the image entrypoint', () => {
  const packageFullArgs = buildFullNodeRunArgs({
    containerName: 'sdn-full-deb',
    imageName: 'sdn-artifact-full-deb:latest',
    configPath: '/tmp/full.yaml',
    networkName: 'sdn-net',
    platform: 'linux/amd64'
  });
  const containerFullArgs = buildFullNodeRunArgs({
    containerName: 'sdn-container-full',
    imageName: 'dockerdigitalarsenal/space-data-network:v1.0.3-beta.1',
    configPath: '/tmp/container.yaml',
    networkName: 'sdn-net',
    platform: 'linux/amd64',
    binaryPath: null,
    configTargetPath: '/app/config/full-docker.yaml'
  });
  const containerEdgeArgs = buildEdgeNodeRunArgs({
    containerName: 'sdn-container-edge',
    imageName: 'dockerdigitalarsenal/space-data-network:v1.0.3-beta.1',
    bootstrapPeer: '/dns4/sdn-full-deb/tcp/4001/p2p/12D3KooWSeed',
    networkName: 'sdn-net',
    platform: 'linux/amd64',
    entrypoint: '/app/spacedatanetwork-edge'
  });

  assert(packageFullArgs.includes('/opt/spacedatanetwork/bin/spacedatanetwork'));
  assert.deepEqual(containerFullArgs.slice(-3), ['daemon', '--config', '/app/config/full-docker.yaml']);
  assert(!containerFullArgs.includes('/app/spacedatanetwork'));
  assert(containerEdgeArgs.includes('--entrypoint'));
  assert.equal(containerEdgeArgs[containerEdgeArgs.indexOf('--entrypoint') + 1], '/app/spacedatanetwork-edge');
  assert(!containerEdgeArgs.slice(containerEdgeArgs.indexOf('dockerdigitalarsenal/space-data-network:v1.0.3-beta.1') + 1).includes('/app/spacedatanetwork-edge'));
});

test('full-node package and VM bundle scripts include the WasmEdge runtime', () => {
  const packageScript = readFileSync(join(repoRoot, 'deployment/packaging/build-linux-packages.sh'), 'utf8');
  const vmScript = readFileSync(join(repoRoot, 'deployment/scripts/package-linux-vm-bundle.sh'), 'utf8');

  assert.match(packageScript, /copy_wasmedge_runtime/);
  assert.match(packageScript, /full\/opt\/spacedatanetwork\/\.wasmedge/);
  assert.match(vmScript, /copy_wasmedge_runtime/);
  assert.match(vmScript, /opt\/spacedatanetwork\/\.wasmedge/);
});

test('sdn-js package includes the postinstall patch script it declares', () => {
  const packageJson = JSON.parse(readFileSync(join(repoRoot, 'sdn-js/package.json'), 'utf8'));

  assert.equal(packageJson.scripts.postinstall, 'node scripts/patch-hd-wallet-ui.mjs');
  assert(packageJson.files.includes('scripts/patch-hd-wallet-ui.mjs'));
});
