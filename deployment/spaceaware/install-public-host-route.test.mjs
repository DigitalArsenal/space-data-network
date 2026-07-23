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

const installerPath = new URL('./install-public-host-route.mjs', import.meta.url);
const deployScriptPath = new URL('../scripts/deploy.sh', import.meta.url);

const originalConfig = `map $http_upgrade $sdn_upgrade_backend {
    default http://127.0.0.1:5020;
    websocket http://127.0.0.1:18080;
}

server {
    listen 443 ssl http2;
    server_name spaceaware.io www.spaceaware.io sdn.spaceaware.io;

    location = /index.html {
        proxy_pass http://127.0.0.1:5020;
    }

    location ^~ /assets/ {
        proxy_pass http://127.0.0.1:5020;
    }

    location ^~ /TestData/ {
        proxy_pass http://127.0.0.1:5020;
    }

    location = / {
        proxy_pass $sdn_upgrade_backend;
    }

    location ^~ /api/module-delivery/ {
        proxy_pass https://127.0.0.1:18443;
    }

    location / {
        proxy_pass http://127.0.0.1:5020;
    }
}
`;

const previouslyInstalledSidecarRootConfig = `map $host $sdn_http_backend {
    default http://127.0.0.1:5020;
    sdn.spaceaware.io https://127.0.0.1:18443;
}

map $http_upgrade $sdn_upgrade_backend {
    default $sdn_http_backend;
    websocket http://127.0.0.1:18080;
}

server {
    listen 443 ssl http2;
    server_name spaceaware.io www.spaceaware.io sdn.spaceaware.io;

    location = /index.html {
        proxy_pass $sdn_http_backend;
    }

    location ^~ /assets/ {
        proxy_pass $sdn_http_backend;
    }

    location ^~ /TestData/ {
        proxy_pass $sdn_http_backend;
    }

    location = / {
        proxy_pass $sdn_upgrade_backend;
    }

    location ^~ /api/module-delivery/ {
        proxy_pass https://127.0.0.1:18443;
    }

    location / {
        proxy_pass $sdn_http_backend;
    }
}
`;

const consoleAssetPaths = [
  '/styles.css',
  '/app.js',
  '/module-harness.js',
  '/flatbuffers.js',
  '/fonts/chakra-400.woff2',
  '/fonts/chakra-500.woff2',
  '/fonts/chakra-600.woff2',
  '/fonts/chakra-700.woff2',
  '/fonts/plex-400.woff2',
  '/fonts/plex-500.woff2',
  '/fonts/plex-600.woff2',
];

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

async function makeFixture(t, {
  nginxExit = 0,
  reloadExit = 0,
  verifyExit = 0,
  mutateDuringNginx = false,
  flockBusy = false,
  initialConfig = originalConfig,
} = {}) {
  const root = await mkdtemp(join(tmpdir(), 'sdn-public-host-route-'));
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
  await writeFile(configPath, initialConfig, { mode: 0o640 });
  await writeFile(callLog, '');
  await writeFile(verifierPath, `
import fs from 'node:fs';
fs.appendFileSync(process.env.STUB_CALL_LOG, 'verify SDN public route\\n');
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

  return {
    root,
    configDir,
    configPath,
    callLog,
    async invoke(selectedBackupDir = backupDir) {
      return await run(process.execPath, [
        installerPath.pathname,
        '--config', configPath,
        '--backup-dir', selectedBackupDir,
        '--lock-path', lockPath,
        '--verify-script', verifierPath,
      ], {
        env: {
          ...process.env,
          PATH: bin + delimiter + process.env.PATH,
          STUB_CALL_LOG: callLog,
          STUB_CONFIG_PATH: configPath,
          STUB_CONCURRENT_CONFIG: '# concurrent operator replacement\n',
          STUB_VERIFY_EXIT: String(verifyExit),
        },
      });
    },
    backupDir,
    lockPath,
    verifierPath,
  };
}

test('installs host-aware SDN routing without changing the SpaceAware backend', async (t) => {
  const fixture = await makeFixture(t);
  const result = await fixture.invoke();
  assert.equal(result.code, 0, result.stderr);

  const installed = await readFile(fixture.configPath, 'utf8');
  assert.match(installed, /map \$host \$sdn_http_backend \{\s+default http:\/\/127\.0\.0\.1:5020;\s+sdn\.spaceaware\.io https:\/\/127\.0\.0\.1:18443;\s+\}/);
  assert.match(installed, /map \$http_upgrade \$sdn_upgrade_backend \{\s+default http:\/\/127\.0\.0\.1:5020;\s+websocket http:\/\/127\.0\.0\.1:18080;\s+\}/);
  assert.match(installed, /location = \/ \{\s+proxy_pass \$sdn_upgrade_backend;\s+\}/);
  assert.match(installed, /location \/ \{\s+proxy_pass \$sdn_http_backend;\s+\}/);
  assert.match(installed, /location = \/index\.html \{\s+proxy_pass http:\/\/127\.0\.0\.1:5020\/;\s+\}/);
  assert.match(installed, /location \^~ \/assets\/ \{\s+proxy_pass \$sdn_http_backend;\s+\}/);
  assert.match(installed, /location \^~ \/TestData\/ \{\s+proxy_pass \$sdn_http_backend;\s+\}/);
  assert.match(installed, /location \^~ \/api\/module-delivery\/ \{\s+proxy_pass https:\/\/127\.0\.0\.1:18443;\s+\}/);
  for (const path of consoleAssetPaths) {
    const escaped = path.replace(/[.*+?^${}()|[\]\\]/gu, '\\$&');
    assert.match(installed, new RegExp(`location = ${escaped} \\{\\s+proxy_pass http://127\\.0\\.0\\.1:5020;\\s+\\}`));
  }

  const calls = await readFile(fixture.callLog, 'utf8');
  assert.match(calls, /nginx -t/);
  assert.match(calls, /systemctl reload nginx/);
  assert.ok(calls.indexOf('systemctl reload nginx') < calls.indexOf('verify SDN public route'));

  const backups = (await readdir(fixture.backupDir)).filter((name) => name.startsWith('spaceaware.pre-sdn-public-route.'));
  assert.equal(backups.length, 1);
  assert.equal(await readFile(join(fixture.backupDir, backups[0]), 'utf8'), originalConfig);
  assert.equal(
    (await readdir(fixture.configDir)).some((name) => name.startsWith('spaceaware.pre-sdn-public-route.')),
    false,
  );
});

test('repairs the previously installed sidecar-root route without changing its fallback', async (t) => {
  const fixture = await makeFixture(t, { initialConfig: previouslyInstalledSidecarRootConfig });
  const result = await fixture.invoke();
  assert.equal(result.code, 0, result.stderr);

  const installed = await readFile(fixture.configPath, 'utf8');
  assert.match(installed, /map \$http_upgrade \$sdn_upgrade_backend \{\s+default http:\/\/127\.0\.0\.1:5020;\s+websocket http:\/\/127\.0\.0\.1:18080;\s+\}/);
  assert.match(installed, /location = \/ \{\s+proxy_pass \$sdn_upgrade_backend;\s+\}/);
  assert.match(installed, /location = \/index\.html \{\s+proxy_pass http:\/\/127\.0\.0\.1:5020\/;\s+\}/);
  assert.match(installed, /location \/ \{\s+proxy_pass \$sdn_http_backend;\s+\}/);
  for (const path of consoleAssetPaths) {
    assert.ok(installed.includes(`    location = ${path} {`), `missing restored console route ${path}`);
  }
  assert.equal(
    (installed.match(/location = \/styles\.css \{/gu) || []).length,
    1,
    'the repair must not duplicate console routes',
  );
});

test('an unchanged second run validates and reloads the desired config without another backup', async (t) => {
  const fixture = await makeFixture(t);
  const first = await fixture.invoke();
  assert.equal(first.code, 0, first.stderr);
  await writeFile(fixture.callLog, '');

  const second = await fixture.invoke();
  assert.equal(second.code, 0, second.stderr);
  assert.match(second.stdout, /already installed/i);
  const calls = await readFile(fixture.callLog, 'utf8');
  assert.match(calls, /nginx -t/);
  assert.match(calls, /systemctl reload nginx/);
  assert.equal((calls.match(/verify SDN public route/g) || []).length, 1);
  assert.ok(calls.indexOf('systemctl reload nginx') < calls.indexOf('verify SDN public route'));
  assert.equal((await readdir(fixture.backupDir)).length, 1);
});

test('rolls the original file back when nginx validation fails', async (t) => {
  const fixture = await makeFixture(t, { nginxExit: 1 });
  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /validation failed/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), originalConfig);

  const calls = await readFile(fixture.callLog, 'utf8');
  assert.equal((calls.match(/nginx -t/g) || []).length, 1);
  assert.doesNotMatch(calls, /systemctl reload nginx/);
});

test('restores the original file when nginx reload fails', async (t) => {
  const fixture = await makeFixture(t, { reloadExit: 1 });
  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /nginx reload and rollback failed/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), originalConfig);

  const calls = await readFile(fixture.callLog, 'utf8');
  assert.equal((calls.match(/nginx -t/g) || []).length, 2);
  assert.equal((calls.match(/systemctl reload nginx/g) || []).length, 2);
});

test('restores and reloads the original file when SDN public verification fails', async (t) => {
  const fixture = await makeFixture(t, { verifyExit: 23 });
  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /SDN public-host verification failed/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), originalConfig);

  const calls = await readFile(fixture.callLog, 'utf8');
  assert.equal((calls.match(/nginx -t/g) || []).length, 2);
  assert.equal((calls.match(/systemctl reload nginx/g) || []).length, 2);
  assert.equal((calls.match(/verify SDN public route/g) || []).length, 1);
});

test('does not overwrite a concurrent config edit while handling validation', async (t) => {
  const fixture = await makeFixture(t, { mutateDuringNginx: true });
  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /nginx validation and rollback failed/i);
  assert.match(result.stderr, /changed between validation and reload/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), '# concurrent operator replacement\n');

  const calls = await readFile(fixture.callLog, 'utf8');
  assert.match(calls, /nginx -t/);
  assert.doesNotMatch(calls, /systemctl reload nginx/);
});

test('refuses an unexpected config instead of making a partial edit', async (t) => {
  const fixture = await makeFixture(t);
  const unexpected = originalConfig.replace(
    'proxy_pass http://127.0.0.1:5020;',
    'proxy_pass http://127.0.0.1:5999;',
  );
  await writeFile(fixture.configPath, unexpected);

  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /unexpected nginx config/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), unexpected);
  assert.equal(await readFile(fixture.callLog, 'utf8'), '');
});

test('refuses a symlink config target', async (t) => {
  const fixture = await makeFixture(t);
  const realConfig = `${fixture.configPath}.real`;
  await rename(fixture.configPath, realConfig);
  await symlink(realConfig, fixture.configPath);

  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /refusing non-regular nginx config/i);
  assert.equal(await readFile(realConfig, 'utf8'), originalConfig);
  assert.equal(await readFile(fixture.callLog, 'utf8'), '');
});

test('refuses to place durable backups inside the Nginx include directory', async (t) => {
  const fixture = await makeFixture(t);
  const result = await fixture.invoke(join(fixture.configDir, 'backups'));
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /outside the Nginx include directory/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), originalConfig);
  assert.equal(await readFile(fixture.callLog, 'utf8'), '');
});

test('fails closed when another installer holds the production lock', async (t) => {
  const fixture = await makeFixture(t, { flockBusy: true });
  const result = await fixture.invoke();
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /installer lock is busy/i);
  assert.equal(await readFile(fixture.configPath, 'utf8'), originalConfig);
  assert.equal(await readFile(fixture.callLog, 'utf8'), '');
});

test('the SpaceAware deploy uses the reviewed installer only for the exact production config', async () => {
  const deploy = await readFile(deployScriptPath, 'utf8');
  assert.match(
    deploy,
    /configure_spaceaware_public_host_route\(\) \{[\s\S]*if ! is_spaceaware_config; then\s+return\s+fi[\s\S]*scp_cmd "\$\{DEPLOY_DIR\}\/spaceaware\/install-public-host-route\.mjs"[\s\S]*scp_cmd "\$\{DEPLOY_DIR\}\/spaceaware\/verify-spaceaware-public-host-route\.mjs"[\s\S]*node \/opt\/spacedatanetwork\/deployment\/spaceaware\/install-public-host-route\.mjs[\s\S]*--verify-script \/opt\/spacedatanetwork\/deployment\/spaceaware\/verify-spaceaware-public-host-route\.mjs/,
  );
  const routeFunction = deploy.slice(
    deploy.indexOf('configure_spaceaware_public_host_route() {'),
    deploy.indexOf('\nprepare_full_node_assets() {'),
  );
  assert.match(routeFunction, /ssh_cmd "\$ip" "set -euo pipefail/);
  assert.doesNotMatch(routeFunction, /restart space-data-network-module-delivery\.service \|\| true/);
  assert.doesNotMatch(routeFunction, /if systemctl cat space-data-network-module-delivery\.service/);
  const unitCheck = routeFunction.indexOf('systemctl cat space-data-network-module-delivery.service');
  const restart = routeFunction.indexOf('systemctl restart space-data-network-module-delivery.service');
  const active = routeFunction.indexOf('systemctl is-active --quiet space-data-network-module-delivery.service');
  const rootProbe = routeFunction.indexOf("curl -kfsS --connect-timeout 5 --max-time 15 https://127.0.0.1:18443/");
  const callbackProbe = routeFunction.indexOf('https://127.0.0.1:18443/wallet/callback');
  const installer = routeFunction.indexOf('node /opt/spacedatanetwork/deployment/spaceaware/install-public-host-route.mjs');
  assert.ok(unitCheck >= 0 && unitCheck < restart);
  assert.ok(restart < active && active < rootProbe);
  assert.ok(rootProbe < callbackProbe && callbackProbe < installer);
  assert.ok(routeFunction.includes('for attempt in \\$(seq 1 30); do'));
  assert.ok(routeFunction.includes('test "\\$sidecar_ready" = true'));
  assert.doesNotMatch(routeFunction, /curl[^\n]*\| grep -Fq/);
  assert.match(routeFunction, /curl[^\n]*\| grep -F 'sdn-node-console-v1' >\/dev\/null/);
  assert.match(routeFunction, /curl[^\n]*\| grep -F 'Completing wallet connection' >\/dev\/null/);
  assert.doesNotMatch(routeFunction, /--resolve sdn\.spaceaware\.io/);
  assert.doesNotMatch(deploy, /pre-sdn-wss-route/);
  assert.doesNotMatch(deploy, /text\.replace\(\s*'server_name spaceaware\.io/);
});
