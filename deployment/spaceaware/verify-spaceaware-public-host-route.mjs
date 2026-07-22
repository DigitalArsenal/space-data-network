#!/usr/bin/env node

import { createHash, randomBytes } from 'node:crypto';
import { lstat, readFile } from 'node:fs/promises';
import { request as requestHttp } from 'node:http';
import { request as requestHttps } from 'node:https';
import { connect as connectTcp } from 'node:net';
import { basename, isAbsolute, join, resolve } from 'node:path';
import { connect as connectTls } from 'node:tls';
import { pathToFileURL } from 'node:url';

const callbackCsp = "default-src 'none'; script-src https://static.spacedatanetwork.org; style-src 'none'; connect-src 'none'; img-src 'none'; font-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'";
const runtimeConfigInjection = Buffer.from('<script>window.__SDN_CONFIG__={apiBase:"/api/v1",serverBaseUrl:window.location.origin,ipfsDashboardUrl:"/webui/"};</script>');
const websocketGuid = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
const maximumResponseBytes = 64 * 1024 * 1024;

function fail(message) {
  throw new Error(message);
}

async function readRegular(path, description) {
  const stat = await lstat(path);
  if (!stat.isFile() || stat.isSymbolicLink()) fail(`${description} must be a regular file: ${path}`);
  return await readFile(path);
}

function parseJson(bytes, description) {
  try {
    return JSON.parse(bytes.toString('utf8'));
  } catch (error) {
    fail(`${description} is not valid JSON: ${error.message}`);
  }
}

function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

function validateIdentity(bytes, description) {
  const identity = parseJson(bytes, description);
  if (
    identity == null
    || typeof identity !== 'object'
    || Array.isArray(identity)
    || identity.schemaVersion !== 1
    || typeof identity.releaseId !== 'string'
    || !/^[0-9A-Za-z][0-9A-Za-z_-]{0,127}$/u.test(identity.releaseId)
    || !Array.isArray(identity.files)
    || identity.files.length === 0
  ) {
    fail(`${description} has an invalid release identity schema`);
  }
  let previous = '';
  for (const record of identity.files) {
    if (
      record == null
      || typeof record !== 'object'
      || Array.isArray(record)
      || typeof record.path !== 'string'
      || record.path.length === 0
      || record.path.startsWith('/')
      || record.path.includes('\\')
      || record.path.split('/').some((segment) => !segment || segment === '.' || segment === '..')
      || (previous && previous >= record.path)
      || !Number.isSafeInteger(record.bytes)
      || record.bytes < 0
      || !/^[0-9a-f]{64}$/u.test(record.sha256)
    ) {
      fail(`${description} has an invalid or unsorted file record`);
    }
    previous = record.path;
  }
  return identity;
}

function identityRecord(identity, path) {
  const record = identity.files.find((candidate) => candidate.path === path);
  if (!record) fail(`activated release identity is missing ${path}`);
  return record;
}

function validateRecordedFile(identity, path, bytes) {
  const record = identityRecord(identity, path);
  if (record.bytes !== bytes.length || record.sha256 !== sha256(bytes)) {
    fail(`activated ${path} does not match release identity ${identity.releaseId}`);
  }
}

async function loadActivatedRelease(webRoot) {
  if (!isAbsolute(webRoot)) fail(`web root must be absolute: ${webRoot}`);
  const [index, callback, orbpro, identityBytes] = await Promise.all([
    readRegular(join(webRoot, 'index.html'), 'activated SpaceAware index'),
    readRegular(join(webRoot, 'wallet', 'callback', 'index.html'), 'activated wallet callback'),
    readRegular(join(webRoot, 'orbpro', 'index.html'), 'activated OrbPro index'),
    readRegular(join(webRoot, 'release-identity.json'), 'activated release identity'),
  ]);
  if (!index.includes(Buffer.from('SpaceAware'))) fail('activated index is not the SpaceAware UI');
  if (!callback.includes(Buffer.from('Completing wallet connection'))) {
    fail('activated wallet callback is not the reviewed callback document');
  }
  const identity = validateIdentity(identityBytes, 'activated release identity');
  validateRecordedFile(identity, 'index.html', index);
  validateRecordedFile(identity, 'orbpro/index.html', orbpro);
  validateRecordedFile(identity, 'wallet/callback/index.html', callback);
  return { index, callback, orbpro, identityBytes, identity };
}

function requestAbsolute(url, timeoutMs) {
  return new Promise((resolvePromise, rejectPromise) => {
    const client = url.protocol === 'https:' ? requestHttps : requestHttp;
    const request = client(url, {
      headers: {
        'Accept-Encoding': 'identity',
        'User-Agent': 'spaceaware-public-host-verifier/1',
      },
    }, (response) => {
      const chunks = [];
      let size = 0;
      response.on('data', (chunk) => {
        size += chunk.length;
        if (size > maximumResponseBytes) {
          request.destroy(new Error(`response exceeded ${maximumResponseBytes} bytes`));
          return;
        }
        chunks.push(chunk);
      });
      response.once('end', () => resolvePromise({
        status: response.statusCode,
        headers: response.headers,
        body: Buffer.concat(chunks),
      }));
    });
    request.setTimeout(timeoutMs, () => request.destroy(new Error(`request timed out after ${timeoutMs}ms`)));
    request.once('error', rejectPromise);
    request.end();
  });
}

function reviewedSriAssets(release) {
  const assets = [];
  for (const [description, bytes] of [
    ['activated wallet callback', release.callback],
    ['activated OrbPro index', release.orbpro],
  ]) {
    const html = bytes.toString('utf8');
    for (const match of html.matchAll(/<(?:script|link)\b[^>]*>/giu)) {
      const urlValue = match[0].match(/\b(?:src|href)="([^"]+)"/iu)?.[1];
      if (!urlValue?.startsWith('https://static.spacedatanetwork.org/')) continue;
      const integrity = match[0].match(/\bintegrity="([^"]+)"/iu)?.[1];
      const sha384Value = integrity?.split(/\s+/u).find((value) => value.startsWith('sha384-'));
      if (!sha384Value || !/^sha384-[A-Za-z0-9+/]+={0,2}$/u.test(sha384Value)) {
        fail(`${description} has a static wallet asset without a valid sha384 SRI value`);
      }
      const url = new URL(urlValue);
      if (!url.pathname.startsWith('/assets/hd-wallet-ui/2.0.28/')) {
        fail(`${description} references an unreviewed wallet asset version: ${url.pathname}`);
      }
      assets.push({ url, sha384: sha384Value.slice('sha384-'.length) });
    }
  }
  const unique = new Map(assets.map((asset) => [asset.url.href, asset]));
  if (unique.size !== 3) {
    fail(`activated release must reference exactly three reviewed wallet SRI assets; found ${unique.size}`);
  }
  return [...unique.values()];
}

async function verifyWalletDependencies(options, release) {
  for (const asset of reviewedSriAssets(release)) {
    const fetchUrl = new URL(asset.url.pathname + asset.url.search, options.staticOrigin);
    let response;
    try {
      response = await requestAbsolute(fetchUrl, options.timeoutMs);
    } catch (error) {
      fail(`static wallet asset is unavailable (${asset.url.href}): ${error.message}`);
    }
    expectStatus(response, 200, `static wallet asset ${asset.url.pathname}`);
    const observed = createHash('sha384').update(response.body).digest('base64');
    if (observed !== asset.sha384) {
      fail(`static wallet asset SRI sha384 mismatch: ${asset.url.pathname}`);
    }
  }

  let wallet;
  try {
    wallet = await requestAbsolute(new URL('/', options.walletOrigin), options.timeoutMs);
  } catch (error) {
    fail(`wallet origin is unavailable (${options.walletOrigin}): ${error.message}`);
  }
  expectStatus(wallet, 200, 'wallet origin root');
  if (!headerValue(wallet, 'content-type').toLowerCase().includes('text/html')
      || !wallet.body.includes(Buffer.from('HD Wallet'))
      || !wallet.body.includes(Buffer.from('Login'))) {
    fail('wallet origin root is not the reviewed HD Wallet login surface');
  }
  process.stdout.write('Verified wallet origin and three exact hd-wallet-ui 2.0.28 SRI assets.\n');
}

function requestEndpoint({
  protocol,
  connectAddress,
  port,
  host,
  path,
  method = 'GET',
  timeoutMs,
}) {
  return new Promise((resolvePromise, rejectPromise) => {
    const request = (protocol === 'https' ? requestHttps : requestHttp)({
      hostname: connectAddress,
      port,
      path,
      method,
      servername: protocol === 'https' ? host : undefined,
      rejectUnauthorized: protocol === 'https' ? false : undefined,
      headers: {
        Host: host,
        'Accept-Encoding': 'identity',
        'User-Agent': 'spaceaware-public-host-verifier/1',
      },
    }, (response) => {
      const chunks = [];
      let size = 0;
      response.on('data', (chunk) => {
        size += chunk.length;
        if (size > maximumResponseBytes) {
          request.destroy(new Error(`response exceeded ${maximumResponseBytes} bytes`));
          return;
        }
        chunks.push(chunk);
      });
      response.once('end', () => resolvePromise({
        status: response.statusCode,
        headers: response.headers,
        body: Buffer.concat(chunks),
      }));
    });
    request.setTimeout(timeoutMs, () => request.destroy(new Error(`request timed out after ${timeoutMs}ms`)));
    request.once('error', rejectPromise);
    request.end();
  });
}

function expectStatus(response, expected, description) {
  if (response.status !== expected) {
    fail(`${description} returned HTTP ${response.status ?? 'unknown'}; expected ${expected}`);
  }
}

function headerValue(response, name) {
  const value = response.headers[name.toLowerCase()];
  return Array.isArray(value) ? value.join(', ') : String(value ?? '');
}

function expectHeader(response, name, expected, description) {
  const actual = headerValue(response, name);
  if (actual !== expected) fail(`${description} ${name} header is ${JSON.stringify(actual)}; expected ${JSON.stringify(expected)}`);
}

function expectBody(response, expected, description) {
  if (!response.body.equals(expected)) fail(`${description} does not match the activated release`);
}

function validateSpaceawareRoot(response, activatedIndex, description) {
  expectStatus(response, 200, description);
  if (response.body.equals(activatedIndex)) return;
  const injectionAt = response.body.indexOf(runtimeConfigInjection);
  if (injectionAt !== -1
      && response.body.indexOf(runtimeConfigInjection, injectionAt + 1) === -1) {
    const withoutRuntimeConfig = Buffer.concat([
      response.body.subarray(0, injectionAt),
      response.body.subarray(injectionAt + runtimeConfigInjection.length),
    ]);
    if (withoutRuntimeConfig.equals(activatedIndex)) return;
  }
  fail(`${description} does not match activated index`);
}

function validateHealth(response, description) {
  expectStatus(response, 200, description);
  const health = parseJson(response.body, description);
  if (health?.status !== 'ok' || health?.component !== 'spaceaware-data-api') {
    fail(`${description} did not report the SpaceAware data API as healthy`);
  }
}

function validateProvider(response, description) {
  expectStatus(response, 200, description);
  const provider = parseJson(response.body, description);
  if (!/^(?:02|03)[0-9a-fA-F]{64}$/u.test(provider?.publicKey ?? '')) {
    fail(`${description} has no valid compressed secp256k1 publicKey`);
  }
  if (!/^[1-9A-HJ-NP-Za-km-z]{20,128}$/u.test(provider?.peerId ?? '')) {
    fail(`${description} has no valid peerId`);
  }
  return provider;
}

function expectSameProvider(publicProvider, directProvider, description) {
  if (publicProvider.publicKey.toLowerCase() !== directProvider.publicKey.toLowerCase()
      || publicProvider.peerId !== directProvider.peerId) {
    fail(`${description} provider identity does not match direct backend`);
  }
}

function websocketHandshake({ protocol, connectAddress, port, host, path, timeoutMs, description }) {
  return new Promise((resolvePromise, rejectPromise) => {
    const key = randomBytes(16).toString('base64');
    const expectedAccept = createHash('sha1').update(key + websocketGuid).digest('base64');
    const options = protocol === 'https'
      ? { host: connectAddress, port, servername: host, rejectUnauthorized: false }
      : { host: connectAddress, port };
    const socket = protocol === 'https' ? connectTls(options) : connectTcp(options);
    let settled = false;
    let received = Buffer.alloc(0);
    const finish = (error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      socket.destroy();
      if (error) rejectPromise(error);
      else resolvePromise();
    };
    const timer = setTimeout(
      () => finish(new Error(`${description} websocket handshake timed out after ${timeoutMs}ms`)),
      timeoutMs,
    );
    const send = () => socket.write([
      `GET ${path} HTTP/1.1`,
      `Host: ${host}`,
      'Connection: keep-alive, Upgrade',
      'Upgrade: websocket',
      'Sec-WebSocket-Version: 13',
      `Sec-WebSocket-Key: ${key}`,
      'User-Agent: spaceaware-public-host-verifier/1',
      '',
      '',
    ].join('\r\n'));
    socket.once(protocol === 'https' ? 'secureConnect' : 'connect', send);
    socket.once('error', (error) => finish(new Error(`${description} websocket handshake failed: ${error.message}`)));
    socket.on('data', (chunk) => {
      received = Buffer.concat([received, chunk]);
      if (received.length > 32 * 1024) {
        finish(new Error(`${description} websocket handshake response was too large`));
        return;
      }
      const end = received.indexOf('\r\n\r\n');
      if (end === -1) return;
      const lines = received.subarray(0, end).toString('latin1').split('\r\n');
      const status = lines.shift();
      const headers = new Map();
      for (const line of lines) {
        const colon = line.indexOf(':');
        if (colon > 0) headers.set(line.slice(0, colon).trim().toLowerCase(), line.slice(colon + 1).trim());
      }
      if (!/^HTTP\/1\.[01] 101(?: |$)/u.test(status ?? '')) {
        finish(new Error(`${description} websocket handshake returned ${status || 'no HTTP status'}`));
      } else if (!/(?:^|,)\s*upgrade\s*(?:,|$)/iu.test(headers.get('connection') ?? '')) {
        finish(new Error(`${description} websocket handshake omitted Connection: Upgrade`));
      } else if ((headers.get('upgrade') ?? '').toLowerCase() !== 'websocket') {
        finish(new Error(`${description} websocket handshake omitted Upgrade: websocket`));
      } else if (headers.get('sec-websocket-accept') !== expectedAccept) {
        finish(new Error(`${description} websocket handshake returned an invalid Sec-WebSocket-Accept`));
      } else {
        finish();
      }
    });
    socket.once('end', () => {
      if (!settled) finish(new Error(`${description} websocket handshake ended before HTTP 101`));
    });
  });
}

function endpoint(options, overrides) {
  return requestEndpoint({
    protocol: 'http',
    connectAddress: options.connectAddress,
    timeoutMs: options.timeoutMs,
    ...overrides,
  });
}

async function verifyLoopback(options, release) {
  const host = options.connectAddress;
  const requestApp = (path, method = 'GET') => endpoint(options, {
    port: options.spaceawareHttpPort, host, path, method,
  });
  const root = await requestApp('/');
  validateSpaceawareRoot(root, release.index, 'SpaceAware loopback root');

  const identity = await requestApp('/release-identity.json');
  expectStatus(identity, 200, 'SpaceAware loopback release identity');
  expectBody(identity, release.identityBytes, 'SpaceAware loopback release identity');
  const callback = await requestApp('/wallet/callback/index.html');
  expectStatus(callback, 200, 'SpaceAware loopback wallet callback');
  expectBody(callback, release.callback, 'SpaceAware loopback wallet callback');
  validateHealth(await requestApp('/api/v1/data/health'), 'SpaceAware loopback health');
  const provider = validateProvider(
    await requestApp('/api/module-delivery/provider'),
    'SpaceAware loopback provider',
  );
  const terrain = await endpoint(options, {
    port: options.terrainPort, host, path: '/__terrain-cache/health',
  });
  expectStatus(terrain, 200, 'SpaceAware loopback terrain health');
  if (terrain.body.toString('utf8').trim() !== 'ok') fail('SpaceAware loopback terrain health did not return ok');
  await websocketHandshake({
    protocol: 'http',
    connectAddress: options.connectAddress,
    port: options.spaceawareWsPort,
    host,
    path: `/p2p/${provider.peerId}`,
    timeoutMs: options.timeoutMs,
    description: 'SpaceAware loopback',
  });
  process.stdout.write(`Verified SpaceAware loopback release ${release.identity.releaseId}, APIs, terrain, and websocket.\n`);
}

function edgeRequest(options, host, path, method = 'GET') {
  return requestEndpoint({
    protocol: options.edgeProtocol,
    connectAddress: options.connectAddress,
    port: options.edgePort,
    host,
    path,
    method,
    timeoutMs: options.timeoutMs,
  });
}

function validateCallbackHeaders(response, description, {
  callbackPolicy = true,
  requireAllow = false,
} = {}) {
  expectHeader(response, 'cache-control', 'no-store', description);
  if (requireAllow) {
    const allowed = headerValue(response, 'allow').split(',').map((value) => value.trim());
    if (!allowed.includes('GET') || !allowed.includes('HEAD')) {
      fail(`${description} does not allow GET and HEAD`);
    }
  }
  if (callbackPolicy) {
    expectHeader(response, 'referrer-policy', 'no-referrer', description);
    expectHeader(response, 'content-security-policy', callbackCsp, description);
  }
}

async function verifyPublic(options, release) {
  const directSpaceawareProvider = validateProvider(await requestEndpoint({
    protocol: 'http',
    connectAddress: options.connectAddress,
    port: options.spaceawareHttpPort,
    host: options.spaceawareHost,
    path: '/api/module-delivery/provider',
    timeoutMs: options.timeoutMs,
  }), 'SpaceAware direct provider');
  const directSdnProvider = validateProvider(await requestEndpoint({
    protocol: options.sdnHttpProtocol,
    connectAddress: options.connectAddress,
    port: options.sdnHttpPort,
    host: options.sdnHost,
    path: '/api/module-delivery/provider',
    timeoutMs: options.timeoutMs,
  }), 'SDN direct provider');

  const root = await edgeRequest(options, options.spaceawareHost, '/');
  validateSpaceawareRoot(root, release.index, 'SpaceAware public root');
  const identity = await edgeRequest(options, options.spaceawareHost, '/release-identity.json');
  expectStatus(identity, 200, 'SpaceAware public release identity');
  if (!identity.body.equals(release.identityBytes)) {
    fail(`SpaceAware public release identity does not match activated release ${release.identity.releaseId}`);
  }

  for (const path of ['/wallet/callback', '/wallet/callback/']) {
    const callback = await edgeRequest(options, options.spaceawareHost, path);
    expectStatus(callback, 200, `SpaceAware public callback ${path}`);
    expectBody(callback, release.callback, `SpaceAware public callback ${path}`);
    validateCallbackHeaders(callback, `SpaceAware public callback ${path}`);
  }
  const callbackHead = await edgeRequest(options, options.spaceawareHost, '/wallet/callback', 'HEAD');
  expectStatus(callbackHead, 200, 'SpaceAware public callback HEAD');
  if (callbackHead.body.length !== 0) fail('SpaceAware public callback HEAD returned a body');
  validateCallbackHeaders(callbackHead, 'SpaceAware public callback HEAD');
  const callbackPost = await edgeRequest(options, options.spaceawareHost, '/wallet/callback', 'POST');
  expectStatus(callbackPost, 405, 'SpaceAware public callback POST');
  validateCallbackHeaders(callbackPost, 'SpaceAware public callback POST', {
    callbackPolicy: false,
    requireAllow: true,
  });

  validateHealth(
    await edgeRequest(options, options.spaceawareHost, '/api/v1/data/health'),
    'SpaceAware public health',
  );
  const spaceawareProvider = validateProvider(
    await edgeRequest(options, options.spaceawareHost, '/api/module-delivery/provider'),
    'SpaceAware public provider',
  );
  expectSameProvider(spaceawareProvider, directSpaceawareProvider, 'SpaceAware public');
  for (const path of ['/terrain/__terrain-cache/health', '/ipfs/terrain/__terrain-cache/health']) {
    const terrain = await edgeRequest(options, options.spaceawareHost, path);
    expectStatus(terrain, 200, `SpaceAware public terrain ${path}`);
    if (terrain.body.toString('utf8').trim() !== 'ok') fail(`SpaceAware public terrain ${path} did not return ok`);
  }
  await websocketHandshake({
    protocol: options.edgeProtocol,
    connectAddress: options.connectAddress,
    port: options.edgePort,
    host: options.spaceawareHost,
    path: `/p2p/${spaceawareProvider.peerId}`,
    timeoutMs: options.timeoutMs,
    description: 'SpaceAware public host',
  });

  const wwwRoot = await edgeRequest(options, options.wwwHost, '/');
  validateSpaceawareRoot(wwwRoot, release.index, 'SpaceAware www public root');
  const wwwIdentity = await edgeRequest(options, options.wwwHost, '/release-identity.json');
  expectStatus(wwwIdentity, 200, 'SpaceAware www public release identity');
  if (!wwwIdentity.body.equals(release.identityBytes)) {
    fail(`SpaceAware www public release identity does not match activated release ${release.identity.releaseId}`);
  }

  const sdnRoot = await edgeRequest(options, options.sdnHost, '/');
  expectStatus(sdnRoot, 200, 'SDN public root');
  if (!sdnRoot.body.includes(Buffer.from('sdn-node-console-v1'))
      || !sdnRoot.body.includes(Buffer.from('2.0.28'))) {
    fail('SDN public root is not the reviewed 2.0.28 SDN node console');
  }
  const sdnCallback = await edgeRequest(options, options.sdnHost, '/wallet/callback');
  expectStatus(sdnCallback, 200, 'SDN public wallet callback');
  expectBody(sdnCallback, release.callback, 'SDN public wallet callback');
  const sdnProvider = validateProvider(
    await edgeRequest(options, options.sdnHost, '/api/module-delivery/provider'),
    'SDN public provider',
  );
  expectSameProvider(sdnProvider, directSdnProvider, 'SDN public');
  await websocketHandshake({
    protocol: options.edgeProtocol,
    connectAddress: options.connectAddress,
    port: options.edgePort,
    host: options.sdnHost,
    path: `/p2p/${sdnProvider.peerId}`,
    timeoutMs: options.timeoutMs,
    description: 'SDN public host',
  });
  process.stdout.write(`Verified both public hosts plus the www alias through the edge listener; SpaceAware release ${release.identity.releaseId}.\n`);
}

function parsePositiveInteger(value, flag) {
  if (!/^[1-9][0-9]*$/u.test(value ?? '')) fail(`${flag} must be a positive integer`);
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || (parsed > 65535 && flag.includes('port'))) {
    fail(`${flag} is out of range`);
  }
  return parsed;
}

function parseOrigin(value, flag) {
  let url;
  try {
    url = new URL(value);
  } catch {
    fail(`${flag} must be an absolute HTTP(S) origin`);
  }
  if (!['http:', 'https:'].includes(url.protocol)
      || url.username
      || url.password
      || url.pathname !== '/'
      || url.search
      || url.hash) {
    fail(`${flag} must be an origin without credentials, path, query, or fragment`);
  }
  if (url.protocol === 'http:' && !['127.0.0.1', 'localhost', '::1'].includes(url.hostname)) {
    fail(`${flag} may use HTTP only for a loopback test origin`);
  }
  return url.origin;
}

function parseArgs(argv) {
  const options = {
    mode: '',
    webRoot: '/opt/spaceaware/current/web',
    connectAddress: '127.0.0.1',
    spaceawareHttpPort: 5010,
    spaceawareWsPort: 8080,
    terrainPort: 8081,
    sdnHttpProtocol: 'https',
    sdnHttpPort: 18443,
    edgeProtocol: 'https',
    edgePort: 443,
    spaceawareHost: 'spaceaware.io',
    wwwHost: 'www.spaceaware.io',
    sdnHost: 'sdn.spaceaware.io',
    staticOrigin: 'https://static.spacedatanetwork.org',
    walletOrigin: 'https://wallet.spacedatanetwork.org',
    timeoutMs: 5000,
  };
  const values = new Map([
    ['--mode', ['mode', String]],
    ['--web-root', ['webRoot', resolve]],
    ['--connect-address', ['connectAddress', String]],
    ['--spaceaware-http-port', ['spaceawareHttpPort', (value) => parsePositiveInteger(value, '--spaceaware-http-port')]],
    ['--spaceaware-ws-port', ['spaceawareWsPort', (value) => parsePositiveInteger(value, '--spaceaware-ws-port')]],
    ['--terrain-port', ['terrainPort', (value) => parsePositiveInteger(value, '--terrain-port')]],
    ['--sdn-http-protocol', ['sdnHttpProtocol', String]],
    ['--sdn-http-port', ['sdnHttpPort', (value) => parsePositiveInteger(value, '--sdn-http-port')]],
    ['--edge-protocol', ['edgeProtocol', String]],
    ['--edge-port', ['edgePort', (value) => parsePositiveInteger(value, '--edge-port')]],
    ['--spaceaware-host', ['spaceawareHost', String]],
    ['--www-host', ['wwwHost', String]],
    ['--sdn-host', ['sdnHost', String]],
    ['--static-origin', ['staticOrigin', (value) => parseOrigin(value, '--static-origin')]],
    ['--wallet-origin', ['walletOrigin', (value) => parseOrigin(value, '--wallet-origin')]],
    ['--timeout-ms', ['timeoutMs', (value) => parsePositiveInteger(value, '--timeout-ms')]],
  ]);
  for (let index = 0; index < argv.length; index += 2) {
    const entry = values.get(argv[index]);
    if (!entry || argv[index + 1] == null) {
      fail(`usage: ${basename(process.argv[1])} --mode loopback|public [connection options]`);
    }
    options[entry[0]] = entry[1](argv[index + 1]);
  }
  if (!['loopback', 'public'].includes(options.mode)) fail('--mode must be loopback or public');
  if (!['http', 'https'].includes(options.edgeProtocol)) fail('--edge-protocol must be http or https');
  if (!['http', 'https'].includes(options.sdnHttpProtocol)) fail('--sdn-http-protocol must be http or https');
  for (const [name, value] of [['connect address', options.connectAddress], ['SpaceAware host', options.spaceawareHost], ['www host', options.wwwHost], ['SDN host', options.sdnHost]]) {
    if (!value || /[\s/]/u.test(value)) fail(`${name} is invalid`);
  }
  return options;
}

async function main(argv) {
  const options = parseArgs(argv);
  const release = await loadActivatedRelease(options.webRoot);
  await verifyWalletDependencies(options, release);
  if (options.mode === 'loopback') await verifyLoopback(options, release);
  else await verifyPublic(options, release);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await main(process.argv.slice(2));
  } catch (error) {
    process.stderr.write(`${error?.stack || error}\n`);
    process.exitCode = 1;
  }
}
