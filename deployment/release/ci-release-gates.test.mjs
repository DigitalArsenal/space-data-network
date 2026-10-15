// Regression tests for the release gates (security audit SEC-042 / SEC-044 /
// SEC-045, fix round 2). Each test drives the shipped file: the workflow's own
// step script runs under bash against a fake `gh`, the signing script runs on
// a throwaway git repository with throwaway keys, and the apt-key script runs
// gpg on throwaway keys. Nothing here touches the network or a real key.
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { createHash, generateKeyPairSync } from 'node:crypto';
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const repoRoot = dirname(dirname(dirname(fileURLToPath(import.meta.url))));
const read = (rel) => readFileSync(join(repoRoot, rel), 'utf8');
const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');

/** The `run: |` body of the step named `name`, dedented. */
function stepScript(workflowText, name) {
  const lines = workflowText.split('\n');
  const at = lines.findIndex((l) => l.trim() === `- name: ${name}` || l.trim() === `name: ${name}`);
  assert.ok(at >= 0, `step "${name}" not found`);
  const stepCol = lines[at].indexOf('-') >= 0 && lines[at].trim().startsWith('-') ? lines[at].indexOf('-') : lines[at].search(/\S/) - 2;
  let i = at + 1;
  while (i < lines.length && !/^\s*run:\s*\|/.test(lines[i])) {
    assert.ok(lines[i].trim() === '' || lines[i].search(/\S/) > stepCol, `step "${name}" has no run: | body`);
    i += 1;
  }
  const runCol = lines[i].search(/\S/);
  const body = [];
  for (i += 1; i < lines.length; i += 1) {
    if (lines[i].trim() !== '' && lines[i].search(/\S/) <= runCol) break;
    body.push(lines[i]);
  }
  const indent = Math.min(...body.filter((l) => l.trim()).map((l) => l.search(/\S/)));
  return body.map((l) => l.slice(indent)).join('\n');
}

/** The `if:` of a job, as one string. */
function jobIf(workflowText, job) {
  const lines = workflowText.split('\n');
  const at = lines.findIndex((l) => l === `  ${job}:`);
  assert.ok(at >= 0, `job ${job} not found`);
  for (let i = at + 1; i < lines.length && !/^ {2}\S/.test(lines[i]); i += 1) {
    const m = /^ {4}if:\s*(.*)$/.exec(lines[i]);
    if (m) return m[1];
  }
  return '';
}

/** A directory with a fake `gh` that answers from a JSON table and logs calls. */
function fakeGh(dir, answers) {
  const bin = join(dir, 'bin');
  mkdirSync(bin, { recursive: true });
  writeFileSync(join(dir, 'gh-answers.json'), JSON.stringify(answers));
  writeFileSync(join(bin, 'gh'), `#!/usr/bin/env node
const fs = require('fs');
const args = process.argv.slice(2);
fs.appendFileSync(${JSON.stringify(join(dir, 'gh-calls.log'))}, args.join(' ') + '\\n');
const answers = JSON.parse(fs.readFileSync(${JSON.stringify(join(dir, 'gh-answers.json'))}, 'utf8'));
for (const [prefix, reply] of Object.entries(answers)) {
  if (args.join(' ').startsWith(prefix)) {
    if (reply.stdout) process.stdout.write(reply.stdout);
    process.exit(reply.code || 0);
  }
}
process.exit(1);
`);
  chmodSync(join(bin, 'gh'), 0o755);
  return bin;
}

function gitRepo(dir) {
  mkdirSync(dir, { recursive: true });
  const git = (...args) => execFileSync('git', ['-C', dir, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  git('init', '-q', '-b', 'main');
  git('config', 'user.email', 'test@example.invalid');
  git('config', 'user.name', 'test');
  git('config', 'commit.gpgsign', 'false');
  git('config', 'tag.gpgsign', 'false');
  return git;
}

function runBash(script, { cwd, env, path }) {
  return spawnSync('bash', ['-e', '-c', script], {
    cwd,
    encoding: 'utf8',
    env: { PATH: `${path}:${process.env.PATH}`, HOME: cwd, ...env },
  });
}

// ---------------------------------------------------------------- B1 / SEC-044

test('docker-publish builds only the tag it runs from: no ref input, no checkout ref, tags-only gate', () => {
  const wf = read('.github/workflows/docker-publish.yml');
  assert.doesNotMatch(wf, /inputs\./, 'no dispatch input may reach the job');
  assert.doesNotMatch(wf, /^\s+ref:/m, 'the checkout must build github.sha, not a named ref');
  const gate = jobIf(wf, 'build-and-push');
  assert.match(gate, /startsWith\(github\.ref, 'refs\/tags\/v'\)/);
  assert.doesNotMatch(gate, /refs\/heads\/main/, 'a dispatch from main used to pass the gate and build any ref');
});

test('docker-publish refuses a branch named like a version, and a tag that is not on main', () => {
  const script = stepScript(read('.github/workflows/docker-publish.yml'), 'Resolve tag');
  const dir = mkdtempSync(join(tmpdir(), 'sdn-docker-gate-'));
  try {
    const git = gitRepo(join(dir, 'repo'));
    writeFileSync(join(dir, 'repo', 'f'), 'x');
    git('add', 'f');
    git('commit', '-q', '-m', 'x');
    const sha = git('rev-parse', 'HEAD');
    const out = join(dir, 'out');
    const run = (env, answers) => {
      writeFileSync(out, '');
      return runBash(script, {
        cwd: join(dir, 'repo'),
        path: fakeGh(dir, answers),
        env: { GITHUB_OUTPUT: out, GITHUB_REPOSITORY: 'o/r', GITHUB_SHA: sha, ...env },
      });
    };
    const onMain = { 'api repos/o/r/compare/refs/heads/main...': { stdout: 'behind\n' } };
    // The audit's bypass: a branch called v1.0.6-beta.1.
    const branch = run({ REF: 'refs/heads/v1.0.6-beta.1', REF_TYPE: 'branch' }, onMain);
    assert.notEqual(branch.status, 0);
    assert.equal(readFileSync(out, 'utf8'), '', 'nothing may be resolved for a branch');
    assert.notEqual(run({ REF: 'refs/heads/main', REF_TYPE: 'branch' }, onMain).status, 0);
    assert.notEqual(run({ REF: 'refs/tags/x', REF_TYPE: 'tag' }, onMain).status, 0);
    assert.notEqual(run({ REF: 'refs/tags/vnext', REF_TYPE: 'tag' }, onMain).status, 0);
    // A real tag whose commit is not on main.
    const offMain = run({ REF: 'refs/tags/v1.0.6-beta.1', REF_TYPE: 'tag' },
      { 'api repos/o/r/compare/refs/heads/main...': { stdout: 'ahead\n' } });
    assert.notEqual(offMain.status, 0);
    assert.match(offMain.stderr, /not on main/);
    assert.notEqual(run({ REF: 'refs/tags/v1.0.6-beta.1', REF_TYPE: 'tag' }, {}).status, 0, 'an unanswerable on-main check fails closed');
    // The legitimate case.
    const ok = run({ REF: 'refs/tags/v1.0.6-beta.1', REF_TYPE: 'tag' }, onMain);
    assert.equal(ok.status, 0, ok.stderr);
    assert.match(readFileSync(out, 'utf8'), /^tag=v1\.0\.6-beta\.1\nversion=1\.0\.6-beta\.1\nchannel=beta\n$/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

// ---------------------------------------------------------------- S4

test('release-deploy has no version input: it releases github.ref_name, from v* tags only', () => {
  const wf = read('.github/workflows/release-deploy.yml');
  assert.doesNotMatch(wf, /inputs\./);
  assert.match(wf, /^ {2}VERSION: \$\{\{ github\.ref_name \}\}$/m);
  for (const job of ['preflight', 'docker', 'release']) {
    const gate = jobIf(wf, job);
    assert.match(gate, /startsWith\(github\.ref, 'refs\/tags\/v'\)/, job);
    assert.doesNotMatch(gate, /refs\/heads\/main/, job);
  }
});

test('release-deploy preflight refuses main, a non-semver label and a tag off main', () => {
  const script = stepScript(read('.github/workflows/release-deploy.yml'), 'The run is a v<semver> tag on main');
  const dir = mkdtempSync(join(tmpdir(), 'sdn-release-gate-'));
  try {
    const git = gitRepo(join(dir, 'repo'));
    writeFileSync(join(dir, 'repo', 'f'), 'x');
    git('add', 'f');
    git('commit', '-q', '-m', 'x');
    const sha = git('rev-parse', 'HEAD');
    const onMain = { 'api repos/o/r/compare/refs/heads/main...': { stdout: 'identical\n' } };
    const run = (env, answers = onMain) => runBash(script, {
      cwd: join(dir, 'repo'),
      path: fakeGh(dir, answers),
      env: { GITHUB_REPOSITORY: 'o/r', GITHUB_SHA: sha, ...env },
    });
    // The audit's case: dispatched from main, labelled `beta`/`latest`/an old version.
    for (const VERSION of ['beta', 'latest', 'v1.0.0']) {
      assert.notEqual(run({ REF: 'refs/heads/main', REF_TYPE: 'branch', VERSION }).status, 0, VERSION);
    }
    assert.notEqual(run({ REF: 'refs/tags/latest', REF_TYPE: 'tag', VERSION: 'latest' }).status, 0);
    assert.notEqual(run({ REF: 'refs/tags/v2.1.0', REF_TYPE: 'tag', VERSION: 'v2.1.0' },
      { 'api repos/o/r/compare/refs/heads/main...': { stdout: 'diverged\n' } }).status, 0);
    const ok = run({ REF: 'refs/tags/v2.1.0', REF_TYPE: 'tag', VERSION: 'v2.1.0' });
    assert.equal(ok.status, 0, ok.stderr);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('release-deploy creates a draft and never replaces a published release\'s assets', () => {
  const wf = read('.github/workflows/release-deploy.yml');
  const script = stepScript(wf, 'Publish the GitHub release as a draft');
  assert.match(script, /gh release create "\$\{VERSION\}"[^\n]*--draft/);
  const dir = mkdtempSync(join(tmpdir(), 'sdn-release-pub-'));
  try {
    mkdirSync(join(dir, 'dist', 'release'), { recursive: true });
    writeFileSync(join(dir, 'dist', 'release', 'spacedatanetwork-checksums.txt'), 'x');
    const run = (answers) => {
      rmSync(join(dir, 'gh-calls.log'), { force: true });
      const r = runBash(script, { cwd: dir, path: fakeGh(dir, answers), env: { VERSION: 'v2.1.0', GITHUB_REPOSITORY: 'o/r' } });
      const calls = existsSync(join(dir, 'gh-calls.log')) ? readFileSync(join(dir, 'gh-calls.log'), 'utf8') : '';
      return { ...r, calls };
    };
    const published = run({ 'release view': { stdout: 'false\n' }, 'release upload': {} });
    assert.notEqual(published.status, 0);
    assert.doesNotMatch(published.calls, /release upload/, 'a published release must not be clobbered');
    const draft = run({ 'release view': { stdout: 'true\n' }, 'release upload': {} });
    assert.equal(draft.status, 0, draft.stderr);
    assert.match(draft.calls, /release upload v2\.1\.0/);
    const fresh = run({ 'release view': { code: 1 }, 'release create': {}, 'release upload': {} });
    assert.equal(fresh.status, 0, fresh.stderr);
    assert.match(fresh.calls, /release create v2\.1\.0 .*--verify-tag --draft/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

// ---------------------------------------------------------------- S5

const KEY_BLOCK = /# BEGIN SDN RELEASE SIGNING KEY\n[\s\S]*?\n# END SDN RELEASE SIGNING KEY/;
const withKey = (text, pem) => text.replace(KEY_BLOCK,
  `# BEGIN SDN RELEASE SIGNING KEY\nSDN_RELEASE_SIGNING_KEY_PEM='${pem.trim()}'\n# END SDN RELEASE SIGNING KEY`);

function signingWorld() {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-sign-'));
  const repo = join(dir, 'repo');
  const git = gitRepo(repo);
  mkdirSync(join(repo, 'deployment', 'release'), { recursive: true });
  mkdirSync(join(repo, 'scripts'), { recursive: true });
  copyFileSync(join(repoRoot, 'deployment/release/sign-release-checksums.sh'), join(repo, 'deployment/release/sign-release-checksums.sh'));
  const owner = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const attacker = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const pub = (k) => k.publicKey.export({ type: 'spki', format: 'pem' });
  const priv = (k, name) => {
    const p = join(dir, name);
    writeFileSync(p, k.privateKey.export({ type: 'pkcs8', format: 'pem' }), { mode: 0o600 });
    return p;
  };
  const installer = withKey(read('scripts/install.sh'), pub(owner));
  writeFileSync(join(repo, 'scripts/install.sh'), installer);
  git('add', '-A');
  git('commit', '-q', '-m', 'release');
  git('tag', 'v1.2.3');
  git('update-ref', 'refs/remotes/origin/main', 'HEAD');
  const draft = (name, { installerText = installer, list } = {}) => {
    const d = join(dir, name);
    mkdirSync(d, { recursive: true });
    writeFileSync(join(d, 'install.sh'), installerText);
    const archive = 'spacedatanetwork-1.2.3-linux-amd64.tar.gz';
    writeFileSync(join(d, archive), 'archive');
    writeFileSync(join(d, 'spacedatanetwork-checksums.txt'), list
      ?? `${sha256('archive')}  ${archive}\n${sha256(installerText)}  install.sh\n`);
    return d;
  };
  const sign = (...args) => spawnSync('bash', [join(repo, 'deployment/release/sign-release-checksums.sh'), 'sign', ...args], { encoding: 'utf8' });
  return { dir, repo, git, owner, attacker, pub, priv, installer, draft, sign };
}

test('sign refuses a draft whose install.sh is not the reviewed one at the tag', () => {
  const w = signingWorld();
  try {
    const key = w.priv(w.owner, 'owner.pem');
    // CI swapped in an installer that trusts the attacker's key (and lists it
    // at its true digest, so the list alone looks consistent).
    const swapped = withKey(w.installer, w.pub(w.attacker));
    const d = w.draft('swapped', { installerText: swapped });
    const r = w.sign(key, 'v1.2.3', d);
    assert.notEqual(r.status, 0);
    assert.match(r.stderr, /not scripts\/install\.sh at v1\.2\.3/);
    assert.ok(!existsSync(join(d, 'spacedatanetwork-checksums.txt.sig')), 'nothing may be signed');
    // One appended byte is enough.
    const d2 = w.draft('appended', { installerText: `${w.installer}\n` });
    assert.notEqual(w.sign(key, 'v1.2.3', d2).status, 0);
  } finally {
    rmSync(w.dir, { recursive: true, force: true });
  }
});

test('sign refuses when the tag\'s installer embeds a different key than the one signing', () => {
  const w = signingWorld();
  try {
    const r = w.sign(w.priv(w.attacker, 'other.pem'), 'v1.2.3', w.draft('d'));
    assert.notEqual(r.status, 0);
    assert.match(r.stderr, /different public key/);
  } finally {
    rmSync(w.dir, { recursive: true, force: true });
  }
});

test('sign refuses a list that misstates install.sh, names another version, or a tag off main', () => {
  const w = signingWorld();
  try {
    const key = w.priv(w.owner, 'owner.pem');
    const wrongDigest = w.draft('digest', { list: `${sha256('archive')}  spacedatanetwork-1.2.3-linux-amd64.tar.gz\n${'0'.repeat(64)}  install.sh\n` });
    assert.match(w.sign(key, 'v1.2.3', wrongDigest).stderr, /install\.sh digest/);
    const otherVersion = w.draft('other', {
      list: `${sha256('archive')}  spacedatanetwork-9.9.9-linux-amd64.tar.gz\n${sha256(w.installer)}  install.sh\n`,
    });
    assert.match(w.sign(key, 'v1.2.3', otherVersion).stderr, /another version/);
    const noInstaller = w.draft('noinst', { list: `${sha256('archive')}  spacedatanetwork-1.2.3-linux-amd64.tar.gz\n` });
    assert.notEqual(w.sign(key, 'v1.2.3', noInstaller).status, 0);
    const badArchive = w.draft('badarchive', {
      list: `${'1'.repeat(64)}  spacedatanetwork-1.2.3-linux-amd64.tar.gz\n${sha256(w.installer)}  install.sh\n`,
    });
    assert.match(w.sign(key, 'v1.2.3', badArchive).stderr, /does not match its listed digest/);
    // The tag moves to a commit main does not contain.
    writeFileSync(join(w.repo, 'extra'), 'x');
    w.git('add', 'extra');
    w.git('commit', '-q', '-m', 'unreviewed');
    w.git('tag', 'v1.2.4');
    const offMain = w.draft('offmain', {
      list: `${sha256('archive')}  spacedatanetwork-1.2.4-linux-amd64.tar.gz\n${sha256(w.installer)}  install.sh\n`,
    });
    assert.match(w.sign(key, 'v1.2.4', offMain).stderr, /not on origin\/main/);
    assert.notEqual(w.sign(key, 'main', w.draft('notag')).status, 0);
    // The old two-argument form signed whatever list it was handed.
    const legacy = w.draft('legacy');
    assert.notEqual(w.sign(key, join(legacy, 'spacedatanetwork-checksums.txt')).status, 0);
    assert.ok(!existsSync(join(legacy, 'spacedatanetwork-checksums.txt.sig')));
  } finally {
    rmSync(w.dir, { recursive: true, force: true });
  }
});

test('sign signs the reviewed draft, and the signature verifies against the tag\'s key', () => {
  const w = signingWorld();
  try {
    const d = w.draft('good');
    const r = w.sign(w.priv(w.owner, 'owner.pem'), 'v1.2.3', d);
    assert.equal(r.status, 0, r.stderr);
    const sig = join(d, 'spacedatanetwork-checksums.txt.sig');
    assert.ok(existsSync(sig));
    const pubFile = join(w.dir, 'owner.pub.pem');
    writeFileSync(pubFile, w.pub(w.owner));
    execFileSync('openssl', ['dgst', '-sha256', '-verify', pubFile, '-signature', sig, join(d, 'spacedatanetwork-checksums.txt')]);
  } finally {
    rmSync(w.dir, { recursive: true, force: true });
  }
});

// ---------------------------------------------------------------- S2

function gpgKeys(dir) {
  const home = join(dir, 'gnupg');
  mkdirSync(home, { mode: 0o700 });
  const gpg = (...args) => execFileSync('gpg', ['--homedir', home, '--batch', '--quiet', ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  const make = (uid) => {
    gpg('--passphrase', '', '--pinentry-mode', 'loopback', '--quick-gen-key', uid, 'ed25519', 'sign', 'never');
    const colons = gpg('--with-colons', '--list-keys', uid);
    const lines = colons.split('\n');
    const pubAt = lines.findIndex((l) => l.startsWith('pub:'));
    const fpr = lines.slice(pubAt).find((l) => l.startsWith('fpr:')).split(':')[9];
    const file = join(dir, `${fpr}.asc`);
    writeFileSync(file, gpg('--armor', '--export', fpr));
    return { fpr, file };
  };
  return { make, gpgHome: home };
}

test('install-pinned-apt-key installs exactly the pinned key and refuses a file carrying extra keys', () => {
  const dir = mkdtempSync(join(tmpdir(), 'sdn-aptkey-'));
  try {
    const { make } = gpgKeys(dir);
    const real = make('Pinned Test Key <pinned@example.invalid>');
    const extra = make('Extra Test Key <extra@example.invalid>');
    const both = join(dir, 'both.asc');
    writeFileSync(both, readFileSync(real.file, 'utf8') + readFileSync(extra.file, 'utf8'));
    const script = join(repoRoot, 'scripts/install-pinned-apt-key.sh');
    const run = (file, fpr, out) => spawnSync('bash', [script, file, fpr, out], { encoding: 'utf8', env: { ...process.env, GNUPGHOME: join(dir, 'unused') } });

    // The verifier's bypass: the real key plus another one in the same file.
    const smuggled = run(both, real.fpr, join(dir, 'smuggled.gpg'));
    assert.notEqual(smuggled.status, 0);
    assert.match(smuggled.stderr, /2 primary keys/);
    assert.ok(!existsSync(join(dir, 'smuggled.gpg')));
    assert.notEqual(run(extra.file, real.fpr, join(dir, 'wrong.gpg')).status, 0, 'a different key is refused');
    assert.notEqual(run(join(dir, 'missing.asc'), real.fpr, join(dir, 'm.gpg')).status, 0);
    assert.notEqual(run(real.file, 'not-a-fingerprint', join(dir, 'n.gpg')).status, 0);

    const out = join(dir, 'ok.gpg');
    const ok = run(real.file, real.fpr.toLowerCase(), out);
    assert.equal(ok.status, 0, ok.stderr);
    const listing = execFileSync('gpg', ['--homedir', join(dir, 'gnupg'), '--batch', '--with-colons', '--import-options', 'show-only', '--import', out], { encoding: 'utf8' });
    const pubs = listing.split('\n').filter((l) => l.startsWith('pub:'));
    assert.equal(pubs.length, 1);
    assert.ok(listing.includes(`fpr:::::::::${real.fpr}:`));
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('the LLVM apt key goes through install-pinned-apt-key, never a whole-file dearmor', () => {
  for (const file of ['.github/workflows/ci.yml', '.github/workflows/beta-release-artifacts.yml']) {
    const wf = read(file);
    assert.match(wf, /scripts\/install-pinned-apt-key\.sh \/tmp\/llvm-snapshot\.gpg\.key "\$\{LLVM_APT_KEY_FINGERPRINT\}" \/etc\/apt\/trusted\.gpg\.d\/llvm\.gpg/, file);
    assert.doesNotMatch(wf, /gpg --dearmor[^\n]*llvm/, file);
  }
});

// ---------------------------------------------------------------- S7, N2, N4

test('the trufflehog scanner image is pinned by digest, govulncheck by version', () => {
  // The step's own lines only: from its uses: to the next list item.
  const wf = read('.github/workflows/encryption-tests.yml');
  const at = wf.indexOf('uses: trufflesecurity/trufflehog@');
  assert.ok(at >= 0);
  const step = wf.slice(at).split('\n').slice(1);
  const end = step.findIndex((l) => /^\s*- /.test(l) || /^\S/.test(l));
  assert.ok(step.slice(0, end).some((l) => /^ +version: 3\.99\.2@sha256:[0-9a-f]{64}$/.test(l)), 'trufflehog version pinned by digest');
  const security = read('.github/workflows/security.yml');
  assert.doesNotMatch(security, /@latest\b/);
  assert.match(security, /go install golang\.org\/x\/vuln\/cmd\/govulncheck@v\d+\.\d+\.\d+/);
});

test('the beta release runs from main only, before its write-scoped probe', () => {
  assert.equal(jobIf(read('.github/workflows/beta-release-artifacts.yml'), 'beta-version'), "github.ref == 'refs/heads/main'");
});

// ---------------------------------------------------------------- S1, N5

test('Pages deploys docs/ only from main, through the github-pages environment', () => {
  const wf = read('.github/workflows/pages.yml');
  assert.equal(jobIf(wf, 'deploy'), "github.ref == 'refs/heads/main'");
  assert.match(wf, /environment:\n\s+name: github-pages/);
  assert.match(wf, /uses: actions\/deploy-pages@[0-9a-f]{40}/);
  assert.doesNotMatch(wf, /pull_request/);
});

test('the Pages stub check passes an unprovisioned stub and refuses half-set pins', () => {
  const script = stepScript(read('.github/workflows/pages.yml'), "Check the install stub's pins");
  const stub = read('docs/install.sh');
  const dir = mkdtempSync(join(tmpdir(), 'sdn-pages-'));
  try {
    mkdirSync(join(dir, 'docs'));
    const run = (text) => {
      writeFileSync(join(dir, 'docs', 'install.sh'), text);
      return runBash(script, { cwd: dir, path: '/usr/bin', env: {} });
    };
    assert.equal(run(stub).status, 0, 'the committed stub (refuses to install) deploys');
    const pem = generateKeyPairSync('ec', { namedCurve: 'P-256' }).publicKey.export({ type: 'spki', format: 'pem' });
    const keyed = withKey(stub, pem);
    assert.equal(run(keyed).status, 0, 'key embedded, nothing pinned yet: still refuses, still deploys');
    const tagOnly = keyed.replace("SDN_INSTALLER_RELEASE_TAG=''", "SDN_INSTALLER_RELEASE_TAG='v1.2.3'");
    assert.notEqual(run(tagOnly).status, 0);
    const pinnedNoKey = stub.replace("SDN_INSTALLER_RELEASE_TAG=''", "SDN_INSTALLER_RELEASE_TAG='v1.2.3'")
      .replace("SDN_INSTALLER_SHA256=''", `SDN_INSTALLER_SHA256='${'a'.repeat(64)}'`);
    assert.notEqual(run(pinnedNoKey).status, 0);
    const pinned = keyed.replace("SDN_INSTALLER_RELEASE_TAG=''", "SDN_INSTALLER_RELEASE_TAG='v1.2.3'")
      .replace("SDN_INSTALLER_SHA256=''", `SDN_INSTALLER_SHA256='${'a'.repeat(64)}'`);
    assert.equal(run(pinned).status, 0);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('no public instruction pipes the disabled PowerShell stub into iex', () => {
  for (const file of ['README.md', 'docs/INSTALL.md', 'docs/index.html', 'docs/onboarding.html', 'scripts/install.sh']) {
    assert.doesNotMatch(read(file), /install\.ps1\s*\|\s*iex/i, file);
  }
});
