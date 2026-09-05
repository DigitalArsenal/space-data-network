import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { request } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { renderNginx } from './render-nginx.mjs';

const image = 'nginx@sha256:dc5069ad14f19660b141b21236140b91656bf89bbc3e2417c70ae650cd66104c';
const run = (args) => spawnSync('docker', args, { encoding: 'utf8', timeout: 20_000 });
function prerequisite() {
  const ready = run(['image', 'inspect', image]);
  if (ready.status === 0) return false;
  const reason = `Docker with pinned image ${image} is required`;
  if (process.env.SDN_REQUIRE_DOCKER_TESTS === '1') throw new Error(reason);
  return reason;
}
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
function get(port, path, { headers = {}, method = 'GET', body } = {}) {
  return new Promise((resolve, reject) => {
    const req = request({ host: '127.0.0.1', port, path, headers, method }, (res) => {
      const chunks = [];
      res.on('data', (chunk) => chunks.push(chunk));
      res.on('error', reject);
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks) }));
    });
    req.setTimeout(5000, () => req.destroy(new Error('Public origin request timed out')));
    req.on('error', reject);
    req.end(body);
  });
}

test('real Nginx public origin preserves bytes, caches conditionally and refuses private traffic', {
  skip: prerequisite(), timeout: 45_000,
}, async (t) => {
  const root = await mkdtemp(join(tmpdir(), 'sdn-public-origin-'));
  const name = `sdn-public-origin-${randomUUID()}`;
  let started = false;
  t.after(async () => {
    if (started) {
      const removed = run(['rm', '--force', name]);
      assert.equal(removed.status, 0, removed.stderr);
    }
    await rm(root, { recursive: true, force: true });
  });
  // Opaque transport fixtures: SDS decoding is tested by the module/client
  // suites. This proxy must preserve any byte sequence without re-encoding.
  const bytes = Buffer.from(Array.from({ length: 2048 }, (_, i) => i % 256));
  await writeFile(join(root, 'record.bin'), bytes);
  await writeFile(join(root, 'short.bin'), bytes.subarray(0, 1024));
  const origin = `
  map $arg_limit $fixture { default /record.bin; 5 /short.bin; }
  map $arg_policy $policy {
    default "";
    no-store no-store;
    private private;
    no-cache no-cache;
    expire "public, max-age=1, must-revalidate";
  }
  map $arg_policy $cookie { default ""; cookie "session=synthetic-test"; }
  map $arg_policy $vary { default ""; vary-all "*"; }
  server {
    listen 18080;
    root /work;
    etag on;
    default_type application/vnd.sdn.flatbuffers.stream;
    add_header Cache-Control $policy always;
    add_header Set-Cookie $cookie always;
    add_header Vary $vary always;
    add_header X-Origin-Request $request_id always;
    location = /api/v1/ready { return 200 "ready"; }
    location /api/v1/data/ {
      error_page 418 = @wrong_type;
      if ($arg_policy = wrong-type) { return 418; }
      if (-f /work/unavailable) { return 503; }
      if ($arg_status = 401) { return 401; }
      if ($arg_status = 503) { return 503; }
      if ($arg_echo = 1) {
        return 200 "$host|$http_authorization|$http_cookie|$http_x_forwarded_for|$http_forwarded|$http_x_original_url|$http_accept|$request_method";
      }
      if ($arg_policy = no-etag) { return 200 "unvalidated"; }
      try_files $fixture =404;
    }
    location @wrong_type { default_type text/plain; try_files $fixture =404; }
  }
`;
  const config = renderNginx({ schemas: ['omm', 'cat'], upstream: 'http://127.0.0.1:18080',
    listen: '0.0.0.0:8088', cacheMiB: 4 }).replace(/\n}\n$/, `${origin}\n}\n`);
  await writeFile(join(root, 'nginx.conf'), config);
  const launch = run(['run', '--detach', '--name', name, '--read-only', '--memory', '96m',
    '--tmpfs', '/tmp:rw,noexec,nosuid,size=32m', '--publish', '127.0.0.1::8088',
    '--mount', `type=bind,source=${root},target=/work,readonly`, image,
    'nginx', '-c', '/work/nginx.conf', '-g', 'daemon off;']);
  assert.equal(launch.status, 0, launch.stderr);
  started = true;
  const mapping = run(['port', name, '8088/tcp']);
  const port = Number(mapping.stdout.trim().match(/:(\d+)$/)?.[1]);
  assert.ok(port > 0, mapping.stderr + run(['logs', name]).stderr);
  let ready = false;
  for (let attempt = 0; attempt < 30; attempt++) {
    try { await get(port, '/api/v1/ready'); ready = true; break; }
    catch { await pause(100); }
  }
  if (!ready) assert.fail(run(['logs', name]).stderr);
  const route = '/api/v1/data/omm/bulk?limit=10';
  const cold = await get(port, route);
  assert.equal(cold.status, 200);
  assert.deepEqual(cold.body, bytes);
  assert.equal(cold.headers['x-sdn-cache'], 'MISS');
  await pause(1200);
  const warm = await get(port, route);
  assert.equal(warm.headers['x-sdn-cache'], 'HIT');
  assert.deepEqual(warm.body, bytes);
  assert.equal(warm.headers['x-origin-request'], cold.headers['x-origin-request']);
  assert.equal(warm.headers.date, cold.headers.date);
  assert.equal(warm.headers['cache-control'], 'public, max-age=0, s-maxage=15, must-revalidate');
  assert.equal(warm.headers['access-control-allow-origin'], '*');
  const head = await get(port, route, { method: 'HEAD' });
  assert.equal(head.status, 200);
  assert.equal(head.body.length, 0);
  assert.equal(Number(head.headers['content-length']), bytes.length);
  const conditional = await get(port, route, { headers: { 'If-None-Match': `"other", W/${warm.headers.etag}` } });
  assert.equal(conditional.status, 304);
  assert.equal(conditional.body.length, 0);
  const preflight = await get(port, route, { method: 'OPTIONS', headers: {
    Origin: 'https://reader.example', 'Access-Control-Request-Headers': 'if-none-match',
  } });
  assert.equal(preflight.status, 204);
  assert.equal(preflight.headers['cache-control'], 'no-store');
  assert.match(preflight.headers['access-control-allow-headers'], /If-None-Match/);

  const concurrent = await Promise.all(Array.from({ length: 12 }, () => get(port, route.replace('10', '5'))));
  assert.equal(concurrent.filter((res) => res.headers['x-sdn-cache'] === 'MISS').length, 1);
  assert.equal(concurrent.filter((res) => res.headers['x-sdn-cache'] === 'HIT').length, 11);
  for (const res of concurrent) assert.deepEqual(res.body, bytes.subarray(0, 1024));
  assert.equal(new Set(concurrent.map((res) => res.headers['x-origin-request'])).size, 1);
  const accept = await get(port, route, { headers: { Accept: 'application/json' } });
  assert.equal(accept.headers['x-sdn-cache'], 'MISS');
  const anotherSchema = await get(port, route.replace('/omm/', '/cat/'));
  assert.equal(anotherSchema.headers['x-sdn-cache'], 'MISS');

  for (const policy of ['no-store', 'private', 'no-cache', 'cookie', 'wrong-type', 'no-etag', 'vary-all']) {
    const first = await get(port, `${route}&policy=${policy}`);
    const second = await get(port, `${route}&policy=${policy}`);
    assert.notEqual(first.headers['x-origin-request'], second.headers['x-origin-request'], policy);
    assert.notEqual(second.headers['x-sdn-cache'], 'HIT', policy);
    if (policy === 'vary-all') assert.match(second.headers.vary, /\*/);
    else assert.equal(second.headers['cache-control'], ['private', 'no-cache'].includes(policy) ? policy : 'no-store', policy);
    if (policy === 'wrong-type') assert.equal(second.status, 200);
  }
  for (const status of [401, 503]) {
    const first = await get(port, `${route}&status=${status}`);
    const second = await get(port, `${route}&status=${status}`);
    assert.equal(second.status, status);
    assert.notEqual(first.headers['x-origin-request'], second.headers['x-origin-request']);
    assert.equal(second.headers['cache-control'], 'no-store');
  }
  for (const headers of [{ 'Cache-Control': 'no-cache' }, { 'Cache-Control': 'no-store' }, { Pragma: 'no-cache' }]) {
    const bypass = await get(port, route, { headers });
    assert.equal(bypass.headers['x-sdn-cache'], 'BYPASS');
    assert.equal(bypass.headers['cache-control'], 'no-store');
    assert.notEqual(bypass.headers['x-origin-request'], warm.headers['x-origin-request']);
    const stillCached = await get(port, route);
    assert.equal(stillCached.headers['x-origin-request'], warm.headers['x-origin-request']);
  }
  const precondition = await get(port, route, { headers: { 'If-Match': '"wrong"' } });
  assert.equal(precondition.status, 412);
  assert.equal(precondition.headers['cache-control'], 'no-store');
  const echoed = await get(port, `${route}&echo=1`, { headers: {
    Host: 'untrusted.example', 'X-Forwarded-For': '198.51.100.1', Forwarded: 'host=untrusted.example',
    'X-Original-URL': '/api/v1/admin/update/status', Accept: 'text/plain',
  } });
  assert.equal(echoed.body.toString(), 'localhost||||||text/plain|GET');
  const denied = [
    ['/api/v1/admin/update/status', {}, 404],
    ['/api/v1/data/kmf/bulk', {}, 404],
    ['/api/v1/data/rfb/bulk', {}, 404],
    ['/api/v1/data/omm/bulk/extra', {}, 404],
    ['/api/v1/data/%6fmm/bulk', {}, 404],
    [route, { headers: { Authorization: 'Bearer synthetic-test' } }, 403],
    [route, { headers: { Cookie: 'session=synthetic-test' } }, 403],
    [route, { headers: { Range: 'bytes=0-10' } }, 416],
    [route, { method: 'POST' }, 405],
    [route, { headers: { 'Content-Length': '1' }, body: 'x' }, 400],
  ];
  for (const [path, options, status] of denied) {
    const res = await get(port, path, options);
    assert.equal(res.status, status, path);
    assert.equal(res.headers['cache-control'], 'no-store', path);
    assert.equal(res.headers['x-origin-request'], undefined, path);
  }
  const health = await get(port, '/api/v1/ready');
  assert.equal(health.headers['cache-control'], 'no-store');
  assert.equal(health.headers['x-sdn-cache'], undefined);

  const expiring = `${route}&policy=expire`;
  const before = await get(port, expiring);
  await pause(2200);
  const unchanged = await get(port, expiring);
  assert.equal(unchanged.headers['x-sdn-cache'], 'REVALIDATED');
  assert.deepEqual(unchanged.body, bytes);
  const replacement = Buffer.concat([bytes, Buffer.from([1])]);
  await writeFile(join(root, 'record.bin'), replacement);
  await pause(2200);
  const changed = await get(port, expiring);
  assert.equal(changed.status, 200);
  assert.deepEqual(changed.body, replacement);
  assert.notEqual(changed.headers.etag, before.headers.etag);
  assert.equal(changed.headers['x-sdn-cache'], 'EXPIRED');
  await writeFile(join(root, 'unavailable'), 'test origin unavailable');
  await pause(2200);
  const unavailable = await get(port, expiring);
  assert.equal(unavailable.status, 503);
  assert.equal(unavailable.headers['cache-control'], 'no-store');
  assert.notEqual(unavailable.headers['x-sdn-cache'], 'STALE');
  assert.ok((await readFile(join(root, 'nginx.conf'), 'utf8')).includes('proxy_cache_use_stale off'));
});
