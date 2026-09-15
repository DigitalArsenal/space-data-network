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

function extractShellFunction(script, functionName) {
  const escapedName = functionName.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const header = new RegExp(`^${escapedName}\\(\\) \\{[ \\t]*$`, 'm').exec(script);

  assert.ok(header, `${functionName} must be defined as a shell function`);

  const bodyStart = header.index + header[0].length;
  const remainder = script.slice(bodyStart);
  const nextFunction = /^[_a-zA-Z][_a-zA-Z0-9]*\(\) \{[ \t]*$/m.exec(remainder);

  return remainder.slice(0, nextFunction?.index ?? remainder.length);
}

function extractGoTestArguments(functionBody, functionName) {
  const invocations = [...functionBody.matchAll(
    /^[ \t]*"\$ROOT\/scripts\/go-with-wasmedge\.sh"[ \t]+test((?:[ \t]+\S+)+)[ \t]*$/gm,
  )];

  assert.equal(invocations.length, 1, `${functionName} must contain exactly one active Go test invocation`);
  return invocations[0][1].trim().split(/\s+/);
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

test('CI quick checks share an explicit step-local WasmEdge directory', () => {
  const workflow = readRepoFile('.github/workflows/ci.yml');
  const stepHeader = '      - name: Run CI checks (same as pre-push)';
  const stepStarts = [...workflow.matchAll(/^      - name: Run CI checks \(same as pre-push\)$/gm)];

  assert.equal(stepStarts.length, 1, 'CI workflow must define exactly one local-equivalent checks step');

  const stepStart = stepStarts[0].index;
  const nextStepStart = workflow.indexOf('\n      - ', stepStart + stepHeader.length);
  const ciStep = workflow.slice(stepStart, nextStepStart === -1 ? workflow.length : nextStepStart);

  assert.match(
    ciStep,
    /^        env:\n          WASMEDGE_DIR: \$\{\{ runner\.temp \}\}\/\.wasmedge[ \t]*$/m,
    'Run CI step must explicitly pass runner.temp/.wasmedge; installer defaults are shell-local',
  );

  const installCommand = /^          \.\/scripts\/install-wasmedge\.sh[ \t]*$/m.exec(ciStep);
  const quickCheckCommand = /^          \.\/scripts\/ci-local\.sh quick[ \t]*$/m.exec(ciStep);

  assert.ok(installCommand, 'Run CI step must install WasmEdge with an active shell command');
  assert.ok(quickCheckCommand, 'Run CI step must run the quick local checks with an active shell command');
  assert.ok(
    installCommand.index < quickCheckCommand.index,
    'Run CI step must install WasmEdge before running quick checks',
  );
});

test('local Go suites serialize package builds while bypassing the test cache', () => {
  const script = readRepoFile('scripts/ci-local.sh');
  const goArgs = extractGoTestArguments(extractShellFunction(script, 'run_go'), 'run_go');
  const raceArgs = extractGoTestArguments(extractShellFunction(script, 'run_go_race'), 'run_go_race');

  for (const [functionName, args] of [['run_go', goArgs], ['run_go_race', raceArgs]]) {
    assert.ok(args.includes('-p=1'), `${functionName} must serialize package builds with -p=1`);
    assert.ok(args.includes('-count=1'), `${functionName} must bypass cached results`);
  }

  // run_go no longer runs ./... — the packages that time out a cold runner are
  // split into run_go_heavy at a 60-minute budget. That is only safe if the set
  // it excludes is genuinely run somewhere, so assert BOTH halves exist and
  // that nothing is quietly dropped between them.
  assert.ok(
    goArgs.at(-1) === '$pkgs',
    'run_go must run the filtered package list, not ./...',
  );
  assert.match(script, /pkgs=\$\([^)]*list \.\/\.\.\.[^)]*grep -Ev "\$\(heavy_pkg_filter\)"/);
  const heavyArgs = extractGoTestArguments(extractShellFunction(script, 'run_go_heavy'), 'run_go_heavy');
  assert.ok(heavyArgs.includes('-p=1'), 'run_go_heavy must serialize package builds');
  assert.ok(heavyArgs.includes('-count=1'), 'run_go_heavy must bypass cached results');
  assert.deepEqual(
    heavyArgs.filter((arg) => arg.startsWith('-timeout=')),
    ['-timeout=60m'],
    'run_go_heavy must carry the long budget the heavy packages were split out for',
  );
  assert.ok(
    heavyArgs.at(-1) === '$HEAVY_GO_PACKAGES',
    'run_go_heavy must run exactly the set heavy_pkg_filter excludes',
  );
  assert.deepEqual(raceArgs.at(-1), './...', 'run_go_race still covers the whole module');

  assert.deepEqual(
    goArgs.filter((arg) => arg.startsWith('-timeout=')),
    ['-timeout=20m'],
    'run_go must have exactly one explicit 20-minute timeout for cold WasmEdge runners',
  );
  assert.deepEqual(
    raceArgs.filter((arg) => arg.startsWith('-timeout=')),
    ['-timeout=30m'],
    'run_go_race must have exactly one explicit 30-minute timeout for serialized race builds',
  );
  assert.ok(raceArgs.includes('-race'), 'run_go_race must retain Go race detection');
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

// The published-deps law: a build consumes PUBLISHED packages, never a local
// gitlink copy. This test used to assert the OPPOSITE — that the Dockerfile
// staged a `replace` onto sdn-server/third_party/spacedatastandards-go before
// `go mod download`. That replacement is gone (SDS is required at a real
// version), so the guarantee worth holding is that it does not come back: a
// replace directive would make the released image build from whatever happened
// to be in the working tree.
test('Docker release image builds against published Go modules, never a local replacement', () => {
  const goMod = readRepoFile('sdn-server/go.mod');

  const replaces = goMod
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line.startsWith('replace ') || (line.includes('=>') && !line.startsWith('//')));
  assert.deepEqual(replaces, [], 'sdn-server/go.mod must carry no replace directives');
  assert.match(
    goMod,
    /github\.com\/DigitalArsenal\/spacedatastandards\.org\/lib\/go v\d+\.\d+\.\d+/,
    'SDS must be required at a published version',
  );
});

// The AOT cache is keyed on the engine bytes AND the libwasmedge version, and a
// miss does not fail — it silently falls back to the ~100x interpreter, which
// has taken production down twice (memory: host01-aot-artifact-drift-recovery).
// So every lane that builds a shipped artifact must install the SAME WasmEdge:
// scripts/install-wasmedge.sh is the one pin, and the workflows had drifted two
// minor versions behind it (0.14.0 vs 0.16.4) while the release cutter and the
// Docker image were on 0.16.4.
test('every workflow installs the WasmEdge the release cutter and Docker image use', () => {
  const installer = readRepoFile('scripts/install-wasmedge.sh');
  const canonical = /WASMEDGE_VERSION="\$\{WASMEDGE_VERSION:-([0-9.]+)\}"/.exec(installer);
  assert.ok(canonical, 'scripts/install-wasmedge.sh must declare the default WasmEdge version');
  const pin = canonical[1];

  const cutter = readRepoFile('deployment/release/cut-release-local.sh');
  assert.match(
    cutter,
    new RegExp(`WASMEDGE_VERSION:-${pin.replaceAll('.', '\\.')}\\}`),
    'the release cutter must use the canonical WasmEdge pin',
  );

  for (const workflow of ['beta-release-artifacts.yml', 'live-dht-cross-platform.yml']) {
    const text = readRepoFile(`.github/workflows/${workflow}`);
    const declared = [...text.matchAll(/^\s*WASMEDGE_VERSION:\s*([0-9.]+)\s*$/gm)].map((m) => m[1]);
    assert.deepEqual(
      [...new Set(declared)],
      [pin],
      `${workflow} must pin WasmEdge ${pin}, the version install-wasmedge.sh installs`,
    );
  }
});

test('Docker release builder Go version matches the server module Go directive', () => {
  const dockerfile = readRepoFile('deployment/docker/Dockerfile');
  const goMod = readRepoFile('sdn-server/go.mod');
  const builderVersion = /^FROM\s+golang:(\d+)\.(\d+)(?:\.\d+)?(?:-[^\s]+)?\s+AS\s+builder\s*$/im.exec(dockerfile);
  const moduleVersion = /^go\s+(\d+)\.(\d+)(?:\.\d+)?\s*$/m.exec(goMod);

  assert.ok(builderVersion, 'Dockerfile must declare a versioned golang builder image');
  assert.ok(moduleVersion, 'sdn-server/go.mod must declare a Go version');
  assert.equal(
    `${builderVersion[1]}.${builderVersion[2]}`,
    `${moduleVersion[1]}.${moduleVersion[2]}`,
    'Docker builder Go major.minor must match the sdn-server module Go major.minor',
  );
});

test('beta release workflow builds updater wasm once before platform CLI archives', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  assert.match(workflow, /updater-wasm:\s*\n\s*name:\s*Build updater module wasm/);
  assert.match(workflow, /name:\s*updater-module-wasm[\s\S]*path:\s*packages\/sdn-updater-module\/dist\/isomorphic\/module\.wasm/);
  assert.match(workflow, /needs:\s*\[beta-version, ipfs, updater-wasm, wasmedge-static\]/);
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

test('every CLI target links WasmEdge in and ships no runtime beside the binary', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  assert.match(workflow, /target_os:\s*windows[\s\S]*runner:\s*windows-latest/);

  // Windows used to be the exception, shipping wasmedge.dll next to the exe.
  // It is not any more: all five targets restore a static prefix and link the
  // runtime into the executable. Nothing may download or stage a DLL, and
  // nothing may pass --wasmedge-path, which is what tells the bundler to put a
  // runtime in the archive.
  assert.doesNotMatch(workflow, /wasmedge\.dll/);
  assert.doesNotMatch(workflow, /WasmEdge-\$\{WASMEDGE_VERSION\}-(windows|Windows)/);
  assert.doesNotMatch(workflow, /--wasmedge-path/);

  const cliJob = workflow.slice(workflow.indexOf('  cli:'), workflow.indexOf('  cli-update-feed:'));
  assert.match(cliJob, /name:\s*Restore the static WasmEdge prefix[\s\S]*fail-on-cache-miss:\s*true/);
  assert.match(cliJob, /go-with-wasmedge\.sh build -o/);
});

test('the static WasmEdge cache key covers everything that shapes a prefix', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  // Three separate outages came from a fix that landed but never ran, because a
  // prefix built by older code stayed in the cache. The key must therefore move
  // whenever the build script, the toolchain-selection script, or the installed
  // package set (tagged by matrix.toolchain, since no hashFiles can see an
  // action's literal inputs) changes.
  // The valid tags come from the matrix itself, not a list written here — a
  // hardcoded list is the same staleness this test exists to catch, and it did
  // go stale the first time the Linux toolchain moved.
  const declared = [...workflow.matchAll(/^\s*toolchain:\s*([\w.-]+)\s*$/gm)].map((m) => m[1]);
  assert(declared.length > 0, 'no matrix entry declares a toolchain tag');
  const tags = [...new Set(declared)];

  const keys = [...workflow.matchAll(/key:\s*(wasmedge-static-[^\n]*)/g)].map((m) => m[1]);
  assert(keys.length >= 4, `expected every consumer to name the key, saw ${keys.length}`);
  for (const key of keys) {
    assert.match(key, /hashFiles\('scripts\/build-static-wasmedge\.sh', 'scripts\/wasmedge-static-env\.sh'\)/);
    const named = key.includes('matrix.toolchain') || tags.some((tag) => key.includes(tag));
    assert(named, `key names no declared toolchain (${tags.join(', ')}): ${key}`);
  }

  // Hashing the whole workflow was the previous, far too broad answer: it threw
  // away five prefixes and rebuilt LLVM on five runners for an edit to an
  // unrelated job.
  assert.doesNotMatch(workflow, /hashFiles\([^)]*beta-release-artifacts\.yml[^)]*\)/);
});

test('beta release workflow builds signed CLI update feed artifacts', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  assert.match(workflow, /cli-update-feed:\s*\n\s*name:\s*Build signed CLI update feed/);
  assert.match(workflow, /needs:\s*\[beta-version, cli\]/);
  assert.match(workflow, /SDN_UPDATE_SIGNING_KEY_PEM:\s*\$\{\{ secrets\.SDN_UPDATE_SIGNING_KEY_PEM \}\}/);
  assert.match(workflow, /SDN_UPDATE_KEY_ID:\s*\$\{\{ secrets\.SDN_UPDATE_KEY_ID \}\}/);
  assert.match(workflow, /SDN_UPDATE_SEQUENCE:\s*\$\{\{ github\.run_number \}\}/);
  assert.match(workflow, /build-cli-update-payload\.mjs/);
  assert.match(workflow, /build-sdn-update-feed\.js/);
  assert.match(workflow, /spacedatanetwork-update-feed-\$\{VERSION\}\.tar\.gz/);
  assert.match(workflow, /name:\s*cli-update-feed[\s\S]*path:\s*dist\/update-feed\/spacedatanetwork-update-feed-\$\{\{ needs\.beta-version\.outputs\.package_version \}\}\.tar\.gz/);
  assert.match(workflow, /name:\s*cli-update-feed[\s\S]*path:\s*dist\/update-feed/);
  assert.match(workflow, /needs:\s*\[beta-version, ipfs, docker, cli, cli-update-feed, packages, sdn-js-package\]/);
  assert.match(workflow, /needs:\s*\[beta-version, ipfs, docker, cli, cli-update-feed, desktop, packages, sdn-js-package, artifact-docker-test\]/);
});

test('beta release workflow builds desktop app artifacts for every supported OS', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  assert.match(workflow, /desktop:\s*\n\s*name:\s*Build desktop app artifacts/);
  // The desktop app SHIPS the node, so it waits on the cli archives rather than
  // on the IPFS asset job. It no longer stages its own copy of the dashboard:
  // the node serves the one embedded in its binary (//go:embed dashboard.html),
  // and a second copy in desktop/assets/sdn-ui was the old architecture.
  assert.match(workflow, /desktop:[\s\S]*?needs:\s*\[beta-version, cli\]/);
  assert.doesNotMatch(workflow, /desktop\/assets\/sdn-ui\//);
  assert.match(workflow, /target_os:\s*macos[\s\S]*runner:\s*macos-14/);
  assert.match(workflow, /target_os:\s*macos[\s\S]*runner:\s*macos-15-intel/);
  assert.match(workflow, /target_os:\s*windows[\s\S]*runner:\s*windows-latest/);
  assert.match(workflow, /target_os:\s*linux[\s\S]*runner:\s*ubuntu-latest/);
  assert.match(workflow, /npm --prefix desktop ci/);
  // Each app carries the bundle built for its own OS and architecture.
  assert.match(workflow, /stage-node-bundle\.mjs --from/);
  assert.match(workflow, /electron-builder --publish never \$\{\{ matrix\.builder_args \}\}/);
  assert.match(workflow, /builder_args:\s*--mac dmg zip --arm64/);
  assert.match(workflow, /builder_args:\s*--mac dmg zip --x64/);
  assert.match(workflow, /builder_args:\s*--win nsis portable --x64/);
  assert.match(workflow, /builder_args:\s*--linux AppImage deb tar\.xz --x64/);
  assert.match(workflow, /name:\s*desktop-\$\{\{ matrix\.target_os \}\}/);
  assert.match(workflow, /path:\s*dist\/desktop\/\*/);
  assert.match(workflow, /needs:\s*\[beta-version, ipfs, docker, cli, cli-update-feed, desktop, packages, sdn-js-package, artifact-docker-test\]/);
  assert.match(workflow, /pattern:\s*desktop-\*[\s\S]*path:\s*dist\/desktop[\s\S]*merge-multiple:\s*true/);
});

test('beta release workflow smoke-tests published installers after release', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  assert.match(workflow, /published-installer-smoke:\s*\n\s*name:\s*Smoke published installers/);
  assert.match(workflow, /published-installer-smoke:[\s\S]*needs:\s*\[beta-version, release\]/);
  assert.match(workflow, /SDN_VERSION:\s*\$\{\{ needs\.beta-version\.outputs\.release_tag \}\}/);
  assert.match(workflow, /target_os:\s*linux[\s\S]*runner:\s*ubuntu-latest/);
  assert.match(workflow, /target_os:\s*macos[\s\S]*runner:\s*macos-14/);
  assert.match(workflow, /target_os:\s*windows[\s\S]*runner:\s*windows-latest/);
  assert.match(workflow, /node deployment\/release\/published-install-smoke\.mjs --platform \$\{\{ matrix\.target_os \}\}/);
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

  // The image builds the daemon through the SAME wrapper every other build
  // product uses, so the static link line has exactly one definition. It used
  // to carry a hand-written copy of the archive list, which went stale and
  // failed to link (missing libwasmedgePluginWasiLogging.a) while the CLI
  // archives built fine off the real list.
  assert.match(dockerfile, /go-with-wasmedge\.sh build -o \/out\/spacedatanetwork \.\/cmd\/spacedatanetwork/);
  assert.doesNotMatch(dockerfile, /llvm-config-16 --link-static/);
  assert.match(dockerfile, /go build -tags edge[\s\S]*-o \/out\/spacedatanetwork-edge \.\/cmd\/spacedatanetwork-edge/);
  // Fails the build rather than shipping an image that needs a runtime present.
  assert.match(dockerfile, /ldd \/out\/spacedatanetwork \| grep -qi wasmedge/);
  assert.match(dockerfile, /ENTRYPOINT \["\/app\/spacedatanetwork"\]/);
  assert.match(dockerfile, /CMD \["daemon", "--config", "\/app\/config\/full-docker\.yaml"\]/);
});

test('nothing invokes an npm script its package.json does not declare', () => {
  // `npm run build:ui` outlived the script by months. Deleting it broke
  // nothing at the time; the jobs failed one at a time, whenever each next ran
  // — ipfs-deploy.sh first, then the release packages job, on the same missing
  // script. A deleted script is invisible to every caller until that caller
  // runs, which for a release job can be weeks.
  const scriptsAt = (dir) => {
    try {
      return JSON.parse(readRepoFile(dir ? `${dir}/package.json` : 'package.json')).scripts ?? {};
    } catch {
      return null;
    }
  };
  // Where an unprefixed `npm run` could be resolving from: the caller either
  // cd'd or set working-directory, which this cannot see, so accept any.
  const anyRoot = ['', 'sdn-js', 'webui'];
  const callers = [
    'deployment/scripts/package-linux-vm-bundle.sh',
    'deployment/scripts/deploy.sh',
    'deployment/ipfs/ipfs-deploy.sh',
    'scripts/admin-dev.sh',
    '.github/workflows/beta-release-artifacts.yml',
    '.github/workflows/security.yml',
  ];

  let checked = 0;
  for (const caller of callers) {
    const text = readRepoFile(caller);
    for (const m of text.matchAll(/npm(?:\s+--prefix\s+(\S+))?\s+run\s+([A-Za-z0-9:_-]+)/g)) {
      const [, rawPrefix, script] = m;
      if (script.includes('$')) continue;
      const prefix = rawPrefix?.replace(/^\.\//, '').replace(/\/$/, '');
      const dirs = prefix ? [prefix] : anyRoot;
      const known = dirs.map(scriptsAt);
      // A prefix naming no package.json at all is its own bug.
      assert(
        known.some((s) => s !== null),
        `${caller} runs npm --prefix ${prefix}, where there is no package.json`,
      );
      assert(
        known.some((s) => s?.[script]),
        `${caller} runs "npm run ${script}", which no package.json in ${dirs.join(', ') || 'the repo root'} declares`,
      );
      checked += 1;
    }
  }
  assert(checked > 0, 'the npm-script scan matched nothing — the pattern has rotted');
});
