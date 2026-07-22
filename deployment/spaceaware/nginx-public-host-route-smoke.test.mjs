import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { request } from 'node:https';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { transformConfig } from './install-spaceaware-public-host-route.mjs';

const nginxImage = 'nginx@sha256:f6daac2445b0ce70e64d77442ccf62839f3f1b4c24bf6746a857eff014e798c8';
const fixturePath = new URL('./fixtures/shared-public-host-after-sdn-route.nginx', import.meta.url);

function command(commandName, args, options = {}) {
  return spawnSync(commandName, args, { encoding: 'utf8', ...options });
}

function dockerSkipReason() {
  const info = command('docker', ['info']);
  if (info.error || info.status !== 0) return 'Docker daemon is unavailable';
  const image = command('docker', ['image', 'inspect', nginxImage]);
  if (image.status !== 0) return `pinned ${nginxImage} is not loaded`;
  return false;
}

async function edgeRequest(port, host, path, headers = {}, method = 'GET') {
  return await new Promise((resolve, reject) => {
    const req = request({
      hostname: '127.0.0.1',
      port,
      path,
      method,
      rejectUnauthorized: false,
      servername: host,
      headers: { Host: host, ...headers },
    }, (response) => {
      const chunks = [];
      response.on('data', (chunk) => chunks.push(chunk));
      response.once('end', () => resolve({
        status: response.statusCode,
        headers: response.headers,
        body: Buffer.concat(chunks).toString('utf8'),
      }));
    });
    req.setTimeout(3000, () => req.destroy(new Error('Nginx smoke request timed out')));
    req.once('error', reject);
    req.end();
  });
}

test('Nginx 1.24 executes the split Host, callback, terrain, and websocket routing contract', {
  skip: dockerSkipReason(),
  timeout: 30_000,
}, async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'spaceaware-nginx-smoke-'));
  const container = `spaceaware-nginx-smoke-${randomUUID()}`;
  t.after(async () => {
    command('docker', ['rm', '--force', container]);
    await rm(root, { recursive: true, force: true });
  });
  await mkdir(join(root, 'cli-bundle'));
  const certificate = command('openssl', [
    'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
    '-subj', '/CN=spaceaware.io',
    '-keyout', join(root, 'origin.key'),
    '-out', join(root, 'origin.crt'),
  ]);
  assert.equal(certificate.status, 0, certificate.stderr);

  const source = await readFile(fixturePath, 'utf8');
  const routed = transformConfig(source)
    .replaceAll('/etc/spacedatanetwork/tls/origin.crt', '/work/origin.crt')
    .replaceAll('/etc/spacedatanetwork/tls/origin.key', '/work/origin.key')
    .replaceAll('/var/www/sdn-updates/cli-bundle/', '/work/cli-bundle/');
  const config = `
worker_processes 1;
pid /tmp/nginx.pid;
error_log stderr notice;

events { worker_connections 256; }

http {
    access_log off;
    proxy_connect_timeout 2s;
    proxy_read_timeout 2s;

${routed}

    server {
        listen 5010;
        default_type text/plain;
        location = /wallet/callback/index.html { return 200 "spaceaware-callback"; }
        location / { add_header X-Mock-Backend spaceaware-http always; return 200 "spaceaware-http:$uri"; }
    }
    server {
        listen 8080;
        default_type text/plain;
        location / { add_header X-Mock-Backend spaceaware-websocket always; return 200 "spaceaware-websocket:$uri"; }
    }
    server {
        listen 8081;
        default_type text/plain;
        location / { add_header X-Mock-Backend terrain always; return 200 "terrain:$uri"; }
    }
    server {
        listen 18443 ssl;
        ssl_certificate /work/origin.crt;
        ssl_certificate_key /work/origin.key;
        default_type text/plain;
        location / { add_header X-Mock-Backend sdn-http always; return 200 "sdn-http:$uri"; }
    }
    server {
        listen 18080;
        default_type text/plain;
        location / { add_header X-Mock-Backend sdn-websocket always; return 200 "sdn-websocket:$uri"; }
    }
}
`;
  await writeFile(join(root, 'nginx.conf'), config);

  const started = command('docker', [
    'run', '--detach', '--name', container,
    '--publish', '127.0.0.1::443',
    '--mount', `type=bind,source=${root},target=/work,readonly`,
    nginxImage,
    'nginx', '-c', '/work/nginx.conf', '-g', 'daemon off;',
  ]);
  assert.equal(started.status, 0, started.stderr);
  const mapping = command('docker', ['port', container, '443/tcp']);
  assert.equal(mapping.status, 0, mapping.stderr);
  const port = Number(mapping.stdout.trim().match(/:([0-9]+)$/u)?.[1]);
  assert.ok(Number.isInteger(port) && port > 0, mapping.stdout);

  let ready = false;
  for (let attempt = 0; attempt < 30; attempt += 1) {
    try {
      await edgeRequest(port, 'spaceaware.io', '/');
      ready = true;
      break;
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }
  if (!ready) {
    const logs = command('docker', ['logs', container]);
    assert.fail(`Nginx smoke container was not ready:\n${logs.stdout}\n${logs.stderr}`);
  }

  let response = await edgeRequest(port, 'spaceaware.io', '/');
  assert.equal(response.body, 'spaceaware-http:/');
  assert.equal(response.headers['x-mock-backend'], 'spaceaware-http');
  response = await edgeRequest(port, 'www.spaceaware.io', '/');
  assert.equal(response.headers['x-mock-backend'], 'spaceaware-http');

  response = await edgeRequest(port, 'spaceaware.io', '/', { Upgrade: 'websocket', Connection: 'close' });
  assert.equal(response.headers['x-mock-backend'], 'spaceaware-http');
  response = await edgeRequest(port, 'spaceaware.io', '/', { Upgrade: 'websocket', Connection: 'notupgrade' });
  assert.equal(response.headers['x-mock-backend'], 'spaceaware-http');
  response = await edgeRequest(port, 'spaceaware.io', '/', { Upgrade: 'websocket', Connection: 'keep-alive, Upgrade' });
  assert.equal(response.headers['x-mock-backend'], 'spaceaware-websocket');
  response = await edgeRequest(port, 'spaceaware.io', `/p2p/peer`, { Upgrade: 'websocket', Connection: 'Upgrade' });
  assert.equal(response.headers['x-mock-backend'], 'spaceaware-websocket');

  response = await edgeRequest(port, 'spaceaware.io', '/terrain/__terrain-cache/health');
  assert.equal(response.body, 'terrain:/__terrain-cache/health');
  response = await edgeRequest(port, 'spaceaware.io', '/ipfs/terrain/tile.bin');
  assert.equal(response.body, 'terrain:/tile.bin');
  response = await edgeRequest(port, 'spaceaware.io', '/api/v1/data/health');
  assert.equal(response.headers['x-mock-backend'], 'spaceaware-http');

  response = await edgeRequest(port, 'spaceaware.io', '/wallet/callback');
  assert.equal(response.status, 200);
  assert.equal(response.body, 'spaceaware-callback');
  assert.equal(response.headers['cache-control'], 'no-store');
  response = await edgeRequest(port, 'spaceaware.io', '/wallet/callback', {}, 'POST');
  assert.equal(response.status, 405);
  assert.equal(response.headers.allow, 'GET, HEAD');

  response = await edgeRequest(port, 'sdn.spaceaware.io', '/');
  assert.equal(response.headers['x-mock-backend'], 'sdn-http');
  response = await edgeRequest(port, 'sdn.spaceaware.io', '/p2p/peer', { Upgrade: 'websocket' });
  assert.equal(response.headers['x-mock-backend'], 'sdn-websocket');
});
