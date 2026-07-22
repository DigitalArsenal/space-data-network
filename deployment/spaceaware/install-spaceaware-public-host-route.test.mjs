import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import {
  chmod,
  mkdtemp,
  mkdir,
  readFile,
  readdir,
  rename,
  rm,
  symlink,
  writeFile,
} from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { delimiter, join } from 'node:path';
import { test } from 'node:test';

const installerPath = new URL('./install-spaceaware-public-host-route.mjs', import.meta.url);
const sdnInstallerPath = new URL('./install-public-host-route.mjs', import.meta.url);
const readinessPath = new URL('./cutover-spaceaware-public-host-route.sh', import.meta.url);
const deployScriptPath = new URL('../scripts/deploy.sh', import.meta.url);
const sourceFixturePath = new URL('./fixtures/shared-public-host-after-sdn-route.nginx', import.meta.url);
const sourceConfig = await readFile(sourceFixturePath, 'utf8');

async function run(command, args, options) {
  return await new Promise((resolve, reject) => {
    const child = spawn(command, args, options);
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => { stdout += chunk; });
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.on('error', reject);
    child.on('close', (code, signal) => resolve({ code, signal, stdout, stderr }));
  });
}

function serverBlock(config, marker) {
  const markerIndex = config.indexOf(marker);
  assert.notEqual(markerIndex, -1, `missing ${marker}`);
  const start = config.lastIndexOf('\nserver {', markerIndex) + 1;
  const next = config.indexOf('\nserver {', markerIndex);
  return config.slice(start, next === -1 ? undefined : next);
}

async function makeFixture(t, {
  nginxExit = 0,
  reloadExit = 0,
  verifyExit = 0,
  mutateDuringNginx = false,
  flockBusy = false,
} = {}) {
  const root = await mkdtemp(join(tmpdir(), 'spaceaware-public-host-route-'));
  t.after(async () => rm(root, { recursive: true, force: true }));
  const bin = join(root, 'bin');
  const configDir = join(root, 'sites-enabled');
  const configPath = join(configDir, 'spaceaware');
  const backupDir = join(root, 'backups');
  const lockPath = join(root, 'route.lock');
  const callLog = join(root, 'calls.jsonl');
  const verifierPath = join(root, 'verify-route.mjs');
  await mkdir(bin);
  await mkdir(configDir);
  await writeFile(configPath, sourceConfig, { mode: 0o640 });
  await writeFile(callLog, '');
  await writeFile(verifierPath, `
import fs from 'node:fs';
fs.appendFileSync(process.env.STUB_CALL_LOG, 'verify public route\\n');
process.exitCode = Number(process.env.STUB_VERIFY_EXIT || '0');
`);

  const stub = `#!/bin/sh
case "$(basename "$0")" in
  flock)
    if [ "${flockBusy ? '1' : '0'}" = 1 ]; then exit 75; fi
    shift 5
    exec "$@"
    ;;
  nginx)
    printf '%s\\n' "$0 $*" >> "$STUB_CALL_LOG"
    if [ "${mutateDuringNginx ? '1' : '0'}" = 1 ]; then
      printf '%s' "$STUB_CONCURRENT_CONFIG" > "$STUB_CONFIG_PATH"
    fi
    exit "${nginxExit}"
    ;;
  systemctl)
    printf '%s\\n' "$0 $*" >> "$STUB_CALL_LOG"
    exit "${reloadExit}"
    ;;
esac
exit 127
`;
  for (const name of ['flock', 'nginx', 'systemctl']) {
    const path = join(bin, name);
    await writeFile(path, stub);
    await chmod(path, 0o755);
  }

  const invoke = async (selectedInstaller = installerPath, selectedBackupDir = backupDir) => {
    const args = [
      selectedInstaller.pathname,
      '--config', configPath,
      '--backup-dir', selectedBackupDir,
      '--lock-path', lockPath,
    ];
    if (selectedInstaller.pathname === installerPath.pathname) {
      args.push('--verify-script', verifierPath);
    }
    return await run(process.execPath, args, {
      env: {
        ...process.env,
        PATH: bin + delimiter + process.env.PATH,
        STUB_CALL_LOG: callLog,
        STUB_CONFIG_PATH: configPath,
        STUB_CONCURRENT_CONFIG: '# concurrent operator replacement\n',
        STUB_VERIFY_EXIT: String(verifyExit),
      },
    });
  };

  return { root, bin, configDir, configPath, backupDir, lockPath, callLog, verifierPath, invoke };
}

test('splits the public hosts and applies the signed SpaceAware route contract', async (t) => {
  const fixture = await makeFixture(t);
  const result = await fixture.invoke();
  assert.equal(result.code, 0, result.stderr);

  const installed = await readFile(fixture.configPath, 'utf8');
  assert.doesNotMatch(installed, /server_name spaceaware\.io www\.spaceaware\.io sdn\.spaceaware\.io;/);
  assert.equal((installed.match(/server_name spaceaware\.io www\.spaceaware\.io;/g) || []).length, 1);
  assert.equal((installed.match(/server_name sdn\.spaceaware\.io;/g) || []).length, 1);
  assert.match(installed, /map "\$http_connection:\$http_upgrade" \$spaceaware_upgrade_backend \{\s+default http:\/\/127\.0\.0\.1:5010;\s+~\*\(\^\|\.\*,\\s\*\)upgrade\(\\s\*,\[\^:\]\*\)\?:websocket\$ http:\/\/127\.0\.0\.1:8080;\s+\}/);

  const spaceaware = serverBlock(installed, '# spaceaware-public-host-route: spaceaware-v1');
  assert.doesNotMatch(spaceaware, /127\.0\.0\.1:(?:5020|18080|18443)/);
  assert.match(spaceaware, /location = \/ \{[\s\S]*?proxy_pass \$spaceaware_upgrade_backend;/);
  assert.match(spaceaware, /location \^~ \/p2p\/ \{[\s\S]*?proxy_pass \$spaceaware_upgrade_backend;/);
  assert.match(spaceaware, /location \^~ \/api\/ \{[\s\S]*?proxy_pass http:\/\/127\.0\.0\.1:5010;/);
  assert.match(spaceaware, /location \^~ \/diag\/ \{[\s\S]*?proxy_pass http:\/\/127\.0\.0\.1:5010;/);
  assert.match(spaceaware, /location \^~ \/ipfs-api\/v0[\s\S]*?rewrite \^\/ipfs-api\/v0\(\.\*\)\$ \/api\/v0\$1 break;[\s\S]*?proxy_pass http:\/\/127\.0\.0\.1:5010;/);
  assert.match(spaceaware, /location \^~ \/terrain\/[\s\S]*?rewrite \^\/terrain\/\(\.\*\)\$ \/\$1 break;[\s\S]*?proxy_pass http:\/\/127\.0\.0\.1:8081;/);
  assert.match(spaceaware, /location \^~ \/ipfs\/terrain\/[\s\S]*?rewrite \^\/ipfs\/terrain\/\(\.\*\)\$ \/\$1 break;[\s\S]*?proxy_pass http:\/\/127\.0\.0\.1:8081;/);
  assert.match(spaceaware, /location ~ \^\/wallet\/callback\/\?\$[\s\S]*?if \(\$request_method !~ \^\(GET\|HEAD\)\$\)[\s\S]*?return 405;[\s\S]*?rewrite \^ \/wallet\/callback\/index\.html break;[\s\S]*?Cache-Control "no-store"/);
  assert.match(spaceaware, /Content-Security-Policy \$spaceaware_wallet_csp always;/);
  assert.match(spaceaware, /Allow \$spaceaware_wallet_allow always;/);
  assert.match(spaceaware, /location \^~ \/asset-ipfs\/[\s\S]*?proxy_pass http:\/\/10\.132\.0\.3:8080\/ipfs\/[\s\S]*?max-age=31536000, immutable/);
  assert.match(spaceaware, /location \^~ \/ipfs\/[\s\S]*?proxy_pass http:\/\/127\.0\.0\.1:5010;[\s\S]*?max-age=31536000, immutable/);
  assert.match(spaceaware, /Content-Security-Policy "default-src 'self'; script-src 'self' 'wasm-unsafe-eval' https:\/\/static\.spacedatanetwork\.org;/);
  assert.match(spaceaware, /location \^~ \/orbpro- \{\s+return 404;/);

  const sdn = serverBlock(installed, '# spaceaware-public-host-route: sdn-v1');
  assert.doesNotMatch(sdn, /127\.0\.0\.1:(?:5010|8081)/);
  assert.match(sdn, /location = \/ \{\s+proxy_pass \$sdn_upgrade_backend;/);
  assert.match(sdn, /location \^~ \/p2p\/ \{[\s\S]*?proxy_pass \$sdn_upgrade_backend;/);
  assert.match(sdn, /location \/ \{\s+proxy_pass \$sdn_http_backend;/);
  assert.match(sdn, /location \^~ \/api\/module-delivery\/[\s\S]*?proxy_pass https:\/\/127\.0\.0\.1:18443;/);
  assert.match(sdn, /location \^~ \/asset-ipfs\/[\s\S]*?proxy_pass http:\/\/10\.132\.0\.3:8080\/ipfs\/[\s\S]*?max-age=31536000, immutable/);

  const calls = await readFile(fixture.callLog, 'utf8');
  assert.match(calls, /nginx -t/);
  assert.match(calls, /systemctl reload nginx/);
  assert.ok(calls.indexOf('systemctl reload nginx') < calls.indexOf('verify public route'));
  const backups = (await readdir(fixture.backupDir)).filter((name) => name.startsWith('spaceaware.pre-spaceaware-public-route.'));
  assert.equal(backups.length, 1);
  assert.equal(await readFile(join(fixture.backupDir, backups[0]), 'utf8'), sourceConfig);
  assert.equal((await readdir(fixture.configDir)).some((name) => name.includes('.pre-spaceaware-public-route.')), false);
});

test('an unchanged second cutover validates and reloads without another backup', async (t) => {
  const fixture = await makeFixture(t);
  assert.equal((await fixture.invoke()).code, 0);
  await writeFile(fixture.callLog, '');
  const second = await fixture.invoke();
  assert.equal(second.code, 0, second.stderr);
  assert.match(second.stdout, /already installed/i);
  assert.equal((await readdir(fixture.backupDir)).length, 1);
  const calls = await readFile(fixture.callLog, 'utf8');
  assert.match(calls, /systemctl reload nginx/);
  assert.match(calls, /verify public route/);
});

test('restores and reloads the original config when public-host verification fails', async (t) => {
  const fixture = await makeFixture(t, { verifyExit: 23 });
  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /public-host verification failed/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), sourceConfig);
  const calls = await readFile(fixture.callLog, 'utf8');
  assert.equal((calls.match(/nginx -t/g) || []).length, 2);
  assert.equal((calls.match(/systemctl reload nginx/g) || []).length, 2);
  assert.equal((calls.match(/verify public route/g) || []).length, 1);
});

test('the SDN route installer accepts and reloads the final split config', async (t) => {
  const fixture = await makeFixture(t);
  assert.equal((await fixture.invoke()).code, 0);
  await writeFile(fixture.callLog, '');
  const sdnRetry = await fixture.invoke(sdnInstallerPath);
  assert.equal(sdnRetry.code, 0, sdnRetry.stderr);
  assert.match(sdnRetry.stdout, /already installed/i);
  assert.match(await readFile(fixture.callLog, 'utf8'), /systemctl reload nginx/);
});

test('the SDN route installer rejects drift anywhere in the managed final split config', async (t) => {
  const fixture = await makeFixture(t);
  assert.equal((await fixture.invoke()).code, 0);
  const installed = await readFile(fixture.configPath, 'utf8');
  const drifted = installed.replace(
    '    location / {\n        proxy_pass http://127.0.0.1:5010;',
    '    location / {\n        proxy_pass http://127.0.0.1:5999;',
  );
  assert.notEqual(drifted, installed);
  await writeFile(fixture.configPath, drifted);
  const retry = await fixture.invoke(sdnInstallerPath);
  assert.notEqual(retry.code, 0);
  assert.match(retry.stderr, /unexpected nginx config/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), drifted);
});

test('rolls back when nginx validation fails', async (t) => {
  const fixture = await makeFixture(t, { nginxExit: 1 });
  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /validation failed/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), sourceConfig);
  assert.doesNotMatch(await readFile(fixture.callLog, 'utf8'), /systemctl reload nginx/);
});

test('restores the original config when nginx reload fails', async (t) => {
  const fixture = await makeFixture(t, { reloadExit: 1 });
  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /nginx reload and rollback failed/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), sourceConfig);
  const calls = await readFile(fixture.callLog, 'utf8');
  assert.equal((calls.match(/nginx -t/g) || []).length, 2);
  assert.equal((calls.match(/systemctl reload nginx/g) || []).length, 2);
});

test('does not overwrite a concurrent operator edit', async (t) => {
  const fixture = await makeFixture(t, { mutateDuringNginx: true });
  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /nginx validation and rollback failed/i);
  assert.match(result.stderr, /changed between validation and reload/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), '# concurrent operator replacement\n');
});

test('refuses unexpected or unsafe config and backup paths', async (t) => {
  const fixture = await makeFixture(t);
  const unexpected = sourceConfig.replace('proxy_pass http://127.0.0.1:5020;', 'proxy_pass http://127.0.0.1:5999;');
  await writeFile(fixture.configPath, unexpected);
  let result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /unexpected nginx config/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), unexpected);

  await writeFile(fixture.configPath, sourceConfig);
  result = await fixture.invoke(installerPath, join(fixture.configDir, 'backups'));
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /outside the Nginx include directory/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), sourceConfig);
});

test('refuses a symlink config target and a busy shared route lock', async (t) => {
  const fixture = await makeFixture(t);
  const realConfig = `${fixture.configPath}.real`;
  await rename(fixture.configPath, realConfig);
  await symlink(realConfig, fixture.configPath);
  let result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /refusing non-regular nginx config/i);
  assert.equal(await readFile(realConfig, 'utf8'), sourceConfig);

  const busy = await makeFixture(t, { flockBusy: true });
  result = await busy.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /installer lock is busy/i);
  assert.equal(await readFile(busy.configPath, 'utf8'), sourceConfig);
});

test('the remote cutover entrypoint gates the installer on units and loopback probes', async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'spaceaware-cutover-ready-'));
  t.after(async () => rm(root, { recursive: true, force: true }));
  const bin = join(root, 'bin');
  const callLog = join(root, 'calls');
  const installer = join(root, 'installer.mjs');
  const verifier = join(root, 'verifier.mjs');
  await mkdir(bin);
  await writeFile(callLog, '');
  await writeFile(installer, '// fixture\n');
  await writeFile(verifier, '// fixture\n');

  const systemctl = `#!/bin/sh
printf 'systemctl %s\\n' "$*" >> "$CUTOVER_CALL_LOG"
case "$1" in
  show) printf 'loaded\\n' ;;
  is-active) exit 0 ;;
  *) exit 91 ;;
esac
`;
  const node = `#!/bin/sh
printf 'node %s\\n' "$*" >> "$CUTOVER_CALL_LOG"
exit 0
`;
  for (const [name, source] of [['systemctl', systemctl], ['node', node]]) {
    await writeFile(join(bin, name), source);
    await chmod(join(bin, name), 0o755);
  }

  const result = await run('bash', [readinessPath.pathname], {
    env: {
      ...process.env,
      PATH: bin + delimiter + process.env.PATH,
      CUTOVER_CALL_LOG: callLog,
      SPACEAWARE_ROUTE_INSTALLER_PATH: installer,
      SPACEAWARE_ROUTE_VERIFIER_PATH: verifier,
      SPACEAWARE_CUTOVER_MAX_ATTEMPTS: '1',
    },
  });
  assert.equal(result.code, 0, result.stderr);
  const calls = await readFile(callLog, 'utf8');
  for (const unit of ['spaceaware-sdn.service', 'spaceaware-ipfs.service', 'spaceaware-ingest.service', 'spaceaware-terrain-cache.service']) {
    assert.match(calls, new RegExp(`systemctl show .* ${unit.replace('.', '\\.')}\\n`));
    assert.match(calls, new RegExp(`systemctl is-active --quiet ${unit.replace('.', '\\.')}\\n`));
  }
  assert.ok(calls.includes(`node ${verifier} --mode loopback\n`));
  assert.ok(calls.includes(`node ${installer} --verify-script ${verifier}\n`));
  assert.ok(calls.indexOf(`node ${verifier}`) < calls.indexOf(`node ${installer}`));
  assert.doesNotMatch(calls, /restart/);
});

test('the SpaceAware deploy command installs and invokes only the reviewed cutover entrypoint', async () => {
  const deploy = await readFile(deployScriptPath, 'utf8');
  assert.match(deploy, /cutover-spaceaware/);
  assert.match(deploy, /cutover_spaceaware_public_host_route\(\) \{/);
  const functionStart = deploy.indexOf('cutover_spaceaware_public_host_route() {');
  const functionEnd = deploy.indexOf('\nprepare_full_node_assets() {', functionStart);
  const cutover = deploy.slice(functionStart, functionEnd);
  assert.match(cutover, /if ! is_spaceaware_config; then[\s\S]*return 1/);
  assert.match(cutover, /scp_cmd "\$\{DEPLOY_DIR\}\/spaceaware\/install-spaceaware-public-host-route\.mjs"/);
  assert.match(cutover, /scp_cmd "\$\{DEPLOY_DIR\}\/spaceaware\/verify-spaceaware-public-host-route\.mjs"/);
  assert.match(cutover, /scp_cmd "\$\{DEPLOY_DIR\}\/spaceaware\/cutover-spaceaware-public-host-route\.sh"/);
  assert.match(cutover, /chown root:root[\s\S]*chmod 0755[\s\S]*cutover-spaceaware-public-host-route\.sh/);
  assert.doesNotMatch(cutover, /restart|\|\| true/);
});
