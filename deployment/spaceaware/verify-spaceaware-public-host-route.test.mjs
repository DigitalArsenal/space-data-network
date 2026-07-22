import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

const verifierPath = new URL('./verify-spaceaware-public-host-route.mjs', import.meta.url);
const callbackCsp = "default-src 'none'; script-src https://static.spacedatanetwork.org; style-src 'none'; connect-src 'none'; img-src 'none'; font-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'";
const runtimeConfigInjection = '<script>window.__SDN_CONFIG__={apiBase:"/api/v1",serverBaseUrl:window.location.origin,ipfsDashboardUrl:"/webui/"};</script>';
const provider = {
  publicKey: '039c19f02f8cb6b99f954ac4d29558de86d1454d7691bd7727d9759f376bee4e94',
  peerId: '12D3KooWJ7dQtKXa7U78SZyJ4afV7jby3SLy9wKF2mWvRZUAA111',
};
const reviewedAssets = [
  { path: '/assets/hd-wallet-ui/2.0.28/public.css', body: Buffer.from('wallet css\n'), type: 'text/css' },
  { path: '/assets/hd-wallet-ui/2.0.28/public.js', body: Buffer.from('wallet public client\n'), type: 'text/javascript' },
  { path: '/assets/hd-wallet-ui/2.0.28/callback.js', body: Buffer.from('wallet callback\n'), type: 'text/javascript' },
];

function integrity(asset) {
  return `sha384-${createHash('sha384').update(asset.body).digest('base64')}`;
}

async function listen(server) {
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  return server.address().port;
}

async function close(server) {
  await new Promise((resolve) => server.close(resolve));
}

async function runVerifier(args) {
  return await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [verifierPath.pathname, ...args]);
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => { stdout += chunk; });
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.once('error', reject);
    child.once('close', (code) => resolve({ code, stdout, stderr }));
  });
}

async function makeWebRoot(t) {
  const root = await mkdtemp(join(tmpdir(), 'spaceaware-route-verify-'));
  t.after(async () => rm(root, { recursive: true, force: true }));
  await mkdir(join(root, 'wallet', 'callback'), { recursive: true });
  const index = '<!doctype html><title>SpaceAware</title><main>release candidate</main>\n';
  const callback = `<!doctype html><title>Completing wallet connection</title><script src="https://static.spacedatanetwork.org${reviewedAssets[2].path}" integrity="${integrity(reviewedAssets[2])}"></script>\n`;
  const orbpro = `<!doctype html><title>OrbPro</title><link rel="stylesheet" href="https://static.spacedatanetwork.org${reviewedAssets[0].path}" integrity="${integrity(reviewedAssets[0])}"><script src="https://static.spacedatanetwork.org${reviewedAssets[1].path}" integrity="${integrity(reviewedAssets[1])}"></script>\n`;
  const identity = `${JSON.stringify({
    schemaVersion: 1,
    releaseId: 'spaceaware-v2_0_28',
    files: [
      { path: 'index.html', bytes: Buffer.byteLength(index), sha256: createHash('sha256').update(index).digest('hex') },
      { path: 'orbpro/index.html', bytes: Buffer.byteLength(orbpro), sha256: createHash('sha256').update(orbpro).digest('hex') },
      { path: 'wallet/callback/index.html', bytes: Buffer.byteLength(callback), sha256: createHash('sha256').update(callback).digest('hex') },
    ],
  }, null, 2)}\n`;
  await writeFile(join(root, 'index.html'), index);
  await mkdir(join(root, 'orbpro'), { recursive: true });
  await writeFile(join(root, 'orbpro', 'index.html'), orbpro);
  await writeFile(join(root, 'release-identity.json'), identity);
  await writeFile(join(root, 'wallet', 'callback', 'index.html'), callback);
  return { root, index, identity, callback, orbpro };
}

function dependencyServer(corruptAsset = () => false) {
  return createServer((req, res) => {
    if (req.url === '/') {
      res.writeHead(200, { 'Content-Type': 'text/html' }).end('<title>HD Wallet</title><button>Login</button>');
      return;
    }
    const asset = reviewedAssets.find((candidate) => candidate.path === req.url);
    if (!asset) return void res.writeHead(404).end();
    res.writeHead(200, { 'Content-Type': asset.type }).end(corruptAsset(asset) ? Buffer.from('corrupt\n') : asset.body);
  });
}

function replyWebSocket(req, socket) {
  const key = req.headers['sec-websocket-key'];
  const accept = createHash('sha1')
    .update(`${key}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`)
    .digest('base64');
  socket.end([
    'HTTP/1.1 101 Switching Protocols',
    'Connection: Upgrade',
    'Upgrade: websocket',
    `Sec-WebSocket-Accept: ${accept}`,
    '',
    '',
  ].join('\r\n'));
}

test('loopback verification requires the exact release and a real websocket handshake', async (t) => {
  const web = await makeWebRoot(t);
  const app = createServer((req, res) => {
    const responses = {
      '/': ['text/html', runtimeConfigInjection + web.index],
      '/wallet/callback/index.html': ['text/html', web.callback],
      '/release-identity.json': ['application/json', web.identity],
      '/api/v1/data/health': ['application/json', JSON.stringify({ status: 'ok', component: 'spaceaware-data-api' })],
      '/api/module-delivery/provider': ['application/json', JSON.stringify(provider)],
    };
    const selected = responses[req.url];
    if (!selected) return void res.writeHead(404).end();
    res.writeHead(200, { 'Content-Type': selected[0] }).end(selected[1]);
  });
  const ws = createServer();
  ws.on('upgrade', (req, socket) => replyWebSocket(req, socket));
  const terrain = createServer((req, res) => {
    if (req.url === '/__terrain-cache/health') res.writeHead(200).end('ok');
    else res.writeHead(404).end();
  });
  const dependencies = dependencyServer();
  const [appPort, wsPort, terrainPort, dependencyPort] = await Promise.all([listen(app), listen(ws), listen(terrain), listen(dependencies)]);
  t.after(async () => Promise.all([close(app), close(ws), close(terrain), close(dependencies)]));

  const result = await runVerifier([
    '--mode', 'loopback',
    '--web-root', web.root,
    '--spaceaware-http-port', String(appPort),
    '--spaceaware-ws-port', String(wsPort),
    '--terrain-port', String(terrainPort),
    '--static-origin', `http://127.0.0.1:${dependencyPort}`,
    '--wallet-origin', `http://127.0.0.1:${dependencyPort}`,
    '--timeout-ms', '2000',
  ]);
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stdout, /release spaceaware-v2_0_28/i);
  assert.match(result.stdout, /websocket/i);

  ws.removeAllListeners('upgrade');
  ws.on('request', (_req, res) => res.writeHead(200).end('not a websocket'));
  const failed = await runVerifier([
    '--mode', 'loopback',
    '--web-root', web.root,
    '--spaceaware-http-port', String(appPort),
    '--spaceaware-ws-port', String(wsPort),
    '--terrain-port', String(terrainPort),
    '--static-origin', `http://127.0.0.1:${dependencyPort}`,
    '--wallet-origin', `http://127.0.0.1:${dependencyPort}`,
    '--timeout-ms', '1000',
  ]);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /websocket handshake/i);
});

test('public verification checks both Host routes, callback policy, terrain, and release identity', async (t) => {
  const web = await makeWebRoot(t);
  let wrongIdentity = false;
  let wrongRoot = false;
  let wrongProvider = false;
  let wrongAsset = false;
  const seenHosts = new Set();
  const edge = createServer((req, res) => {
    const host = String(req.headers.host || '').split(':')[0];
    seenHosts.add(host);
    if (host === 'spaceaware.test' || host === 'www.spaceaware.test') {
      if (req.url === '/') return void res.writeHead(200).end(wrongRoot ? '<title>SpaceAware conjunction</title>' : web.index);
      if (req.url === '/release-identity.json') {
        return void res.writeHead(200).end(wrongIdentity ? '{"releaseId":"stale"}\n' : web.identity);
      }
      if (req.url === '/wallet/callback' || req.url === '/wallet/callback/') {
        const headers = {
          'Cache-Control': 'no-store',
          'Referrer-Policy': req.method === 'GET' || req.method === 'HEAD' ? 'no-referrer' : 'strict-origin-when-cross-origin',
          'Content-Security-Policy': callbackCsp,
        };
        if (req.method === 'POST') return void res.writeHead(405, { ...headers, Allow: 'GET, HEAD' }).end();
        if (req.method === 'HEAD') return void res.writeHead(200, headers).end();
        return void res.writeHead(200, headers).end(web.callback);
      }
      if (req.url === '/api/v1/data/health') return void res.writeHead(200).end(JSON.stringify({ status: 'ok', component: 'spaceaware-data-api' }));
      if (req.url === '/api/module-delivery/provider') return void res.writeHead(200).end(JSON.stringify(wrongProvider ? { ...provider, peerId: `${provider.peerId}2` } : provider));
      if (req.url === '/terrain/__terrain-cache/health' || req.url === '/ipfs/terrain/__terrain-cache/health') return void res.writeHead(200).end('ok');
    }
    if (host === 'sdn.test') {
      if (req.url === '/') return void res.writeHead(200).end('<div id="sdn-node-console-v1">2.0.28</div>');
      if (req.url === '/wallet/callback') return void res.writeHead(200).end(web.callback);
      if (req.url === '/api/module-delivery/provider') return void res.writeHead(200).end(JSON.stringify(provider));
    }
    res.writeHead(404).end();
  });
  edge.on('upgrade', (req, socket) => replyWebSocket(req, socket));
  const spaceDirect = createServer((req, res) => {
    if (req.url === '/api/module-delivery/provider') res.writeHead(200).end(JSON.stringify(provider));
    else res.writeHead(404).end();
  });
  const sdnDirect = createServer((req, res) => {
    if (req.url === '/api/module-delivery/provider') res.writeHead(200).end(JSON.stringify(provider));
    else res.writeHead(404).end();
  });
  const dependencies = dependencyServer(() => wrongAsset);
  const [edgePort, spaceDirectPort, sdnDirectPort, dependencyPort] = await Promise.all([
    listen(edge), listen(spaceDirect), listen(sdnDirect), listen(dependencies),
  ]);
  t.after(async () => Promise.all([close(edge), close(spaceDirect), close(sdnDirect), close(dependencies)]));

  const args = [
    '--mode', 'public',
    '--web-root', web.root,
    '--edge-protocol', 'http',
    '--edge-port', String(edgePort),
    '--spaceaware-host', 'spaceaware.test',
    '--www-host', 'www.spaceaware.test',
    '--sdn-host', 'sdn.test',
    '--spaceaware-http-port', String(spaceDirectPort),
    '--sdn-http-port', String(sdnDirectPort),
    '--sdn-http-protocol', 'http',
    '--static-origin', `http://127.0.0.1:${dependencyPort}`,
    '--wallet-origin', `http://127.0.0.1:${dependencyPort}`,
    '--timeout-ms', '2000',
  ];
  const result = await runVerifier(args);
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stdout, /both public hosts/i);
  assert.ok(seenHosts.has('www.spaceaware.test'));

  wrongAsset = true;
  let failed = await runVerifier(args);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /SRI sha384 mismatch/i);
  wrongAsset = false;
  wrongProvider = true;
  failed = await runVerifier(args);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /provider identity does not match direct/i);
  wrongProvider = false;
  wrongRoot = true;
  failed = await runVerifier(args);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /root does not match activated index/i);
  wrongRoot = false;
  wrongIdentity = true;
  failed = await runVerifier(args);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /release identity.*does not match/i);
});
