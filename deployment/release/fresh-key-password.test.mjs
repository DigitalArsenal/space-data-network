// The three installers' fresh-node key password step, RUN against a stub
// binary (the node's own decision is tested in Go: cmd/spacedatanetwork
// key_password_file_cli_test.go). What is pinned here is the wiring: each
// installer asks `key fingerprint-check` first, asks `key fresh-password-file`
// only on "refused", records the file only on "created"/"exists", and leaves a
// node with a configured key password alone.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
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
const prepareFn = installSh.match(/\nprepare_key_password_file\(\) \{[\s\S]*?\n\}\n/)?.[0];

function runPrepare({ verdict, outcome, env = {} }) {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-install-kp-'));
  const bin = stubBinary(dir);
  const script = `
log_info() { echo "INFO $*"; }
log_warn() { echo "WARN $*"; }
log_error() { echo "ERROR $*"; }
PRIMARY_COMMAND_PATH='${bin}'
PRIMARY_BINARY_NAME=spacedatanetwork
KEY_PASSWORD_FILE='${dir}/home/.spacedatanetwork/key-password'
${prepareFn}
prepare_key_password_file
echo "INIT_ARGS=\${INIT_ARGS[*]}"
`;
  const out = execFileSync('bash', ['-c', script], {
    encoding: 'utf8',
    env: { PATH: process.env.PATH, STUB_VERDICT: verdict, STUB_OUTCOME: outcome, ...env },
  });
  return { dir, out, calls: calls(dir) };
}

test('install.sh: a refused fresh node gets the file and init --key-password-file', () => {
  assert.ok(prepareFn, 'prepare_key_password_file is defined in scripts/install.sh');
  const { dir, out, calls: log } = runPrepare({ verdict: 'refused', outcome: 'created' });
  assert.match(log, /^key fingerprint-check$/m);
  assert.match(log, new RegExp(`^key fresh-password-file ${dir}/home/\\.spacedatanetwork/key-password$`, 'm'));
  assert.match(out, new RegExp(`INIT_ARGS=--key-password-file ${dir}/home/\\.spacedatanetwork/key-password`));
  assert.match(out, /WARN KEEP .* back it up apart from the node's data/);
  assert.match(installSh, /"\$PRIMARY_COMMAND_PATH" init "\$\{INIT_ARGS\[@\]\}"/);
  assert.match(installSh, /KEY_PASSWORD_FILE="\$\{HOME\}\/\.spacedatanetwork\/key-password"/);
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
  const keyFile = join(dir, 'data/keys/.key-password');
  if (preexisting) {
    mkdirSync(dirname(keyFile), { recursive: true });
    writeFileSync(keyFile, 'y', { mode: 0o400 });
  }
  const script = read('deployment/docker/entrypoint.sh')
    .replaceAll('/app/spacedatanetwork', bin)
    .replaceAll('/app/data/keys/.key-password', keyFile);
  const path = join(dir, 'entrypoint.sh');
  writeFileSync(path, script, { mode: 0o755 });
  const result = execFileSync('sh', [path, ...args], {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { PATH: process.env.PATH, STUB_VERDICT: verdict, STUB_OUTCOME: outcome, ...env },
  });
  return { dir, keyFile, out: result, calls: calls(dir) };
}

test('entrypoint: a refused fresh node creates the file in the volume and the daemon gets it', () => {
  const { keyFile, out, calls: log } = runEntrypoint(['daemon', '--config', '/app/config/full-docker.yaml']);
  assert.match(log, /^key fingerprint-check$/m);
  assert.match(log, new RegExp(`^-c /app/config/full-docker\\.yaml key fresh-password-file ${keyFile}$`, 'm'));
  assert.match(out, new RegExp(`EXEC SDN_KEY_PASSWORD_FILE=${keyFile}`));
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
