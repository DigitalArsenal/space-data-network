import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..');

function readRepoFile(relativePath) {
  return readFileSync(join(repoRoot, relativePath), 'utf8');
}

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
  assert.match(workflow, /--license-path "\$\{PWD\}\/LICENSE"/);
  assert.match(license, /MIT License/);
  assert.match(license, /Space Data Network/);
  assert.match(workflow, /spacedatanetwork-\$\{\{ needs\.beta-version\.outputs\.package_version \}\}-\$\{\{ matrix\.target_os \}\}-\$\{\{ matrix\.target_arch \}\}\.tar\.gz/);
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
