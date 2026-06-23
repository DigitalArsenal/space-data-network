import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const workflowPaths = [
  '.github/workflows/beta-release-artifacts.yml',
  '.github/workflows/ci.yml',
  '.github/workflows/docker-publish.yml',
  '.github/workflows/encryption-tests.yml',
  '.github/workflows/linux-vm-bundle.yml',
  '.github/workflows/npm-publish-sdn-js.yml',
  '.github/workflows/release-deploy.yml',
  '.github/workflows/security.yml',
];

function readRepoFile(relativePath) {
  return readFileSync(join(repoRoot, relativePath), 'utf8');
}

test('workflows opt into Node 24 for GitHub actions and project scripts', () => {
  for (const workflowPath of workflowPaths) {
    const workflow = readRepoFile(workflowPath);

    assert.match(
      workflow,
      /FORCE_JAVASCRIPT_ACTIONS_TO_NODE24:\s*true/,
      `${workflowPath} must force JavaScript actions onto the Node 24 runtime`,
    );
    assert.doesNotMatch(
      workflow,
      /node-version:\s*['"]?20['"]?/,
      `${workflowPath} must not run project scripts on Node 20`,
    );
  }
});

test('beta release workflow publishes public beta artifacts', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');
  const license = readRepoFile('LICENSE');

  assert.match(workflow, /name:\s*SDN Beta Release Artifacts/);
  assert.match(workflow, /workflow_dispatch:/);
  assert.match(workflow, /prepare-beta-release\.mjs/);
  assert.match(workflow, /assemble-beta-release-artifacts\.sh/);
  assert.match(workflow, /native_package_version/);
  assert.match(workflow, /npm pack --pack-destination/);
  assert.match(workflow, /artifact-docker-test:/);
  assert.match(workflow, /test:release-artifacts:docker/);
  assert.match(workflow, /container-image/);
  assert.match(workflow, /docker save/);
  assert.match(workflow, /spacedatanetwork-container-\$\{NATIVE_PACKAGE_VERSION\}-linux-amd64\.tar\.gz/);
  assert.match(workflow, /Build self-contained CLI archives/);
  assert.match(workflow, /build-self-contained-cli\.mjs/);
  assert.match(workflow, /--hd-wallet-wasm-path "\$\{PWD\}\/node_modules\/hd-wallet-wasm\/dist\/hd-wallet-wasi\.wasm"/);
  assert.match(workflow, /--license-path "\$\{PWD\}\/LICENSE"/);
  assert.match(license, /MIT License/);
  assert.match(license, /Space Data Network/);
  assert.match(workflow, /spacedatanetwork-\$\{\{ needs\.beta-version\.outputs\.package_version \}\}-\$\{\{ matrix\.target_os \}\}-\$\{\{ matrix\.target_arch \}\}\.\$\{\{ matrix\.archive_extension \}\}/);
  assert.match(workflow, /name:\s*cli-\$\{\{ matrix\.target_os \}\}-\$\{\{ matrix\.target_arch \}\}/);
  assert.match(workflow, /pattern:\s*cli-\*/);
  assert.match(workflow, /merge-multiple:\s*true/);
  assert.match(workflow, /prerelease:\s*false/);
  assert.match(workflow, /make_latest:\s*true/);
  assert.match(workflow, /release_name/);
  assert.match(workflow, /docker\.io/);
  assert.match(workflow, /dockerdigitalarsenal\/space-data-network/);
  assert.match(workflow, /beta/);
  assert.doesNotMatch(workflow, /Dockerfile\.full/);
  assert.doesNotMatch(workflow, /Dockerfile\.edge/);
  assert.doesNotMatch(workflow, /space-data-network-full/);
  assert.doesNotMatch(workflow, /space-data-network-edge/);
});

test('IPFS asset release script skips browser downloads and bounds dependency installs', () => {
  const script = readRepoFile('deployment/ipfs/ipfs-deploy.sh');

  assert.match(script, /PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD/);
  assert.match(script, /PUPPETEER_SKIP_DOWNLOAD/);
  assert.match(script, /CYPRESS_INSTALL_BINARY/);
  assert.match(script, /run_with_timeout/);
  assert.match(script, /WEBUI_NPM_CI_TIMEOUT_SECONDS/);
  assert.match(script, /npm ci --no-audit --fund=false/);
  assert.match(script, /log "Installing IPFS WebUI dependencies"/);
});

test('Docker release image copies local Go replacement modules before dependency download', () => {
  const dockerfile = readRepoFile('deployment/docker/Dockerfile');
  const goMod = readRepoFile('sdn-server/go.mod');

  const replacementModuleCopy = 'COPY sdn-server/third_party/spacedatastandards-go/go.mod ./sdn-server/third_party/spacedatastandards-go/go.mod';
  const goModDownload = 'RUN go mod download';

  assert.match(goMod, /replace github\.com\/DigitalArsenal\/spacedatastandards\.org\/lib\/go => \.\/third_party\/spacedatastandards-go/);
  assert.ok(
    dockerfile.indexOf(replacementModuleCopy) > -1,
    'Dockerfile must copy the local SDS replacement module go.mod before go mod download',
  );
  assert.ok(
    dockerfile.indexOf(replacementModuleCopy) < dockerfile.indexOf(goModDownload),
    'local replacement module go.mod must be available before go mod download runs',
  );
});

test('beta release workflow builds updater wasm once before platform CLI archives', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  assert.match(workflow, /updater-wasm:\s*\n\s*name:\s*Build updater module wasm/);
  assert.match(workflow, /name:\s*updater-module-wasm[\s\S]*path:\s*packages\/sdn-updater-module\/dist\/isomorphic\/module\.wasm/);
  assert.match(workflow, /needs:\s*\[beta-version, ipfs, updater-wasm\]/);
  assert.match(workflow, /name:\s*updater-module-wasm[\s\S]*path:\s*packages\/sdn-updater-module\/dist\/isomorphic/);
  assert.match(workflow, /name:\s*Verify updater module wasm[\s\S]*test -f packages\/sdn-updater-module\/dist\/isomorphic\/module\.wasm/);

  const cliJob = workflow.slice(workflow.indexOf('  cli:'), workflow.indexOf('  packages:'));
  assert.doesNotMatch(cliJob, /name:\s*Build updater module wasm/);
});

test('beta release workflow builds every required portable CLI target', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  for (const target of [
    { os: 'linux', arch: 'amd64', archive: 'tar.gz' },
    { os: 'linux', arch: 'arm64', archive: 'tar.gz' },
    { os: 'darwin', arch: 'arm64', archive: 'tar.gz' },
    { os: 'darwin', arch: 'amd64', archive: 'tar.gz' },
    { os: 'windows', arch: 'amd64', archive: 'zip' },
  ]) {
    assert.match(
      workflow,
      new RegExp(`target_os:\\s*${target.os}[\\s\\S]*target_arch:\\s*${target.arch}[\\s\\S]*archive_extension:\\s*${target.archive}`),
      `${target.os}-${target.arch} must declare ${target.archive} as its portable CLI archive extension`,
    );
  }
});

test('beta release workflow downloads Kubo archives with retries and validation', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');
  const downloaderPath = 'deployment/release/download-kubo.sh';

  assert.equal(existsSync(join(repoRoot, downloaderPath)), true);

  const downloader = readRepoFile(downloaderPath);

  assert.match(workflow, /deployment\/release\/download-kubo\.sh[\s\S]*--platform linux-amd64[\s\S]*--archive tar\.gz/);
  assert.match(workflow, /deployment\/release\/download-kubo\.sh[\s\S]*--platform "\$\{KUBO_PLATFORM\}"[\s\S]*--archive "\$\{KUBO_ARCHIVE\}"/);
  assert.doesNotMatch(workflow, /curl -L https:\/\/dist\.ipfs\.tech\/kubo\/\$\{KUBO_VERSION\}\/kubo_\$\{KUBO_VERSION\}_linux-amd64\.tar\.gz \| tar/);
  assert.doesNotMatch(workflow, /curl -L "https:\/\/dist\.ipfs\.tech\/kubo\/\$\{KUBO_VERSION\}\/kubo_\$\{KUBO_VERSION\}_\$\{KUBO_PLATFORM\}\.tar\.gz" \| tar/);
  assert.match(downloader, /curl -fL/);
  assert.match(downloader, /--retry-all-errors/);
  assert.match(downloader, /tar -tzf/);
  assert.match(downloader, /unzip -tq/);
});

test('beta release workflow builds the Windows CLI with the Windows WasmEdge runtime', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  assert.match(workflow, /target_os:\s*windows[\s\S]*runner:\s*windows-latest/);
  assert.match(workflow, /WasmEdge-\$\{WASMEDGE_VERSION\}-windows\.zip/);
  assert.match(workflow, /WasmEdge-\$\{WASMEDGE_VERSION\}-Windows/);
  assert.match(workflow, /--wasmedge-path "\$\{WASMEDGE_DIR\}"/);
  assert.match(workflow, /bin\/wasmedge\.dll/);
});

test('npm release publishing maps beta releases to the beta dist-tag', () => {
  const workflow = readRepoFile('.github/workflows/npm-publish-sdn-js.yml');

  assert.match(workflow, /beta\)/);
  assert.match(workflow, /echo "tag=beta"/);
});

test('release workflows install nFPM from a pinned Go module version', () => {
  for (const workflowPath of [
    '.github/workflows/beta-release-artifacts.yml',
    '.github/workflows/release-deploy.yml'
  ]) {
    const workflow = readRepoFile(workflowPath);

    assert.match(workflow, /NFPM_VERSION:\s*v\d+\.\d+\.\d+/);
    assert.match(workflow, /go install "github\.com\/goreleaser\/nfpm\/v2\/cmd\/nfpm@\$\{NFPM_VERSION\}"/);
    assert.match(workflow, /nfpm" --version/);
    assert.doesNotMatch(workflow, /goreleaser\/nfpm\/main\/www\/docs\/install\.sh/);
  }
});

test('push packaging workflows build IPFS WebUI before packaging full-node assets', () => {
  for (const workflowPath of [
    '.github/workflows/docker-publish.yml',
    '.github/workflows/linux-vm-bundle.yml'
  ]) {
    const workflow = readRepoFile(workflowPath);

    assert.match(workflow, /working-directory:\s*webui/, `${workflowPath} must install and build webui assets`);
    assert.match(workflow, /npm ci[\s\S]*npm run build/, `${workflowPath} must build webui/build before packaging`);
  }
});

test('container publish workflow ships one Docker Hub image', () => {
  const workflow = readRepoFile('.github/workflows/docker-publish.yml');

  assert.match(workflow, /REGISTRY:\s*docker\.io/);
  assert.match(workflow, /IMAGE_NAME:\s*dockerdigitalarsenal\/space-data-network/);
  assert.match(workflow, /username:\s*dockerdigitalarsenal/);
  assert.match(workflow, /secrets\.DOCKERHUB_TOKEN/);
  assert.match(workflow, /deployment\/docker\/Dockerfile/);
  assert.doesNotMatch(workflow, /matrix:/);
  assert.doesNotMatch(workflow, /Dockerfile\.full/);
  assert.doesNotMatch(workflow, /Dockerfile\.edge/);
  assert.doesNotMatch(workflow, /space-data-network-full/);
  assert.doesNotMatch(workflow, /space-data-network-edge/);
});

test('single Dockerfile defaults to full node and keeps edge mode as command override', () => {
  const dockerfile = readRepoFile('deployment/docker/Dockerfile');

  assert.match(dockerfile, /go build[\s\S]*-o \/out\/spacedatanetwork \.\/cmd\/spacedatanetwork/);
  assert.match(dockerfile, /go build -tags edge[\s\S]*-o \/out\/spacedatanetwork-edge \.\/cmd\/spacedatanetwork-edge/);
  assert.match(dockerfile, /ENTRYPOINT \["\/app\/spacedatanetwork"\]/);
  assert.match(dockerfile, /CMD \["daemon", "--config", "\/app\/config\/full-docker\.yaml"\]/);
});
