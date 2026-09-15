import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
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
  writeFixture(releaseDir, 'spacedatanetwork-1.0.3-beta.1-linux-amd64.tar.gz');
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
  assert.equal(artifacts.linuxCli.name, 'spacedatanetwork-1.0.3-beta.1-linux-amd64.tar.gz');
  assert.equal(artifacts.container.name, 'spacedatanetwork-container-1.0.3~beta.1-linux-amd64.tar.gz');
  assert.equal(artifacts.sdnJs.name, 'spacedatanetwork-sdn-js-2.0.12.tgz');
  assert.equal(artifacts.sbom.name, 'spacedatanetwork-sbom.cdx.json');
  assert.equal(artifacts.ipfsDeployment.name, 'ipfs-deployment.json');
});

test('portable Linux CLI Dockerfile asserts bundle layout and alias', () => {
  const linuxCli = generateInstallDockerfile({
    artifactName: 'spacedatanetwork-1.0.3-beta.1-linux-amd64.tar.gz',
    artifactType: 'linux-cli'
  });

  assert.match(linuxCli, /tar -C \/opt -xzf/);
  assert.match(linuxCli, /\/opt\/spacedatanetwork-1\.0\.3-beta\.1-linux-amd64\/bin\/spacedatanetwork --help/);
  assert.match(linuxCli, /\/opt\/spacedatanetwork-1\.0\.3-beta\.1-linux-amd64\/bin\/sdn --help/);
  assert.match(linuxCli, /runtime\/kubo\/ipfs/);
  assert.match(linuxCli, /runtime\/modules\/hd-wallet-wasi\.wasm/);
  assert.match(linuxCli, /runtime\/modules\/org\.spacedatanetwork\.updater\.wasm/);
  assert.match(linuxCli, /runtime\/ui\/sdn/);
  assert.match(linuxCli, /runtime\/ui\/webui/);
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

test('full-node install Dockerfiles prove the installed binary needs no runtime', () => {
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

  // Flipped deliberately. These used to require a WasmEdge directory inside the
  // package and to set WASMEDGE_DIR/LD_LIBRARY_PATH before running the binary,
  // which proved only that a bundled runtime worked. The runtime is now linked
  // in, so the meaningful proof is the negative one: nothing is installed
  // beside the binary, it resolves no libwasmedge, and it still runs — inside a
  // stock base image that has no WasmEdge to fall back on.
  for (const [label, dockerfile] of [['full-deb', fullDeb], ['full-rpm', fullRpm]]) {
    assert.doesNotMatch(dockerfile, /WASMEDGE_DIR=/, `${label} must not point at a runtime`);
    assert.doesNotMatch(dockerfile, /LD_LIBRARY_PATH=/, `${label} must not widen the library path`);
    assert.match(dockerfile, /! test -e \/opt\/spacedatanetwork\/\.wasmedge/, label);
    assert.match(dockerfile, /! ldd \/opt\/spacedatanetwork\/bin\/spacedatanetwork \| grep -qi wasmedge/, label);
    assert.match(dockerfile, /\/opt\/spacedatanetwork\/bin\/spacedatanetwork --help/, label);
  }
  assert.doesNotMatch(edgeDeb, /WASMEDGE_DIR=\/opt\/spacedatanetwork\/\.wasmedge/);
  assert.match(edgeDeb, /\/opt\/spacedatanetwork\/bin\/spacedatanetwork-edge --help/);
  assert.match(fullRpm, /dnf install -y/);
  assert.doesNotMatch(fullRpm, /dnf install -y ca-certificates curl/);
  assert.match(fullRpm, /command -v curl/);
});

test('sdn-js install Dockerfile imports all published package subpaths on Node 24', () => {
  const sdnJs = generateInstallDockerfile({
    artifactName: 'spacedatanetwork-sdn-js-2.0.12.tgz',
    artifactType: 'sdn-js'
  });

  assert.match(sdnJs, /FROM node:24-bookworm-slim/);
  assert.doesNotMatch(sdnJs, /apt-get install .*git/);
  assert.match(sdnJs, /npm install --no-audit --no-fund \/tmp\/spacedatanetwork-sdn-js-2\.0\.12\.tgz/);
  assert.match(sdnJs, /import\('@spacedatanetwork\/sdn-js'\)/);
  assert.match(sdnJs, /import\('@spacedatanetwork\/sdn-js\/ui'\)/);
  assert.match(sdnJs, /import\('@spacedatanetwork\/sdn-js\/storefront'\)/);
});

test('containers are launched with a stable hostname', () => {
  // The at-rest key binds to (machine, user) and inside a container the machine
  // half is its hostname. Docker's default is a fresh container ID per run, so
  // a node started without --hostname refuses to seal an identity it could
  // never reopen. Every launch here must therefore name the container.
  const full = buildFullNodeRunArgs({
    containerName: 'sdn-full-deb',
    imageName: 'sdn-full-deb:test',
    configPath: '/tmp/full.yaml',
    networkName: 'sdn-test',
    platform: 'linux/amd64'
  });
  const edge = buildEdgeNodeRunArgs({
    containerName: 'sdn-edge-us',
    imageName: 'sdn-edge-deb:test',
    bootstrapPeer: '/dns4/sdn-full-deb/tcp/4001/p2p/12D3KooWSeed',
    networkName: 'sdn-test',
    platform: 'linux/amd64'
  });

  for (const [label, args] of [['full node', full], ['edge node', edge]]) {
    const at = args.indexOf('--hostname');
    assert(at !== -1, `${label} launch sets no --hostname`);
    assert.equal(args[at + 1], args[args.indexOf('--name') + 1], `${label} hostname must match its container name`);
  }
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

test('full-node package and VM bundle scripts stage nothing for a static runtime', () => {
  const packageScript = readFileSync(join(repoRoot, 'deployment/packaging/build-linux-packages.sh'), 'utf8');
  const vmScript = readFileSync(join(repoRoot, 'deployment/scripts/package-linux-vm-bundle.sh'), 'utf8');

  for (const [label, script] of [['package', packageScript], ['vm bundle', vmScript]]) {
    // Both still know how to stage a dynamic runtime — that path is what a
    // non-static prefix needs — but a static prefix must stage nothing, or the
    // artifact would carry a second, unused WasmEdge the daemon might prefer.
    // link.flags is the marker that says which kind of prefix is in use.
    assert.match(script, /copy_wasmedge_runtime/, label);
    assert.match(script, /link\.flags/, `${label} must detect a static prefix`);
    assert.match(script, /opt\/spacedatanetwork\/\.wasmedge/, label);
    // And neither may silently fall back to a runtime the builder happens to
    // have in its home directory; that default is what made the packages job
    // fail on every runner.
    assert.doesNotMatch(script, /\$\{HOME\}\/\.wasmedge/, `${label} must not default to ~/.wasmedge`);
  }
});

test('sdn-js declares no postinstall step it cannot run', () => {
  const packageJson = JSON.parse(readFileSync(join(repoRoot, 'sdn-js/package.json'), 'utf8'));

  // This used to require scripts/patch-hd-wallet-ui.mjs. That patch was deleted
  // with the public wallet client (1c6a79c9, 2026-07-22) and hd-wallet-ui 2.0.29
  // needs no patching, so the requirement outlived the thing it guarded. What
  // still matters is the failure it was written to catch: a declared lifecycle
  // script whose file is not in the package breaks `npm install` for everyone.
  const lifecycle = ['preinstall', 'install', 'postinstall', 'prepare'];
  for (const name of lifecycle) {
    const script = packageJson.scripts?.[name];
    if (!script) continue;
    const local = script.match(/node\s+(scripts\/[\w./-]+)/);
    if (!local) continue;
    assert(
      packageJson.files.includes(local[1]),
      `${name} runs ${local[1]}, which package.json "files" does not ship`,
    );
    assert(
      existsSync(join(repoRoot, 'sdn-js', local[1])),
      `${name} runs ${local[1]}, which is not in the tree`,
    );
  }
});

test('sdn-js package dependencies are installable without GitHub SSH access', () => {
  const packageJson = JSON.parse(readFileSync(join(repoRoot, 'sdn-js/package.json'), 'utf8'));
  const specs = Object.entries(packageJson.dependencies ?? {});
  const sshOnlySpecs = specs.filter(([, spec]) => /^(github:|git\+ssh:|ssh:)|git@github\.com/.test(spec));

  assert.deepEqual(sshOnlySpecs, []);
});
