// The three installers' fresh-node key password step, RUN against a stub
// binary (the node's own decision is tested in Go: cmd/spacedatanetwork
// key_password_file_cli_test.go). What is pinned here is the wiring: each
// installer asks `key fingerprint-check` first, asks `key fresh-password-file`
// only on "refused", records the file only on "created"/"exists", and leaves a
// node with a configured key password alone.
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = dirname(dirname(dirname(fileURLToPath(import.meta.url))));
const read = (p) => readFileSync(join(repoRoot, p), 'utf8');

// A stub `spacedatanetwork`: logs its argv, answers fingerprint-check with
// $STUB_VERDICT and fresh-password-file with $STUB_OUTCOME (creating the file
// for "created"), and prints SDN_KEY_PASSWORD_FILE for anything else.
function stubBinary(dir) {
  const path = join(dir, 'spacedatanetwork');
  writeFileSync(path, `#!/bin/sh
echo "$*" >> "${dir}/calls.log"
case "$*" in
  *"key fingerprint-check"*) echo "$STUB_VERDICT"; echo "stub reason" >&2 ;;
  *"key fresh-password-file"*)
    for last in "$@"; do :; done
    case "$STUB_OUTCOME" in created) mkdir -p "$(dirname "$last")"; printf 'x' > "$last"; chmod 0400 "$last"; echo "created $last" ;;
      exists) echo "exists $last" ;; *) echo "skipped: $STUB_OUTCOME" ;; esac ;;
  *) echo "EXEC SDN_KEY_PASSWORD_FILE=\${SDN_KEY_PASSWORD_FILE:-}" ;;
esac
`);
  chmodSync(path, 0o755);
  return path;
}

const calls = (dir) => (existsSync(join(dir, 'calls.log')) ? readFileSync(join(dir, 'calls.log'), 'utf8') : '');

// ---------------------------------------------------------------- install.sh
const installSh = read('scripts/install.sh');
// The installer's key password block: its variables, prepare_key_password_file
// and initialize_unix_node, exactly as shipped.
const installBlock = installSh.match(/\nKEY_PASSWORD_FILE=[\s\S]*?\ninitialize_unix_node\(\) \{[\s\S]*?\n\}\n/)?.[0];

function runInstallBlock({ verdict, outcome, env = {}, call = 'prepare_key_password_file' }) {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-install-kp-'));
  const home = join(dir, 'home');
  mkdirSync(home, { recursive: true });
  const bin = stubBinary(dir);
  const script = `
log_info() { echo "INFO $*"; }
log_warn() { echo "WARN $*"; }
log_error() { echo "ERROR $*"; }
PRIMARY_COMMAND_PATH='${bin}'
PRIMARY_BINARY_NAME=spacedatanetwork
OS=linux
${installBlock}
${call}
echo "INIT_ARGS=\${INIT_ARGS[*]}"
`;
  const out = execFileSync('bash', ['-c', script], {
    encoding: 'utf8',
    env: { PATH: process.env.PATH, HOME: home, STUB_VERDICT: verdict, STUB_OUTCOME: outcome, ...env },
  });
  return { dir, home, out, calls: calls(dir) };
}
const runPrepare = (opts) => runInstallBlock(opts);

test('install.sh: a refused fresh node gets the file and init --key-password-file', () => {
  assert.ok(installBlock, 'the key password block and initialize_unix_node are in scripts/install.sh');
  const { home, out, calls: log } = runPrepare({ verdict: 'refused', outcome: 'created' });
  assert.match(log, /^key fingerprint-check$/m);
  assert.match(log, new RegExp(`^-c ${home}/\\.spacedatanetwork/config\\.yaml key fresh-password-file ${home}/sdn-key-password$`, 'm'));
  assert.match(out, new RegExp(`INIT_ARGS=--key-password-file ${home}/sdn-key-password`));
  assert.match(out, /WARN KEEP .* back it up apart from ~\/\.spacedatanetwork: .* anyone holding both can open it/);
  assert.match(installSh, /"\$PRIMARY_COMMAND_PATH" -c "\$NODE_CONFIG_PATH" init "\$\{INIT_ARGS\[@\]\}"/);
});

// A copy of the node's data directory alone must not open the identity: the
// generated file lives outside ~/.spacedatanetwork (the manual recipe's
// ~/sdn-key-password), never beside the keys.
test('install.sh: the generated file is outside the node data directory', () => {
  assert.match(installSh, /\nKEY_PASSWORD_FILE="\$\{HOME\}\/sdn-key-password"\n/);
  const { home, out } = runPrepare({ verdict: 'refused', outcome: 'created' });
  assert.ok(existsSync(join(home, 'sdn-key-password')));
  assert.equal(existsSync(join(home, '.spacedatanetwork', 'key-password')), false);
  assert.doesNotMatch(out, /\.spacedatanetwork\/key-password/);
});

// THE SPOOF. With no -c, the binary resolves its config through the process
// table first, which every account on the machine writes to: a stranger's
// process claiming `spacedatanetwork daemon --config /tmp/theirs.yaml` chose
// the key directory the fresh-node check looked in and the config `init`
// saved. Every node command the installer runs names the node's own config.
test('install.sh: every node command names the node config, never the process table', () => {
  const { home, calls: log } = runInstallBlock({ verdict: 'refused', outcome: 'created', call: 'initialize_unix_node' });
  const own = `${home}/.spacedatanetwork/config.yaml`;
  const lines = log.trim().split('\n');
  for (const command of ['key fresh-password-file', 'init', 'show-identity']) {
    const line = lines.find((l) => l.includes(` ${command}`) || l.startsWith(command));
    assert.ok(line, `${command} ran: ${log}`);
    assert.ok(line.startsWith(`-c ${own} ${command}`), `${command} must be given -c ${own}: ${line}`);
  }
  assert.match(log, new RegExp(`^-c ${own} init --key-password-file ${home}/sdn-key-password$`, 'm'));

  // An operator's own SDN_CONFIG is that config.
  const custom = runInstallBlock({
    verdict: 'refused', outcome: 'created', call: 'initialize_unix_node', env: { SDN_CONFIG: '/srv/node/config.yaml' },
  });
  for (const command of ['key fresh-password-file', 'init', 'show-identity']) {
    assert.match(custom.calls, new RegExp(`^-c /srv/node/config\\.yaml ${command}`, 'm'), command);
  }
});

test('install.sh: an existing file is used, not replaced', () => {
  const { out } = runPrepare({ verdict: 'refused', outcome: 'exists' });
  assert.match(out, /INIT_ARGS=--key-password-file /);
});

test('install.sh: ok, absent, an old binary, a skip, or a configured password change nothing', () => {
  for (const verdict of ['ok', 'absent', 'Usage: spacedatanetwork key [command]']) {
    const { out, calls: log } = runPrepare({ verdict, outcome: 'created' });
    assert.match(out, /INIT_ARGS=$/m, verdict);
    assert.doesNotMatch(log, /fresh-password-file/, verdict);
  }
  const skippedRun = runPrepare({ verdict: 'refused', outcome: 'this node already has an identity' });
  assert.match(skippedRun.out, /INIT_ARGS=$/m);
  for (const env of [{ SDN_KEY_PASSWORD_FILE: '/run/secrets/k' }, { SDN_KEY_PASSWORD: 'p' }]) {
    const { out, calls: log } = runPrepare({ verdict: 'refused', outcome: 'created', env });
    assert.match(out, /INIT_ARGS=$/m);
    assert.equal(log, '', 'nothing is even asked when a key password is supplied');
  }
});

// ---------------------------------------------------------- container image
function runEntrypoint(args, { verdict = 'refused', outcome = 'created', env = {}, preexisting = false } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-entry-kp-'));
  const bin = stubBinary(dir);
  const keyFile = join(dir, 'data/key-password');
  if (preexisting) {
    mkdirSync(dirname(keyFile), { recursive: true });
    writeFileSync(keyFile, 'y', { mode: 0o400 });
  }
  const script = read('deployment/docker/entrypoint.sh')
    .replaceAll('/app/spacedatanetwork', bin)
    .replaceAll('/app/data/key-password', keyFile);
  const path = join(dir, 'entrypoint.sh');
  writeFileSync(path, script, { mode: 0o755 });
  const result = spawnSync('sh', [path, ...args], {
    encoding: 'utf8',
    env: { PATH: process.env.PATH, STUB_VERDICT: verdict, STUB_OUTCOME: outcome, ...env },
  });
  assert.equal(result.status, 0, result.stderr);
  return { dir, keyFile, out: result.stdout, err: result.stderr, calls: calls(dir) };
}

test('entrypoint: a refused fresh node creates the file in the volume and the daemon gets it', () => {
  const { keyFile, out, err, calls: log } = runEntrypoint(['daemon', '--config', '/app/config/full-docker.yaml']);
  assert.match(log, /^key fingerprint-check$/m);
  assert.match(log, new RegExp(`^-c /app/config/full-docker\\.yaml key fresh-password-file --key-dir-within /app/data ${keyFile}$`, 'm'));
  assert.match(out, new RegExp(`EXEC SDN_KEY_PASSWORD_FILE=${keyFile}`));
  // The log says plainly what keeping it in the volume costs, and how CLI
  // commands get the file (docker exec does not pass through the entrypoint).
  assert.match(err, /IN THE DATA VOLUME: a copy of the volume opens the identity and the credential store/);
  assert.match(err, /docker exec <container> \/app\/entrypoint\.sh <command>/);
});

// The file is kept in the data volume, so it is generated only when the
// config keeps the key directory there too; and the config is found in every
// spelling the node itself accepts, or the check would run against the image
// default instead of the operator's config.
test('entrypoint: every config spelling reaches fresh-password-file, with --key-dir-within', () => {
  for (const args of [
    ['daemon', '--config', '/srv/c.yaml'],
    ['daemon', '--config=/srv/c.yaml'],
    ['daemon', '-c', '/srv/c.yaml'],
    ['daemon', '-c=/srv/c.yaml'],
    ['daemon', '-c/srv/c.yaml'],
    ['init', '--debug', '-c/srv/c.yaml'],
  ]) {
    const { keyFile, calls: log } = runEntrypoint(args);
    assert.match(log, new RegExp(`^-c /srv/c\\.yaml key fresh-password-file --key-dir-within /app/data ${keyFile}$`, 'm'), args.join(' '));
  }
});

test('entrypoint: a skip on a refused host is logged with its reason', () => {
  const { keyFile, err } = runEntrypoint(['daemon', '-c', '/srv/c.yaml'], {
    outcome: "this node's key directory /srv/keys is not inside /app/data",
  });
  assert.match(err, /no key password file generated \(this node's key directory \/srv\/keys is not inside \/app\/data\)/);
  assert.equal(existsSync(keyFile), false);
});

// docker exec does not run the entrypoint, so the variable it exports never
// reaches a CLI command; running the command THROUGH the entrypoint works for
// every node (generated file, mounted secret, or machine key).
test('entrypoint: a CLI command run through it gets the generated file', () => {
  const { keyFile, out, calls: log } = runEntrypoint(['show-identity', '--config', '/app/config/full-docker.yaml', '--show-mnemonic'], { preexisting: true });
  assert.match(out, new RegExp(`EXEC SDN_KEY_PASSWORD_FILE=${keyFile}`));
  assert.doesNotMatch(log, /fingerprint-check|fresh-password-file/);
});

test('entrypoint: a later start uses the file without asking; a supplied password wins', () => {
  const later = runEntrypoint(['daemon'], { preexisting: true });
  assert.match(later.out, new RegExp(`EXEC SDN_KEY_PASSWORD_FILE=${later.keyFile}`));
  assert.doesNotMatch(later.calls, /fingerprint-check|fresh-password-file/);

  const mounted = runEntrypoint(['daemon'], { env: { SDN_KEY_PASSWORD_FILE: '/run/secrets/sdn-key' } });
  assert.match(mounted.out, /EXEC SDN_KEY_PASSWORD_FILE=\/run\/secrets\/sdn-key/);
  assert.doesNotMatch(mounted.calls, /fresh-password-file/);
});

test('entrypoint: ok, a skipped node, or a one-shot command get no file', () => {
  for (const [args, opts] of [
    [['daemon'], { verdict: 'ok' }],
    [['daemon'], { outcome: 'this node already has an identity' }],
    [['version'], {}],
  ]) {
    const { keyFile, out } = runEntrypoint(args, opts);
    assert.match(out, /EXEC SDN_KEY_PASSWORD_FILE=$/m, JSON.stringify(args));
    assert.equal(existsSync(keyFile), false, JSON.stringify(args));
  }
});

// ------------------------------------------------------------- .deb / .rpm
const postinstall = read('deployment/packaging/scripts/postinstall.sh');

function runPostinstallKeyStep({ verdict = 'refused', outcome = 'created', otherDropin = '' } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-postinst-kp-'));
  const bin = stubBinary(dir);
  const etc = join(dir, 'etc/systemd/system');
  mkdirSync(etc, { recursive: true });
  writeFileSync(join(etc, 'spacedatanetwork.service'), '[Service]\nUser=sdn\n');
  if (otherDropin) {
    mkdirSync(join(etc, 'spacedatanetwork.service.d'), { recursive: true });
    writeFileSync(join(etc, 'spacedatanetwork.service.d/operator.conf'), otherDropin);
  }
  const keyFile = join(dir, 'var/lib/spacedatanetwork/key-password');
  mkdirSync(dirname(keyFile), { recursive: true });
  const block = postinstall.match(/\nSDN_BIN=[\s\S]*?\nsetup_fresh_key_password \|\| true\n/)?.[0];
  assert.ok(block, 'the fresh key password block is in postinstall.sh');
  const script = `set -e
run_as_sdn() { "$@"; }
${block
    .replace(/\nrun_as_sdn\(\) \{[\s\S]*?\n\}\n/, '\n')
    .replaceAll('/opt/spacedatanetwork/bin/spacedatanetwork', bin)
    .replaceAll('/etc/systemd/system', etc)
    .replaceAll('/run/systemd/system', join(dir, 'run'))
    .replaceAll('/usr/lib/systemd/system', join(dir, 'usr-lib'))
    .replaceAll('/lib/systemd/system', join(dir, 'lib'))
    .replaceAll('/var/lib/spacedatanetwork/key-password', keyFile)}`;
  const out = execFileSync('sh', ['-c', script], {
    encoding: 'utf8',
    env: { PATH: process.env.PATH, STUB_VERDICT: verdict, STUB_OUTCOME: outcome },
  });
  const dropin = join(etc, 'spacedatanetwork.service.d/key-password.conf');
  return { out, calls: calls(dir), keyFile, dropin, dropinText: existsSync(dropin) ? readFileSync(dropin, 'utf8') : '' };
}

test('postinstall: a refused fresh node gets the file and a drop-in naming it', () => {
  const { calls: log, keyFile, dropinText } = runPostinstallKeyStep();
  assert.match(log, /^key fingerprint-check$/m);
  assert.match(log, new RegExp(`^-c /etc/spacedatanetwork/config\\.yaml key fresh-password-file ${keyFile}$`, 'm'));
  assert.match(dropinText, /^\[Service\]$/m);
  assert.match(dropinText, new RegExp(`^Environment=SDN_KEY_PASSWORD_FILE=${keyFile}$`, 'm'));
  assert.match(postinstall, /runuser -u sdn --/);
});

test('postinstall: ok, a skipped node, or an operator password write nothing', () => {
  for (const opts of [
    { verdict: 'ok' },
    { verdict: 'absent' },
    { outcome: 'this node already has an identity' },
    { otherDropin: '[Service]\nEnvironment=SDN_KEY_PASSWORD_FILE=/etc/sdn-key\n' },
  ]) {
    const { dropinText, calls: log } = runPostinstallKeyStep(opts);
    assert.equal(dropinText, '', JSON.stringify(opts));
    if (opts.otherDropin) assert.equal(log, '', 'an operator drop-in means the node is not even asked');
  }
});

test('postremove keeps the drop-in: a kept identity needs it', () => {
  const postremove = read('deployment/packaging/scripts/postremove.sh');
  const commands = postremove.split('\n').filter((line) => !line.trim().startsWith('#')).join('\n');
  assert.doesNotMatch(commands, /key-password/);
  assert.match(postremove, /key-password\.conf/);
});

test('postinstall: the message says what a copy of the data directory opens, and how CLI commands get the file', () => {
  const { out } = runPostinstallKeyStep();
  assert.match(out, /a copy of \/var\/lib\/spacedatanetwork alone opens the identity and the credential store/);
  assert.match(out, /sudo -u sdn env SDN_KEY_PASSWORD_FILE=\S+ \S+ -c \/etc\/spacedatanetwork\/config\.yaml <command>/);
});

// ------------------------------------------------------------------- docs
const docs = {
  'docs/INSTALL.md': read('docs/INSTALL.md'),
  'docs/onboarding.html': read('docs/onboarding.html'),
  'docs/releases/unreleased.md': read('docs/releases/unreleased.md'),
};

// The documented backup step, `docker exec ... show-identity --show-mnemonic`,
// bypassed the entrypoint and so never got the generated key password file:
// it failed on exactly the containers that have one. Every documented docker
// exec runs the command through /app/entrypoint.sh.
test('docs: docker exec commands run through the entrypoint', () => {
  for (const [name, text] of Object.entries(docs)) {
    const execs = text.replace(/\s+/g, ' ').match(/docker exec [^`<]*/g) ?? [];
    for (const line of execs) {
      if (/<container>|<command>/.test(line) || !/\/app\//.test(line)) continue; // prose, not a command
      assert.match(line, /docker exec (-e SDN_KEY_PASSWORD_FILE=\S+ )?\S+ \/app\/entrypoint\.sh /, `${name}: ${line}`);
    }
  }
  assert.match(docs['docs/INSTALL.md'], /\/app\/entrypoint\.sh show-identity --config \/app\/config\/full-docker\.yaml --show-mnemonic/);
  assert.match(docs['docs/onboarding.html'], /\/app\/entrypoint\.sh show-identity --config \/app\/config\/full-docker\.yaml --show-mnemonic/);
});

// A recipe that writes the secret and chmods afterwards leaves it readable by
// everyone for a moment (and an fd opened then keeps reading). Every recipe
// sets a umask first, or writes into a file `install -m 0400` already made.
test('docs: every key password recipe creates the file private first', () => {
  const recipeSources = { ...docs, 'internal/keys/derivation.go': read('sdn-server/internal/keys/derivation.go') };
  let seen = 0;
  for (const [name, text] of Object.entries(recipeSources)) {
    const lines = text.split('\n');
    lines.forEach((line, i) => {
      if (!line.includes('head -c 32 /dev/urandom')) return;
      seen += 1;
      if (/umask 077/.test(line) && line.indexOf('umask 077') < line.indexOf('head -c 32')) return;
      const target = line.match(/tee (\S+)/)?.[1];
      assert.ok(target, `${name}:${i + 1}: a recipe without a umask must write with tee into a pre-made file: ${line}`);
      const premade = lines.slice(Math.max(0, i - 3), i).some((l) => l.includes('install -m 0400') && l.includes(`/dev/null ${target}`));
      assert.ok(premade, `${name}:${i + 1}: ${target} must be created 0400 (install -m 0400 ... /dev/null) before the secret is written`);
    });
  }
  assert.ok(seen >= 4, `expected the documented recipes, saw ${seen}`);
});

// The generated locations, as the scripts make them, and the consequence of
// each stated plainly where it is in the node's data.
test('docs: the generated paths match the installers, and a data copy opening the identity is said', () => {
  const install = docs['docs/INSTALL.md'];
  assert.match(installSh, /KEY_PASSWORD_FILE="\$\{HOME\}\/sdn-key-password"/);
  assert.match(read('deployment/docker/entrypoint.sh'), /\nKEY_PASSWORD_FILE=\/app\/data\/key-password\n/);
  assert.match(postinstall, /\nKEY_PASSWORD_FILE=\/var\/lib\/spacedatanetwork\/key-password\n/);
  for (const path of ['~/sdn-key-password', '/app/data/key-password', '/var/lib/spacedatanetwork/key-password']) {
    assert.ok(install.includes(path), `INSTALL.md names ${path}`);
    assert.ok(docs['docs/releases/unreleased.md'].includes(path), `release notes name ${path}`);
  }
  assert.doesNotMatch(install + docs['docs/releases/unreleased.md'], /\.key-password|\.spacedatanetwork\/key-password/);
  assert.match(install, /a copy of the volume alone \([^)]*\) opens the node identity and its credential store/);
  assert.match(install, /a copy\s+of `\/var\/lib\/spacedatanetwork` alone opens the node identity and its\s+credential store/);
  assert.match(docs['docs/releases/unreleased.md'], /a copy of the volume alone opens the node identity and its credential\s+store/);
});

// Releases are named by what they carry, not by a version that may be tagged
// before or after this change lands.
test('docs: no claim about which v1.1.x is the last manual release', () => {
  for (const [name, text] of Object.entries(docs)) {
    assert.doesNotMatch(text, /v1\.1\.[3-9]|[Tt]hrough v1\.1|v1\.1\.0 to v1\.1/, name);
  }
});
