#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import {
  constants,
  lstat,
  mkdir,
  open,
  rename,
  unlink,
} from 'node:fs/promises';
import { basename, dirname, isAbsolute, join, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';

const sharedServerName = 'server_name spaceaware.io www.spaceaware.io sdn.spaceaware.io;';
const spaceawareServerName = 'server_name spaceaware.io www.spaceaware.io;';
const sdnServerName = 'server_name sdn.spaceaware.io;';
const spaceawareMarker = '# spaceaware-public-host-route: spaceaware-v1';
const sdnMarker = '# spaceaware-public-host-route: sdn-v1';
const lockHeldEnvironment = 'SPACEAWARE_PUBLIC_HOST_ROUTE_LOCK_HELD';
const reviewedSourceSha256 = '4f523da298477080025ceee09866b38f9e47c4d8c1df7b5f0a55ffbe1170de26';
const reviewedFinalSha256 = '5ca3b835a42a226842d227d007bbfb1120b9b07775da671ea31d3cd7efbb4aab';
const reviewedHistoricalFinalSha256 = '0cd3509325acf0e3928fd6dd7c892d3e5d4301791e7db0e01e9874942bf79d51';

const sidecarRootSdnMaps = `map $host $sdn_http_backend {
    default http://127.0.0.1:5020;
    sdn.spaceaware.io https://127.0.0.1:18443;
}

map $http_upgrade $sdn_upgrade_backend {
    default $sdn_http_backend;
    websocket http://127.0.0.1:18080;
}`;

const sdnMaps = `map $host $sdn_http_backend {
    default http://127.0.0.1:5020;
    sdn.spaceaware.io https://127.0.0.1:18443;
}

map $http_upgrade $sdn_upgrade_backend {
    default http://127.0.0.1:5020;
    websocket http://127.0.0.1:18080;
}`;

const spaceawareMaps = `map "$http_connection:$http_upgrade" $spaceaware_upgrade_backend {
    default http://127.0.0.1:5010;
    ~*(^|.*,\\s*)upgrade(\\s*,[^:]*)?:websocket$ http://127.0.0.1:8080;
}

map "$http_connection:$http_upgrade" $spaceaware_connection_header {
    default "";
    ~*(^|.*,\\s*)upgrade(\\s*,[^:]*)?:websocket$ "upgrade";
}

map $request_method $spaceaware_wallet_allow {
    default "GET, HEAD";
    GET "";
    HEAD "";
}

map $request_method $spaceaware_wallet_csp {
    default "default-src 'self'; script-src 'self' 'wasm-unsafe-eval' https://static.spacedatanetwork.org; style-src 'self' 'unsafe-inline' https://static.spacedatanetwork.org; connect-src 'self' https://wallet.spacedatanetwork.org wss: blob:; img-src 'self' data: blob:; font-src 'self' data:; worker-src 'self' blob:; child-src 'self' blob:; media-src 'self' blob:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'";
    GET "default-src 'none'; script-src https://static.spacedatanetwork.org; style-src 'none'; connect-src 'none'; img-src 'none'; font-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'";
    HEAD "default-src 'none'; script-src https://static.spacedatanetwork.org; style-src 'none'; connect-src 'none'; img-src 'none'; font-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'";
}

map $request_method $spaceaware_wallet_referrer {
    default "strict-origin-when-cross-origin";
    GET "no-referrer";
    HEAD "no-referrer";
}`;

const sdnConsoleAssetPaths = [
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
const sdnConsoleLocations = sdnConsoleAssetPaths.map((path) => `    location = ${path} {
        proxy_pass http://127.0.0.1:5020;
    }`).join('\n\n');

const sourceLocationHeaders = [
  '^~ /cli-bundle/',
  '^~ /p2p/',
  '= /index.html',
  ...sdnConsoleAssetPaths.map((path) => `= ${path}`),
  '^~ /assets/',
  '^~ /TestData/',
  '^~ /ipfs/terrain/',
  '^~ /terrain/',
  '^~ /asset-ipfs/',
  '^~ /ipfs/',
  '= /',
  '= /sdn/v1/node/qr',
  '^~ /api/module-delivery/',
  '/',
];

const spaceawareLocationHeaders = [
  '^~ /cli-bundle/',
  '^~ /p2p/',
  '= /index.html',
  '= /orbpro',
  '= /orbpro/',
  '= /orbpro/index.html',
  '= /orbpro/sandcastle',
  '= /orbpro/sandcastle/',
  '= /orbpro/sandcastle/index.html',
  '^~ /assets/',
  '^~ /TestData/',
  '^~ /orbpro/sandcastle/',
  '^~ /ipfs/terrain/',
  '^~ /terrain/',
  '^~ /asset-ipfs/',
  '^~ /ipfs-api/v0',
  '^~ /api/',
  '^~ /diag/',
  '^~ /orbpro-',
  '^~ /ipfs/',
  '~ ^/wallet/callback/?$',
  '= /',
  '/',
];

const globalCsp = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval' https://static.spacedatanetwork.org; style-src 'self' 'unsafe-inline' https://static.spacedatanetwork.org; connect-src 'self' https://wallet.spacedatanetwork.org wss: blob:; img-src 'self' data: blob:; font-src 'self' data:; worker-src 'self' blob:; child-src 'self' blob:; media-src 'self' blob:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'self'";

function fail(message) {
  throw new Error(message);
}

function formatError(error, indent = '') {
  const own = `${error?.stack || error}`
    .split('\n')
    .map((line) => indent + line)
    .join('\n');
  if (!(error instanceof AggregateError)) return own;
  return [
    own,
    ...error.errors.map((nested, index) => (
      `${indent}cause ${index + 1}:\n${formatError(nested, indent + '  ')}`
    )),
  ].join('\n');
}

function count(text, value) {
  return text.split(value).length - 1;
}

function locateBraceBlock(text, start, description) {
  const opening = text.indexOf('{', start);
  if (opening === -1) fail(`unexpected nginx config: missing ${description} opening brace`);
  let depth = 0;
  for (let index = opening; index < text.length; index += 1) {
    if (text[index] === '{') depth += 1;
    if (text[index] === '}') {
      depth -= 1;
      if (depth === 0) {
        return { start, end: index + 1, value: text.slice(start, index + 1) };
      }
      if (depth < 0) break;
    }
  }
  fail(`unexpected nginx config: unterminated ${description}`);
}

function serverBlocks(text) {
  const blocks = [];
  const pattern = /(^|\n)server\s*\{/g;
  let match;
  while ((match = pattern.exec(text)) !== null) {
    const start = match.index + (match[1] ? 1 : 0);
    const block = locateBraceBlock(text, start, 'server block');
    blocks.push(block);
    pattern.lastIndex = block.end;
  }
  return blocks;
}

function namedServer(text, serverName) {
  const matches = serverBlocks(text).filter((block) => count(block.value, serverName) === 1);
  if (matches.length !== 1) {
    fail(`unexpected nginx config: expected exactly one ${serverName}`);
  }
  return matches[0];
}

function topLevelLocationHeaders(server) {
  return [...server.matchAll(/^    location ([^{]+) \{/gm)].map((match) => match[1]);
}

function locationBlock(server, header) {
  const marker = `    location ${header} {`;
  if (count(server, marker) !== 1) {
    fail(`unexpected nginx config: expected exactly one location ${header}`);
  }
  return locateBraceBlock(server, server.indexOf(marker), `location ${header}`).value;
}

function requireOnce(text, value, description) {
  if (count(text, value) !== 1) {
    fail(`unexpected nginx config: ${description} is missing or duplicated`);
  }
}

function requireLocationInventory(server, expected, description) {
  const actual = topLevelLocationHeaders(server);
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    fail(`unexpected nginx config: ${description} route inventory changed (${actual.join(', ')})`);
  }
}

function requireProxyInventory(server, expected, description) {
  const actual = [...server.matchAll(/\bproxy_pass\s+([^;]+);/gu)]
    .map((match) => match[1])
    .sort();
  const sortedExpected = [...expected].sort();
  if (JSON.stringify(actual) !== JSON.stringify(sortedExpected)) {
    fail(`unexpected nginx config: ${description} proxy inventory changed (${actual.join(', ')})`);
  }
}

function validateSourceConfig(text) {
  const sourceDigest = createHash('sha256').update(text).digest('hex');
  if (sourceDigest !== reviewedSourceSha256) {
    fail(`unexpected nginx config: source digest ${sourceDigest} is not the reviewed production config`);
  }
  requireOnce(text, sdnMaps, 'reviewed SDN backend maps');
  requireOnce(text, sharedServerName, 'shared SpaceAware/SDN server_name');
  if (count(text, spaceawareMarker) !== 0 || count(text, sdnMarker) !== 0) {
    fail('unexpected nginx config: cutover markers are only valid in the final split config');
  }
  const blocks = serverBlocks(text);
  if (blocks.length !== 1) fail('unexpected nginx config: expected exactly one shared TLS server block');
  const shared = namedServer(text, sharedServerName);
  const headers = topLevelLocationHeaders(shared.value);
  if (JSON.stringify(headers) !== JSON.stringify(sourceLocationHeaders)) {
    fail(`unexpected nginx config: shared route inventory changed (${headers.join(', ')})`);
  }

  const root = locationBlock(shared.value, '= /');
  requireOnce(root, 'proxy_pass $sdn_upgrade_backend;', 'shared websocket-aware root route');
  const fallback = locationBlock(shared.value, '/');
  requireOnce(fallback, 'proxy_pass $sdn_http_backend;', 'shared HTTP fallback route');
  const p2p = locationBlock(shared.value, '^~ /p2p/');
  requireOnce(p2p, 'proxy_pass http://127.0.0.1:8080;', 'pre-cutover p2p route');
  const index = locationBlock(shared.value, '= /index.html');
  requireOnce(index, 'proxy_pass http://127.0.0.1:5020/;', 'pre-cutover SDN console index route');
  for (const path of sdnConsoleAssetPaths) {
    const consoleAsset = locationBlock(shared.value, `= ${path}`);
    requireOnce(consoleAsset, 'proxy_pass http://127.0.0.1:5020;', `pre-cutover SDN console asset ${path}`);
  }
  for (const header of ['^~ /assets/', '^~ /TestData/']) {
    const staticRoute = locationBlock(shared.value, header);
    requireOnce(staticRoute, 'proxy_pass $sdn_http_backend;', `pre-cutover ${header} route`);
  }
  const ipfsTerrain = locationBlock(shared.value, '^~ /ipfs/terrain/');
  requireOnce(ipfsTerrain, 'proxy_pass http://127.0.0.1:5020;', 'pre-cutover IPFS terrain route');
  const terrain = locationBlock(shared.value, '^~ /terrain/');
  requireOnce(terrain, 'rewrite ^/terrain/(.*)$ /ipfs/terrain/$1 break;', 'pre-cutover terrain rewrite');
  requireOnce(terrain, 'proxy_pass http://127.0.0.1:5020;', 'pre-cutover terrain route');
  const moduleDelivery = locationBlock(shared.value, '^~ /api/module-delivery/');
  requireOnce(moduleDelivery, 'proxy_pass https://127.0.0.1:18443;', 'SDN module-delivery route');
  const assetIpfs = locationBlock(shared.value, '^~ /asset-ipfs/');
  requireOnce(assetIpfs, 'proxy_pass http://10.132.0.3:8080/ipfs/;', 'remote asset IPFS route');
  requireOnce(assetIpfs, 'Cache-Control "public, max-age=31536000, immutable" always;', 'immutable asset IPFS cache policy');
  const ipfs = locationBlock(shared.value, '^~ /ipfs/');
  requireOnce(ipfs, 'proxy_pass http://127.0.0.1:8080;', 'pre-cutover IPFS route');
  const qr = locationBlock(shared.value, '= /sdn/v1/node/qr');
  requireOnce(qr, 'proxy_pass https://127.0.0.1:18443/api/node/epm/qr;', 'SDN compact identity QR route');
  requireOnce(shared.value, 'ssl_certificate /etc/spacedatanetwork/tls/origin.crt;', 'shared origin certificate');
  requireOnce(shared.value, 'ssl_certificate_key /etc/spacedatanetwork/tls/origin.key;', 'shared origin private-key path');
  return shared;
}

export function validateFinalConfig(text) {
  const finalDigest = createHash('sha256').update(text).digest('hex');
  if (finalDigest !== reviewedFinalSha256) {
    fail(`unexpected nginx config: final digest ${finalDigest} is not the canonical split config`);
  }
  requireOnce(text, sdnMaps, 'reviewed SDN backend maps');
  requireOnce(text, spaceawareMaps, 'SpaceAware backend and callback maps');
  if (count(text, sharedServerName) !== 0) {
    fail('unexpected nginx config: shared server_name remains after cutover');
  }
  requireOnce(text, spaceawareMarker, 'SpaceAware cutover marker');
  requireOnce(text, sdnMarker, 'SDN cutover marker');
  const blocks = serverBlocks(text);
  if (blocks.length !== 2) fail('unexpected nginx config: final route must contain exactly two TLS server blocks');

  const spaceaware = namedServer(text, spaceawareServerName).value;
  const sdn = namedServer(text, sdnServerName).value;
  requireOnce(spaceaware, spaceawareMarker, 'SpaceAware marker placement');
  requireOnce(sdn, sdnMarker, 'SDN marker placement');
  requireLocationInventory(spaceaware, spaceawareLocationHeaders, 'SpaceAware');
  requireLocationInventory(sdn, sourceLocationHeaders, 'SDN');
  for (const forbidden of [
    'http://127.0.0.1:5020',
    'https://127.0.0.1:18443',
    'http://127.0.0.1:18080',
  ]) {
    if (spaceaware.includes(forbidden)) {
      fail(`unexpected nginx config: SpaceAware server contains SDN/legacy backend ${forbidden}`);
    }
  }
  for (const required of [
    'proxy_pass $spaceaware_upgrade_backend;',
    'proxy_pass http://127.0.0.1:5010;',
    'proxy_pass http://127.0.0.1:8081;',
    'location ~ ^/wallet/callback/?$',
    'rewrite ^ /wallet/callback/index.html break;',
    'add_header Allow $spaceaware_wallet_allow always;',
    'add_header Content-Security-Policy $spaceaware_wallet_csp always;',
    'proxy_pass http://10.132.0.3:8080/ipfs/;',
    'Cache-Control "public, max-age=31536000, immutable" always;',
  ]) {
    if (!spaceaware.includes(required)) {
      fail(`unexpected nginx config: SpaceAware route contract is missing (${required})`);
    }
  }
  if (spaceaware.includes('$sdn_')) {
    fail('unexpected nginx config: SpaceAware server contains an SDN backend variable');
  }
  requireProxyInventory(spaceaware, [
    ...Array(2).fill('$spaceaware_upgrade_backend'),
    ...Array(16).fill('http://127.0.0.1:5010'),
    ...Array(2).fill('http://127.0.0.1:8081'),
    'http://10.132.0.3:8080/ipfs/',
  ], 'SpaceAware');
  if (count(spaceaware, 'proxy_pass $spaceaware_upgrade_backend;') !== 2) {
    fail('unexpected nginx config: SpaceAware root and p2p websocket routes are incomplete');
  }
  if (count(spaceaware, 'proxy_pass http://127.0.0.1:8081;') !== 2) {
    fail('unexpected nginx config: SpaceAware terrain routes are incomplete');
  }

  const sdnRoot = locationBlock(sdn, '= /');
  requireOnce(sdnRoot, 'proxy_pass $sdn_upgrade_backend;', 'SDN websocket-aware root route');
  const sdnP2p = locationBlock(sdn, '^~ /p2p/');
  requireOnce(sdnP2p, 'proxy_pass $sdn_upgrade_backend;', 'SDN p2p websocket route');
  const sdnIndex = locationBlock(sdn, '= /index.html');
  requireOnce(sdnIndex, 'proxy_pass http://127.0.0.1:5020/;', 'SDN console index route');
  for (const path of sdnConsoleAssetPaths) {
    const consoleAsset = locationBlock(sdn, `= ${path}`);
    requireOnce(consoleAsset, 'proxy_pass http://127.0.0.1:5020;', `SDN console asset ${path}`);
  }
  const sdnFallback = locationBlock(sdn, '/');
  requireOnce(sdnFallback, 'proxy_pass $sdn_http_backend;', 'SDN HTTP fallback route');
  const sdnModule = locationBlock(sdn, '^~ /api/module-delivery/');
  requireOnce(sdnModule, 'proxy_pass https://127.0.0.1:18443;', 'SDN module-delivery route');
  const sdnAsset = locationBlock(sdn, '^~ /asset-ipfs/');
  requireOnce(sdnAsset, 'proxy_pass http://10.132.0.3:8080/ipfs/;', 'SDN asset IPFS route');
  requireOnce(sdnAsset, 'Cache-Control "public, max-age=31536000, immutable" always;', 'SDN immutable asset IPFS cache policy');
  requireProxyInventory(sdn, [
    ...Array(2).fill('$sdn_upgrade_backend'),
    ...Array(3).fill('$sdn_http_backend'),
    ...Array(13).fill('http://127.0.0.1:5020'),
    'http://127.0.0.1:5020/',
    'http://127.0.0.1:8080',
    'http://10.132.0.3:8080/ipfs/',
    'https://127.0.0.1:18443',
    'https://127.0.0.1:18443/api/node/epm/qr',
  ], 'SDN');
  if (sdn.includes('http://127.0.0.1:5010') || sdn.includes('http://127.0.0.1:8081')) {
    fail('unexpected nginx config: SDN server contains a SpaceAware backend');
  }
}

export function transformManagedFinalConfig(text) {
  const digest = createHash('sha256').update(text).digest('hex');
  if (digest === reviewedFinalSha256) {
    validateFinalConfig(text);
    return text;
  }
  if (digest !== reviewedHistoricalFinalSha256) {
    validateFinalConfig(text);
  }

  requireOnce(text, sidecarRootSdnMaps, 'historical SDN sidecar-root maps');
  requireOnce(text, spaceawareMarker, 'historical SpaceAware cutover marker');
  requireOnce(text, sdnMarker, 'historical SDN cutover marker');
  const historicalSdn = namedServer(text, sdnServerName);
  const historicalIndex = locationBlock(historicalSdn.value, '= /index.html');
  requireOnce(
    historicalIndex,
    'proxy_pass $sdn_http_backend;',
    'historical SDN sidecar-root index route',
  );
  let repairedSdn = historicalSdn.value.replace(
    historicalIndex,
    historicalIndex.replace(
      'proxy_pass $sdn_http_backend;',
      'proxy_pass http://127.0.0.1:5020/;',
    ),
  );
  const repairedIndex = locationBlock(repairedSdn, '= /index.html');
  const insertion = repairedSdn.indexOf(repairedIndex) + repairedIndex.length;
  repairedSdn = repairedSdn.slice(0, insertion)
    + `\n\n${sdnConsoleLocations}`
    + repairedSdn.slice(insertion);

  let repaired = text.slice(0, historicalSdn.start)
    + repairedSdn
    + text.slice(historicalSdn.end);
  repaired = repaired.replace(sidecarRootSdnMaps, sdnMaps);
  validateFinalConfig(repaired);
  return repaired;
}

function securityHeaders(indent, {
  csp = `"${globalCsp}"`,
  referrer = '"strict-origin-when-cross-origin"',
} = {}) {
  return [
    `add_header Cross-Origin-Opener-Policy "same-origin" always;`,
    `add_header Cross-Origin-Embedder-Policy "require-corp" always;`,
    `add_header X-Content-Type-Options "nosniff" always;`,
    `add_header Referrer-Policy ${referrer} always;`,
    `add_header Strict-Transport-Security "max-age=31536000; includeSubDomains; preload" always;`,
    `add_header Content-Security-Policy ${csp} always;`,
  ].map((line) => indent + line).join('\n');
}

function cacheHeaders(maxAge) {
  return `        proxy_hide_header Cache-Control;\n        add_header Cache-Control "public, max-age=${maxAge}" always;\n${securityHeaders('        ')}`;
}

function htmlLocation(path) {
  return `    location = ${path} {
        proxy_pass http://127.0.0.1:5010;
${cacheHeaders(120)}
    }`;
}

function addSecurityHeadersToLocation(location) {
  const cacheLine = '        add_header Cache-Control ';
  const index = location.indexOf(cacheLine);
  if (index === -1) fail('unexpected nginx config: copied static location has no Cache-Control header');
  return location.slice(0, index) + securityHeaders('        ') + '\n' + location.slice(index);
}

function buildSpaceawareLocations(cliLocation, assetIpfsLocation) {
  const entrypoints = [
    '/index.html',
    '/orbpro',
    '/orbpro/',
    '/orbpro/index.html',
    '/orbpro/sandcastle',
    '/orbpro/sandcastle/',
    '/orbpro/sandcastle/index.html',
  ].map(htmlLocation).join('\n\n');
  return `${addSecurityHeadersToLocation(cliLocation)}

    location ^~ /p2p/ {
        proxy_pass $spaceaware_upgrade_backend;
    }

${entrypoints}

    location ^~ /assets/ {
        proxy_pass http://127.0.0.1:5010;
${cacheHeaders(1800)}
    }

    location ^~ /TestData/ {
        proxy_pass http://127.0.0.1:5010;
${cacheHeaders(1800)}
    }

    location ^~ /orbpro/sandcastle/ {
        proxy_pass http://127.0.0.1:5010;
${cacheHeaders(1800)}
    }

    location ^~ /ipfs/terrain/ {
        rewrite ^/ipfs/terrain/(.*)$ /$1 break;
        proxy_pass http://127.0.0.1:8081;
${cacheHeaders(1800)}
    }

    location ^~ /terrain/ {
        rewrite ^/terrain/(.*)$ /$1 break;
        proxy_pass http://127.0.0.1:8081;
${cacheHeaders(1800)}
    }

${addSecurityHeadersToLocation(assetIpfsLocation)}

    location ^~ /ipfs-api/v0 {
        rewrite ^/ipfs-api/v0(.*)$ /api/v0$1 break;
        proxy_pass http://127.0.0.1:5010;
    }

    location ^~ /api/ {
        if ($request_method = OPTIONS) {
            return 204;
        }
        proxy_hide_header Access-Control-Allow-Origin;
        proxy_hide_header Access-Control-Allow-Methods;
        proxy_hide_header Access-Control-Allow-Headers;
        proxy_hide_header Access-Control-Allow-Credentials;
        proxy_hide_header Vary;
        add_header Access-Control-Allow-Origin $http_origin always;
        add_header Access-Control-Allow-Methods "GET, POST, PUT, PATCH, DELETE, OPTIONS" always;
        add_header Access-Control-Allow-Headers "Origin, X-Requested-With, Content-Type, Accept, Authorization" always;
        add_header Access-Control-Allow-Credentials "true" always;
        add_header Vary "Origin" always;
${securityHeaders('        ')}
        proxy_pass http://127.0.0.1:5010;
    }

    location ^~ /diag/ {
        proxy_pass http://127.0.0.1:5010;
    }

    location ^~ /orbpro- {
        return 404;
    }

    location ^~ /ipfs/ {
        limit_except GET HEAD OPTIONS {
            deny all;
        }
        proxy_pass http://127.0.0.1:5010;
        proxy_set_header Connection "";
        proxy_hide_header Access-Control-Allow-Origin;
        proxy_hide_header Access-Control-Allow-Methods;
        proxy_hide_header Access-Control-Allow-Headers;
        proxy_hide_header Cache-Control;
        add_header Access-Control-Allow-Origin "*" always;
        add_header Access-Control-Allow-Methods "GET, HEAD, OPTIONS" always;
        add_header Access-Control-Allow-Headers "Accept, Range, Content-Type" always;
        add_header Access-Control-Expose-Headers "Content-Length, Content-Range" always;
        add_header Cache-Control "public, max-age=31536000, immutable" always;
${securityHeaders('        ')}
    }

    location ~ ^/wallet/callback/?$ {
        if ($request_method !~ ^(GET|HEAD)$) {
            return 405;
        }
        rewrite ^ /wallet/callback/index.html break;
        proxy_hide_header Cache-Control;
        proxy_hide_header Referrer-Policy;
        proxy_hide_header Content-Security-Policy;
        add_header Allow $spaceaware_wallet_allow always;
        add_header Cache-Control "no-store" always;
${securityHeaders('        ', {
    csp: '$spaceaware_wallet_csp',
    referrer: '$spaceaware_wallet_referrer',
  })}
        proxy_pass http://127.0.0.1:5010;
    }

    location = / {
        proxy_pass $spaceaware_upgrade_backend;
${cacheHeaders(120)}
    }

    location / {
        proxy_pass http://127.0.0.1:5010;
    }`;
}

export function transformConfig(text) {
  if (count(text, spaceawareMarker) || count(text, sdnMarker)) {
    return transformManagedFinalConfig(text);
  }

  const shared = validateSourceConfig(text);
  const firstLocation = shared.value.indexOf('    location ');
  if (firstLocation === -1) fail('unexpected nginx config: shared server has no locations');
  const prefix = shared.value.slice(0, firstLocation);
  const finalLocation = locationBlock(shared.value, '/');
  const finalLocationIndex = shared.value.lastIndexOf(finalLocation);
  if (!/^\s*}$/.test(shared.value.slice(finalLocationIndex + finalLocation.length))) {
    fail('unexpected nginx config: unreviewed content follows the shared fallback route');
  }

  const cli = locationBlock(shared.value, '^~ /cli-bundle/');
  const assetIpfs = locationBlock(shared.value, '^~ /asset-ipfs/');
  const spaceawarePrefix = prefix
    .replace(sharedServerName, `${spaceawareServerName}\n    ${spaceawareMarker}`)
    .replace(
      '    proxy_set_header Connection "upgrade";',
      '    proxy_set_header Connection $spaceaware_connection_header;',
    );
  if (spaceawarePrefix === prefix) {
    fail('unexpected nginx config: could not specialize the SpaceAware server prefix');
  }
  const spaceawareServer = `${spaceawarePrefix}${securityHeaders('    ')}\n\n${buildSpaceawareLocations(cli, assetIpfs)}\n}`;

  const sourceP2p = locationBlock(shared.value, '^~ /p2p/');
  const sdnP2p = sourceP2p.replace(
    'proxy_pass http://127.0.0.1:8080;',
    'proxy_pass $sdn_upgrade_backend;',
  );
  if (sdnP2p === sourceP2p) fail('unexpected nginx config: could not specialize the SDN p2p route');
  const sdnServer = shared.value
    .replace(sharedServerName, `${sdnServerName}\n    ${sdnMarker}`)
    .replace(sourceP2p, sdnP2p);

  const mapsInsertion = text.indexOf(shared.value);
  const next = text.slice(0, mapsInsertion)
    + `${spaceawareMaps}\n\n`
    + spaceawareServer
    + '\n\n'
    + sdnServer
    + text.slice(mapsInsertion + shared.value.length);
  validateFinalConfig(next);
  return next;
}

function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

async function readRegularFileNoFollow(path) {
  const before = await lstat(path);
  if (!before.isFile() || before.isSymbolicLink()) {
    fail(`refusing non-regular nginx config: ${path}`);
  }
  const handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    const opened = await handle.stat();
    if (!opened.isFile() || opened.dev !== before.dev || opened.ino !== before.ino) {
      fail(`nginx config changed while opening: ${path}`);
    }
    return { bytes: await handle.readFile(), stat: opened };
  } finally {
    await handle.close();
  }
}

async function writeReplacement(path, bytes, sourceStat) {
  const handle = await open(
    path,
    constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
    sourceStat.mode & 0o7777,
  );
  try {
    await handle.chown(sourceStat.uid, sourceStat.gid);
    await handle.writeFile(bytes);
    await handle.sync();
  } finally {
    await handle.close();
  }
}

async function ensureBackupDirectory(configPath, backupDir) {
  if (!isAbsolute(backupDir)) fail(`backup directory must be absolute: ${backupDir}`);
  const configDirectory = resolve(dirname(configPath));
  const resolvedBackup = resolve(backupDir);
  if (resolvedBackup === configDirectory || resolvedBackup.startsWith(configDirectory + sep)) {
    fail(`backup directory must be outside the Nginx include directory: ${resolvedBackup}`);
  }
  await mkdir(resolvedBackup, { recursive: true, mode: 0o700 });
  const backupStat = await lstat(resolvedBackup);
  if (!backupStat.isDirectory() || backupStat.isSymbolicLink()) {
    fail(`refusing non-directory backup target: ${resolvedBackup}`);
  }
  if ((backupStat.mode & 0o077) !== 0) {
    fail(`backup directory must not grant group or other access: ${resolvedBackup}`);
  }
  return resolvedBackup;
}

async function validateLockPath(configPath, lockPath) {
  if (!isAbsolute(lockPath)) fail(`installer lock path must be absolute: ${lockPath}`);
  const configDirectory = resolve(dirname(configPath));
  const resolvedLock = resolve(lockPath);
  if (resolvedLock === configDirectory || resolvedLock.startsWith(configDirectory + sep)) {
    fail(`installer lock must be outside the Nginx include directory: ${resolvedLock}`);
  }
  const parent = await lstat(dirname(resolvedLock));
  if (!parent.isDirectory() || parent.isSymbolicLink()) {
    fail(`refusing unsafe installer lock directory: ${dirname(resolvedLock)}`);
  }
  try {
    const existing = await lstat(resolvedLock);
    if (!existing.isFile() || existing.isSymbolicLink()) {
      fail(`refusing unsafe installer lock target: ${resolvedLock}`);
    }
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
  return resolvedLock;
}

function runChecked(command, args, description) {
  const result = spawnSync(command, args, { encoding: 'utf8' });
  if (result.error || result.status !== 0) {
    const detail = [result.error?.message, result.stdout, result.stderr]
      .filter(Boolean)
      .join('\n')
      .trim();
    fail(`${description} failed${detail ? `: ${detail}` : ''}`);
  }
}

async function assertCurrentDigest(configPath, expectedDigest, phase) {
  const current = await readRegularFileNoFollow(configPath);
  const actualDigest = sha256(current.bytes);
  if (actualDigest !== expectedDigest) {
    fail(`nginx config changed ${phase}: expected ${expectedDigest}, got ${actualDigest}`);
  }
  return current;
}

async function replaceWithBytesIfCurrent(
  configPath,
  bytes,
  sourceStat,
  suffix,
  expectedCurrentDigest,
) {
  const temporary = join(
    dirname(configPath),
    `.${basename(configPath)}.${suffix}.${process.pid}.${Date.now()}`,
  );
  try {
    await writeReplacement(temporary, bytes, sourceStat);
    if (expectedCurrentDigest) {
      await assertCurrentDigest(configPath, expectedCurrentDigest, 'immediately before replacement');
    }
    await rename(temporary, configPath);
    await assertCurrentDigest(configPath, sha256(bytes), 'immediately after replacement');
  } finally {
    await unlink(temporary).catch((error) => {
      if (error.code !== 'ENOENT') throw error;
    });
  }
}

async function rollbackOwnedConfig({
  trigger,
  description,
  configPath,
  original,
  candidateDigest,
  reload,
}) {
  try {
    await replaceWithBytesIfCurrent(
      configPath,
      original.bytes,
      original.stat,
      'rollback',
      candidateDigest,
    );
    if (reload) {
      runChecked('nginx', ['-t'], 'rollback nginx validation');
      runChecked('systemctl', ['reload', 'nginx'], 'rollback nginx reload');
      await assertCurrentDigest(configPath, sha256(original.bytes), 'after rollback reload');
    }
  } catch (rollbackError) {
    throw new AggregateError([trigger, rollbackError], `${description} and rollback failed`);
  }
  throw trigger;
}

async function validateVerifierPath(verifierPath) {
  if (!isAbsolute(verifierPath)) fail(`public-host verifier path must be absolute: ${verifierPath}`);
  const verifierStat = await lstat(verifierPath);
  if (!verifierStat.isFile() || verifierStat.isSymbolicLink()) {
    fail(`public-host verifier must be a regular file: ${verifierPath}`);
  }
  return verifierPath;
}

function verifyPublicHost(verifierPath) {
  runChecked(
    process.execPath,
    [verifierPath, '--mode', 'public'],
    'public-host verification',
  );
}

async function install(configPath, backupDir, verifierPath) {
  const original = await readRegularFileNoFollow(configPath);
  const originalText = original.bytes.toString('utf8');
  const transformedText = transformConfig(originalText);
  const originalDigest = sha256(original.bytes);
  if (transformedText === originalText) {
    await assertCurrentDigest(configPath, originalDigest, 'before validation');
    runChecked('nginx', ['-t'], 'nginx validation');
    await assertCurrentDigest(configPath, originalDigest, 'between validation and reload');
    runChecked('systemctl', ['reload', 'nginx'], 'nginx reload');
    await assertCurrentDigest(configPath, originalDigest, 'after reload');
    verifyPublicHost(verifierPath);
    process.stdout.write(`SpaceAware public host route already installed and reloaded in ${configPath}\n`);
    return;
  }

  await assertCurrentDigest(configPath, originalDigest, 'during preflight');
  const digest = originalDigest.slice(0, 12);
  const safeBackupDir = await ensureBackupDirectory(configPath, backupDir);
  const backupPath = join(
    safeBackupDir,
    `${basename(configPath)}.pre-spaceaware-public-route.${digest}.${Date.now()}`,
  );
  await writeReplacement(backupPath, original.bytes, original.stat);
  const candidateBytes = Buffer.from(transformedText);
  const candidateDigest = sha256(candidateBytes);
  await replaceWithBytesIfCurrent(
    configPath,
    candidateBytes,
    original.stat,
    'candidate',
    originalDigest,
  );

  try {
    runChecked('nginx', ['-t'], 'nginx validation');
    await assertCurrentDigest(configPath, candidateDigest, 'between validation and reload');
  } catch (error) {
    await rollbackOwnedConfig({
      trigger: error,
      description: 'nginx validation',
      configPath,
      original,
      candidateDigest,
      reload: false,
    });
  }

  try {
    runChecked('systemctl', ['reload', 'nginx'], 'nginx reload');
    await assertCurrentDigest(configPath, candidateDigest, 'after reload');
  } catch (error) {
    await rollbackOwnedConfig({
      trigger: error,
      description: 'nginx reload',
      configPath,
      original,
      candidateDigest,
      reload: true,
    });
  }

  try {
    verifyPublicHost(verifierPath);
    await assertCurrentDigest(configPath, candidateDigest, 'after public-host verification');
  } catch (error) {
    await rollbackOwnedConfig({
      trigger: error,
      description: 'public-host verification',
      configPath,
      original,
      candidateDigest,
      reload: true,
    });
  }

  process.stdout.write(`Installed SpaceAware public host route in ${configPath}; backup: ${backupPath}\n`);
}

function parseArgs(argv) {
  let configPath = '/etc/nginx/sites-enabled/spaceaware';
  let backupDir = '/var/backups/spacedatanetwork/nginx';
  let lockPath = '/run/sdn-public-host-route.lock';
  let verifierPath = resolve(dirname(process.argv[1]), 'verify-spaceaware-public-host-route.mjs');
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === '--config' && argv[index + 1]) {
      configPath = argv[index + 1];
      index += 1;
    } else if (argv[index] === '--backup-dir' && argv[index + 1]) {
      backupDir = argv[index + 1];
      index += 1;
    } else if (argv[index] === '--lock-path' && argv[index + 1]) {
      lockPath = argv[index + 1];
      index += 1;
    } else if (argv[index] === '--verify-script' && argv[index + 1]) {
      verifierPath = argv[index + 1];
      index += 1;
    } else {
      fail(`usage: ${basename(process.argv[1])} [--config PATH] [--backup-dir PATH] [--lock-path PATH] [--verify-script PATH]`);
    }
  }
  return { configPath, backupDir, lockPath, verifierPath };
}

async function main(argv) {
  const { configPath, backupDir, lockPath, verifierPath } = parseArgs(argv);
  const safeVerifierPath = await validateVerifierPath(verifierPath);
  if (process.env[lockHeldEnvironment] === '1') {
    await install(configPath, backupDir, safeVerifierPath);
    return;
  }

  const safeLockPath = await validateLockPath(configPath, lockPath);
  const result = spawnSync('flock', [
    '--exclusive',
    '--nonblock',
    '--conflict-exit-code', '75',
    safeLockPath,
    process.execPath,
    process.argv[1],
    ...argv,
  ], {
    env: { ...process.env, [lockHeldEnvironment]: '1' },
    stdio: 'inherit',
  });
  if (result.error) fail(`could not execute installer lock: ${result.error.message}`);
  if (result.status === 75) fail(`installer lock is busy: ${safeLockPath}`);
  if (result.status !== 0) process.exitCode = result.status ?? 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await main(process.argv.slice(2));
  } catch (error) {
    process.stderr.write(`${formatError(error)}\n`);
    process.exitCode = 1;
  }
}
