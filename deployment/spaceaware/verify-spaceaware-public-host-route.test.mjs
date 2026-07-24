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
const sdnConsoleRoot = Buffer.from(`<!doctype html>
<title>Space Data Network — Node Console</title>
<div id="sdn-node-console-v1" class="app-shell">
  <nav class="sdn-rail">
    <span>NODE</span><span>PEERS</span><span>DATA</span>
    <span>CHANNELS</span><span>APPS</span><span>MODULES</span>
  </nav>
  <code>/sdn/v1</code>
</div>
`);
const sdnAppsRoot = Buffer.from('<!doctype html><title>SDN APPS</title><main>Installed applications</main>\n');
const sdnConsoleAssets = [
  { path: '/styles.css', body: Buffer.from('/* SDN Node Console */\n'), type: 'text/css' },
  { path: '/app.js', body: Buffer.from('console.info("SDN Node Console");\n'), type: 'text/javascript' },
  { path: '/module-harness.js', body: Buffer.from('export const harness = true;\n'), type: 'text/javascript' },
  { path: '/flatbuffers.js', body: Buffer.from('export const flatbuffers = true;\n'), type: 'text/javascript' },
  ...[
    'chakra-400.woff2',
    'chakra-500.woff2',
    'chakra-600.woff2',
    'chakra-700.woff2',
    'plex-400.woff2',
    'plex-500.woff2',
    'plex-600.woff2',
  ].map((name) => ({
    path: `/fonts/${name}`,
    body: Buffer.from(`fixture ${name}\n`),
    type: 'font/woff2',
  })),
];

function contentAddressedWalletAsset(extension, body) {
  const digest = createHash('sha256').update(body).digest('hex');
  const type = {
    css: 'text/css',
    js: 'text/javascript',
    wasm: 'application/wasm',
  }[extension];
  return { path: `/assets/wallet-origin.${digest}.${extension}`, body, type };
}

function makeWalletOriginBundle(version = '2.0.28') {
  const wasm = contentAddressedWalletAsset(
    'wasm',
    Buffer.from(`fixture wallet wasm ${version}\n`),
  );
  const js = contentAddressedWalletAsset(
    'js',
    Buffer.from(`const version="${version}";const labels=["HD Wallet","Login","Account"];const wasm="${wasm.path.split('/').pop()}";\n`),
  );
  const css = contentAddressedWalletAsset(
    'css',
    Buffer.from(`/* fixture HD Wallet ${version} */\n`),
  );
  const index = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<title>SDN Wallet</title>
<link rel="stylesheet" href="${css.path}" integrity="${integrity(css)}" crossorigin="anonymous">
</head><body><main data-wallet-origin-root aria-live="polite"></main>
<script type="module" src="${js.path}" integrity="${integrity(js)}" crossorigin="anonymous"></script>
</body></html>`;
  return { assets: [css, js, wasm], index };
}

const reviewedWalletOrigin = makeWalletOriginBundle();

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

function dependencyServer(corruptAsset = () => false, walletOrigin = () => reviewedWalletOrigin) {
  return createServer((req, res) => {
    const currentWalletOrigin = walletOrigin();
    if (req.url === '/') {
      res.writeHead(200, { 'Content-Type': 'text/html' }).end(currentWalletOrigin.index);
      return;
    }
    const asset = [...reviewedAssets, ...currentWalletOrigin.assets]
      .find((candidate) => candidate.path === req.url);
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
  assert.doesNotMatch(reviewedWalletOrigin.index, /HD Wallet|Login/u);
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
  let wrongWalletAsset = false;
  let wrongWalletWasm = false;
  let wrongConsoleAsset = false;
  let staleWallet = false;
  let allowSdnP2pUpgrade = true;
  const seenHosts = new Set();
  const sdnUpgradePaths = [];
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
      if (req.url === '/' || req.url === '/index.html') {
        return void res.writeHead(200, { 'Content-Type': 'text/html' }).end(sdnConsoleRoot);
      }
      const consoleAsset = sdnConsoleAssets.find((candidate) => candidate.path === req.url);
      if (consoleAsset) {
        const body = wrongConsoleAsset && consoleAsset.path === '/app.js'
          ? Buffer.from('corrupt public console JavaScript\n')
          : consoleAsset.body;
        return void res.writeHead(200, { 'Content-Type': consoleAsset.type }).end(body);
      }
      if (req.url === '/apps/') {
        return void res.writeHead(200, { 'Content-Type': 'text/html' }).end(sdnAppsRoot);
      }
      if (req.url === '/wallet/callback') return void res.writeHead(200).end(web.callback);
      if (req.url === '/api/module-delivery/provider') return void res.writeHead(200).end(JSON.stringify(provider));
    }
    res.writeHead(404).end();
  });
  edge.on('upgrade', (req, socket) => {
    const host = String(req.headers.host || '').split(':')[0];
    if (host === 'sdn.test') {
      sdnUpgradePaths.push(req.url);
      if (req.url !== '/' && !allowSdnP2pUpgrade) {
        socket.end('HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n');
        return;
      }
    }
    replyWebSocket(req, socket);
  });
  const spaceDirect = createServer((req, res) => {
    if (req.url === '/api/module-delivery/provider') res.writeHead(200).end(JSON.stringify(provider));
    else res.writeHead(404).end();
  });
  const sdnDirect = createServer((req, res) => {
    if (req.url === '/api/module-delivery/provider') res.writeHead(200).end(JSON.stringify(provider));
    else if (req.url === '/apps/') res.writeHead(200, { 'Content-Type': 'text/html' }).end(sdnAppsRoot);
    else if (req.url === '/wallet/callback') res.writeHead(200, { 'Content-Type': 'text/html' }).end(web.callback);
    else res.writeHead(404).end();
  });
  const sdnConsoleDirect = createServer((req, res) => {
    if (req.url === '/' || req.url === '/index.html') {
      res.writeHead(200, { 'Content-Type': 'text/html' }).end(sdnConsoleRoot);
      return;
    }
    const asset = sdnConsoleAssets.find((candidate) => candidate.path === req.url);
    if (!asset) return void res.writeHead(404).end();
    res.writeHead(200, { 'Content-Type': asset.type }).end(asset.body);
  });
  const dependencies = dependencyServer(
    (asset) => (wrongAsset && asset.path.startsWith('/assets/hd-wallet-ui/'))
      || (wrongWalletAsset && asset.path.startsWith('/assets/wallet-origin.') && asset.path.endsWith('.js'))
      || (wrongWalletWasm && asset.path.startsWith('/assets/wallet-origin.') && asset.path.endsWith('.wasm')),
    () => makeWalletOriginBundle(staleWallet ? '2.0.27' : '2.0.28'),
  );
  const [edgePort, spaceDirectPort, sdnDirectPort, sdnConsolePort, dependencyPort] = await Promise.all([
    listen(edge), listen(spaceDirect), listen(sdnDirect), listen(sdnConsoleDirect), listen(dependencies),
  ]);
  t.after(async () => Promise.all([
    close(edge),
    close(spaceDirect),
    close(sdnDirect),
    close(sdnConsoleDirect),
    close(dependencies),
  ]));

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
    '--sdn-console-port', String(sdnConsolePort),
    '--static-origin', `http://127.0.0.1:${dependencyPort}`,
    '--wallet-origin', `http://127.0.0.1:${dependencyPort}`,
    '--timeout-ms', '2000',
  ];
  const result = await runVerifier(args);
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stdout, /both public hosts/i);
  assert.ok(seenHosts.has('www.spaceaware.test'));
  assert.deepEqual(sdnUpgradePaths, ['/', `/p2p/${provider.peerId}`]);

  sdnUpgradePaths.length = 0;
  allowSdnP2pUpgrade = false;
  const sdnOnlyArgs = [...args];
  sdnOnlyArgs[sdnOnlyArgs.indexOf('--mode') + 1] = 'sdn-public';
  const sdnOnly = await runVerifier(sdnOnlyArgs);
  assert.equal(sdnOnly.code, 0, sdnOnly.stderr);
  assert.match(sdnOnly.stdout, /SDN public Node console, Apps sidecar, and websocket route/i);
  assert.deepEqual(sdnUpgradePaths, ['/']);

  wrongConsoleAsset = true;
  let failed = await runVerifier(args);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /SDN public console asset .* does not match direct console/i);
  wrongConsoleAsset = false;
  wrongAsset = true;
  failed = await runVerifier(args);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /SRI sha384 mismatch/i);
  wrongAsset = false;
  wrongWalletAsset = true;
  failed = await runVerifier(args);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /wallet origin asset sha256 does not match/i);
  wrongWalletAsset = false;
  wrongWalletWasm = true;
  failed = await runVerifier(args);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /wallet origin WASM sha256 does not match/i);
  wrongWalletWasm = false;
  staleWallet = true;
  failed = await runVerifier(args);
  assert.notEqual(failed.code, 0);
  assert.match(failed.stderr, /hd-wallet-ui 2\.0\.28 login\/account surface/i);
  staleWallet = false;
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
