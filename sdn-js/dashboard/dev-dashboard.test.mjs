import assert from 'node:assert/strict';
import { once } from 'node:events';
import { createServer as createHttpServer } from 'node:http';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { createServer } from 'vite';
import { dashboardDependencyBoundary, dashboardDevServer } from './vite.config.mjs';

test('node dashboard refuses orbital engines and terrain in page and worker imports', () => {
  const boundary = dashboardDependencyBoundary();
  for (const id of ['@macrostrat/cesium-martini', '../orbital-console/App.svelte', 'cesium',
    '/workspace/OrbPro/Build/OrbPro.esm.js', '@orbpro/engine', 'OrbPro.abcd.esm.js']) {
    assert.throws(() => boundary.resolveId(id, '/dashboard/App.svelte'), /must not import/);
  }
  for (const id of ['three', './peers/land.json', 'flatsql/wasm', 'sdn-node-status-runtime']) {
    assert.equal(boundary.resolveId(id, '/dashboard/App.svelte'), null);
  }
});

test('dev proxy accepts local node origins without weakening TLS verification', () => {
  for (const origin of ['https://example.com', 'http://user:password@localhost:7173',
    'http://127.0.0.1:7173/private', 'http://127.0.0.1:7173/?key=secret', 'file:///tmp/node']) {
    assert.throws(() => dashboardDevServer(origin));
  }
  const config = dashboardDevServer('https://localhost:14080');
  assert.equal(config.host, '127.0.0.1');
  assert.equal(config.port, 5181);
  assert.equal(config.proxy['/api'].target, 'https://localhost:14080');
  assert.notEqual(config.proxy['/api'].secure, false);
  assert.equal(config.proxy['/ws'].ws, true);
});

test('dashboard dev entry loads without Orbital Console and proxies the node same-origin', { timeout: 60_000 }, async (t) => {
  const origin = createHttpServer((req, res) => {
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ path: req.url, host: req.headers.host }));
  });
  origin.listen(0, '127.0.0.1');
  await once(origin, 'listening');
  t.after(() => new Promise((resolve) => { origin.closeAllConnections(); origin.close(resolve); }));
  const nodeOrigin = `http://127.0.0.1:${origin.address().port}`;
  const cacheDir = await mkdtemp(join(tmpdir(), 'sdn-dashboard-vite-test-'));
  t.after(() => rm(cacheDir, { recursive: true, force: true }));
  const server = await createServer({
    configFile: fileURLToPath(new URL('./vite.config.mjs', import.meta.url)),
    logLevel: 'error',
    cacheDir,
    // This HTTP harness requests the entry only. Leave recursive module
    // loading to browser verification, so teardown cannot race eager transforms.
    server: { ...dashboardDevServer(nodeOrigin), port: 0, strictPort: false, preTransformRequests: false }
  });
  t.after(() => server.close());
  await server.listen();
  const base = `http://127.0.0.1:${server.httpServer.address().port}`;
  const navigation = await fetch(`${base}/?screen=store`, { headers: { Accept: 'text/html' }, redirect: 'manual' });
  assert.equal(navigation.status, 307);
  assert.equal(navigation.headers.get('location'), `http://localhost:${server.httpServer.address().port}/?screen=store`);
  assert.equal(navigation.headers.get('cache-control'), 'no-store');
  const canonical = await fetch(base.replace('127.0.0.1', 'localhost'), { headers: { Accept: 'text/html' }, redirect: 'manual' });
  assert.equal(canonical.status, 200);
  const response = await fetch(base);
  const html = await response.text();
  assert.equal(response.status, 200);
  assert.match(html, /Space Data Network — Node Dashboard/);
  assert.match(html, /src="\.\/main\.js"/);
  assert.doesNotMatch(html, /main\.ts|Orbital Console/);
  assert.equal(response.headers.get('cross-origin-embedder-policy'), 'require-corp');
  const entry = await server.transformRequest('/main.js');
  assert.match(entry.code, /dashboard-tailadmin/);
  assert.doesNotMatch(entry.code, /orbital-console|cesium-martini|OrbPro/);
  const proxied = await (await fetch(`${base}/api/v1/ready?probe=dashboard`)).json();
  assert.equal(proxied.path, '/api/v1/ready?probe=dashboard');
  assert.equal(proxied.host, new URL(nodeOrigin).host);
});
