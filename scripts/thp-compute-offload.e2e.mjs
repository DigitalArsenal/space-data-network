#!/usr/bin/env node
/**
 * Real-Daemon End-to-End Test for Milestone M6:
 *   Signed $THP Threat Lanes + Subscription SDN Compute Offload ($PIV -> WasmEdge).
 *
 * Verifies:
 *   1. All 7 default $THP threat lanes (ACCESS_TIER=OPEN, ($SCHEMA_FILE_ID, RECORD_ID)
 *      companion refs, NOT_PUBLIC jammer power + what-if template, Ed25519 signed),
 *      stored into FlatSQL over /api/v1/data/publish/batch/THP.fbs, scanned back
 *      via POST /api/v1/data/scan ($QRP), and Ed25519 verified (including tamper rejection).
 *   2. Unauthenticated & unsubscribed ("free") callers hitting POST /api/v1/compute/invoke
 *      fail closed with HTTP 402, X-SDN-Compute-Fallback: browser-local, and
 *      $QRP{ERROR_CODE="subscription_required"}, followed by local Node WASM fallback.
 *   3. Subscribed ("analysis" plan via $ENT -> POST /api/v1/admin/entitlements) callers
 *      stream progressive $TMX (assess_threat_matrix, both inline & FlatSQL-hydrated $THP)
 *      and $COA/$DCO (synthesize_coas, evaluate_coa) FlatBuffers from WasmEdge that are
 *      byte-for-byte identical (Buffer.compare === 0) to local Node/SDK WASM execution.
 *   4. Real libp2p /space-data-network/compute-invoke/1.0.0 protocol stream via SDNNode:
 *      unsubscribed peer fails closed ($QRP subscription_required) and subscribed peer
 *      receives byte-identical $TMX frames.
 */

import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { execFileSync, spawn } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const ROOT_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const SDN_JS_ENTRY = path.join(ROOT_DIR, 'sdn-js/dist/index.mjs');

if (!fs.existsSync(SDN_JS_ENTRY)) {
  throw new Error(
    `Missing built sdn-js bundle at ${SDN_JS_ENTRY}. Run "cd sdn-js && npm run build:core" first.`,
  );
}

const sdn = await import(pathToFileURL(SDN_JS_ENTRY).href);
const flatbuffers = await import(
  pathToFileURL(path.join(ROOT_DIR, 'sdn-js/node_modules/flatbuffers/mjs/flatbuffers.js')).href
);
const { encodeModuleConfig } = await import(
  pathToFileURL(
    path.join(ROOT_DIR, 'sdn-js/node_modules/space-data-module-sdk/src/standards/runtimeContracts.js'),
  ).href
);
const { createRuntimeHost } = await import(
  pathToFileURL(
    path.join(ROOT_DIR, 'sdn-js/node_modules/space-data-module-sdk/src/runtime-host/index.js'),
  ).href
);
const { createBrowserModuleHarness } = await import(
  pathToFileURL(
    path.join(ROOT_DIR, 'sdn-js/node_modules/space-data-module-sdk/src/testing/browser.js'),
  ).href
);
const { ENT, ENTT, entitlementLifecycleStatus } = await import(
  pathToFileURL(path.join(ROOT_DIR, 'sdn-js/node_modules/spacedatastandards.org/lib/js/ENT/main.js'))
    .href
);
const { QRP, QRPT, qrpKind } = await import(
  pathToFileURL(path.join(ROOT_DIR, 'sdn-js/node_modules/spacedatastandards.org/lib/js/QRP/main.js'))
    .href
);
const { THP, thpAccessTier, thpThreatCategory } = await import(
  pathToFileURL(path.join(ROOT_DIR, 'sdn-js/node_modules/spacedatastandards.org/lib/js/THP/main.js'))
    .href
);
const {
  TMX,
  TMXT,
  TMXRequestT,
  tmxExecutionHostKind,
  tmxPropagatorKind,
} = await import(
  pathToFileURL(path.join(ROOT_DIR, 'sdn-js/node_modules/spacedatastandards.org/lib/js/TMX/main.js'))
    .href
);
const { SIT, SITT, SiteType } = await import(
  pathToFileURL(path.join(ROOT_DIR, 'sdn-js/node_modules/spacedatastandards.org/lib/js/SIT/main.js'))
    .href
);
const { CDM, CDMT, CDMObjectT, CATT: CdmCatT } = await import(
  pathToFileURL(path.join(ROOT_DIR, 'sdn-js/node_modules/spacedatastandards.org/lib/js/CDM/main.js'))
    .href
);

const RE_KM = 6378.137;
const MU_KM3_S2 = 398600.4418;
const COMPUTE_INVOKE_PROTOCOL_ID = '/space-data-network/compute-invoke/1.0.0';

function getFreePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.unref();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address();
      const port = typeof addr === 'object' && addr ? addr.port : 0;
      srv.close((err) => (err ? reject(err) : resolve(port)));
    });
  });
}

function circularStateKm(altKm, incDeg, raanDeg, uDeg) {
  const r = RE_KM + altKm;
  const v = Math.sqrt(MU_KM3_S2 / r);
  const inc = (incDeg * Math.PI) / 180;
  const om = (raanDeg * Math.PI) / 180;
  const u = (uDeg * Math.PI) / 180;
  const cu = Math.cos(u);
  const su = Math.sin(u);
  const co = Math.cos(om);
  const so = Math.sin(om);
  const ci = Math.cos(inc);
  const si = Math.sin(inc);
  return {
    r: [r * (co * cu - so * su * ci), r * (so * cu + co * su * ci), r * (su * si)],
    v: [v * (-co * su - so * cu * ci), v * (-so * su + co * cu * ci), v * (cu * si)],
  };
}

function fillScrStatesFromShells(statesView, bandsView, shells, stepCount, stepS) {
  const objCount = shells.length;
  for (let o = 0; o < objCount; o += 1) {
    const sh = shells[o];
    const rKm = RE_KM + sh.altKm;
    const nDegS = (Math.sqrt(MU_KM3_S2 / (rKm * rKm * rKm)) * 180) / Math.PI;
    let rMin = 1e9;
    let rMax = 0;
    for (let s = 0; s < stepCount; s += 1) {
      const t = s * stepS;
      const uDeg = (sh.u0Deg + nDegS * t) % 360;
      const st = circularStateKm(sh.altKm, sh.incDeg, sh.raanDeg, uDeg);
      const radius = Math.hypot(st.r[0], st.r[1], st.r[2]);
      rMin = Math.min(rMin, radius);
      rMax = Math.max(rMax, radius);
      const base = (s * objCount + o) * 8;
      statesView[base + 0] = st.r[0];
      statesView[base + 1] = st.r[1];
      statesView[base + 2] = st.r[2];
      statesView[base + 3] = 0.05;
      statesView[base + 4] = st.v[0];
      statesView[base + 5] = st.v[1];
      statesView[base + 6] = st.v[2];
      statesView[base + 7] = 0.0;
    }
    bandsView[o * 2 + 0] = Math.max(0, rMin - 1.0);
    bandsView[o * 2 + 1] = rMax + 1.0;
  }
}

function encodeSiteRecord(id, latDeg, lonDeg, altM, siteType = SiteType.SATELLITE_GROUND_STATION) {
  const sit = new SITT();
  sit.ID = id;
  sit.NAME = id;
  sit.SITE_TYPE = siteType;
  sit.LATITUDE = latDeg;
  sit.LONGITUDE = lonDeg;
  sit.ALTITUDE = altM;
  const builder = new flatbuffers.Builder(256);
  SIT.finishSITBuffer(builder, sit.pack(builder));
  return builder.asUint8Array().slice();
}

function encodeCdmRecord({
  messageId = 'cdm-m6-target',
  tca = '2025-01-01T01:30:00Z',
  missKm = 0.15,
  pc = 2e-2,
  norad1 = 61001,
  norad2 = 99901,
}) {
  const cdm = new CDMT();
  cdm.CCSDS_CDM_VERS = 1.0;
  cdm.MESSAGE_ID = messageId;
  cdm.TCA = tca;
  cdm.MISS_DISTANCE = missKm * 1000.0;
  cdm.COLLISION_PROBABILITY = pc;
  const obj1 = new CDMObjectT();
  obj1.OBJECT_DESIGNATOR = String(norad1);
  obj1.CATALOG_NAME = 'SATCAT';
  obj1.OBJECT_NAME = `SAT-${norad1}`;
  obj1.OBJECT = new CdmCatT();
  obj1.OBJECT.NORAD_CAT_ID = norad1;
  obj1.X = RE_KM + 550;
  obj1.Y = 0;
  obj1.Z = 0;
  obj1.X_DOT = 0;
  obj1.Y_DOT = 7.58;
  obj1.Z_DOT = 0;
  const obj2 = new CDMObjectT();
  obj2.OBJECT_DESIGNATOR = String(norad2);
  obj2.CATALOG_NAME = 'SATCAT';
  obj2.OBJECT_NAME = `OBJ-${norad2}`;
  obj2.OBJECT = new CdmCatT();
  obj2.OBJECT.NORAD_CAT_ID = norad2;
  obj2.X = RE_KM + 550;
  obj2.Y = missKm;
  obj2.Z = 0;
  obj2.X_DOT = 0;
  obj2.Y_DOT = -7.58;
  obj2.Z_DOT = 0;
  cdm.OBJECT1 = obj1;
  cdm.OBJECT2 = obj2;
  const builder = new flatbuffers.Builder(1024);
  CDM.finishCDMBuffer(builder, cdm.pack(builder));
  return builder.asUint8Array().slice();
}

function encodeTmxRequestRecord({
  requestId,
  noradIds,
  windowStart,
  windowEnd,
  thpIds,
}) {
  const req = new TMXRequestT();
  req.REQUEST_ID = requestId;
  req.TARGET_SCOPE = 'M6-E2E';
  req.TARGET_NORAD_CAT_IDS = noradIds;
  req.CATALOG_REVISION = 'm6-e2e-rev1';
  req.WINDOW_START = windowStart;
  req.WINDOW_END = windowEnd;
  req.PROPAGATOR = tmxPropagatorKind.AP;
  req.PROPAGATOR_ID = 'propagator/sdn-ap';
  req.EXECUTION_HOST = tmxExecutionHostKind.SDN_SERVER;
  req.PHENOMENOLOGIES_REQUESTED = [];
  req.THP_IDS = thpIds;
  req.THP_CIDS = [];
  req.SCORING_PROFILE_ID = 'default-v1';
  req.INCLUDE_COAS = true;
  const tmx = new TMXT();
  tmx.RUN_ID = requestId;
  tmx.CREATED_UTC = windowStart;
  tmx.WINDOW_START = windowStart;
  tmx.WINDOW_END = windowEnd;
  tmx.PROPAGATOR = tmxPropagatorKind.AP;
  tmx.PROPAGATOR_ID = 'propagator/sdn-ap';
  tmx.EXECUTION_HOST = tmxExecutionHostKind.SDN_SERVER;
  tmx.SCORING_PROFILE_ID = 'default-v1';
  tmx.CHUNK_INDEX = 0;
  tmx.IS_FINAL_CHUNK = true;
  tmx.REQUEST = req;
  const builder = new flatbuffers.Builder(1024);
  TMX.finishTMXBuffer(builder, tmx.pack(builder));
  return builder.asUint8Array().slice();
}

function encodeEntitlementRecord({
  entitlementId,
  xpub,
  peerId,
  plan,
  status = entitlementLifecycleStatus.Active,
  statusText = 'active',
  expiresAt = 0n,
}) {
  const nowSec = BigInt(Math.floor(Date.now() / 1000));
  const ent = new ENTT(
    entitlementId,
    xpub,
    peerId,
    plan,
    status,
    statusText,
    null,
    null,
    expiresAt,
    nowSec,
    nowSec,
    null,
    [],
  );
  const builder = new flatbuffers.Builder(512);
  ENT.finishENTBuffer(builder, ent.pack(builder));
  return builder.asUint8Array().slice();
}

function encodeSizePrefixedQrpScanRequest(schemaName, providerId, sourceName, limit = 100) {
  const qrp = new QRPT();
  qrp.KIND = qrpKind.Request;
  qrp.SCHEMA_NAME = schemaName;
  qrp.PROVIDER_ID = providerId;
  qrp.SOURCE_NAME = sourceName;
  qrp.PAGE = 1;
  qrp.LIMIT = limit;
  const builder = new flatbuffers.Builder(256);
  QRP.finishSizePrefixedQRPBuffer(builder, qrp.pack(builder));
  return builder.asUint8Array().slice();
}

function hasRequiredModules(dir) {
  const required = [
    'analysis/threat-matrix/dist/isomorphic/module.wasm',
    'analysis/coa-synthesizer/dist/isomorphic/module.wasm',
    'analysis/radiation-belts/dist/isomorphic/module.wasm',
    'analysis/radiation-effects/dist/isomorphic/module.wasm',
  ];
  return dir && required.every((rel) => fs.existsSync(path.join(dir, rel)));
}

function resolveModulesDir(tmpRoot) {
  const candidates = [
    process.env.SDN_MODULES_DIR,
    path.resolve(ROOT_DIR, '../space-data-network-modules'),
    path.resolve(ROOT_DIR, '../../space-data-network-modules'),
  ].filter(Boolean);

  for (const dir of candidates) {
    if (hasRequiredModules(dir)) {
      return { dir, ownedClone: false };
    }
  }

  const cloneDir = path.join(tmpRoot, 'space-data-network-modules');
  const remotes = [
    'https://github.com/DigitalArsenal/space-data-network-modules.git',
    'git@github.com:DigitalArsenal/space-data-network-modules.git',
  ];
  let lastErr = null;
  for (const url of remotes) {
    try {
      execFileSync('git', ['clone', '--depth', '1', url, cloneDir], {
        stdio: 'pipe',
      });
      if (hasRequiredModules(cloneDir)) {
        return { dir: cloneDir, ownedClone: true };
      }
    } catch (err) {
      lastErr = err;
      fs.rmSync(cloneDir, { recursive: true, force: true });
    }
  }
  throw new Error(
    `Unable to locate or clone space-data-network-modules with M4/M5a/M5b artifacts: ${lastErr?.message || 'unknown error'}`,
  );
}

function resolveOrBuildDaemonBinary(tmpRoot) {
  const envBin = process.env.SDN_BINARY;
  if (envBin && fs.existsSync(envBin)) {
    return envBin;
  }
  const builtBin = path.join(ROOT_DIR, 'sdn-server/bin/spacedatanetwork');
  if (fs.existsSync(builtBin)) {
    return builtBin;
  }
  const outBin = path.join(tmpRoot, 'spacedatanetwork');
  execFileSync(
    path.join(ROOT_DIR, 'scripts/go-with-wasmedge.sh'),
    ['build', '-o', outBin, './cmd/spacedatanetwork'],
    {
      cwd: path.join(ROOT_DIR, 'sdn-server'),
      stdio: 'pipe',
    },
  );
  return outBin;
}

function stagePluginRoot(pluginRoot, modulesDir) {
  fs.mkdirSync(pluginRoot, { recursive: true });
  fs.copyFileSync(
    path.join(ROOT_DIR, 'deployment/license/grant-policy.json'),
    path.join(pluginRoot, 'grant-policy.json'),
  );

  const specs = [
    {
      id: 'com.digitalarsenal.analysis.threat-matrix',
      rel: 'analysis/threat-matrix/dist/isomorphic/module.wasm',
      out: 'threat-matrix.wasm',
      version: '0.1.0',
    },
    {
      id: 'com.digitalarsenal.analysis.coa-synthesizer',
      rel: 'analysis/coa-synthesizer/dist/isomorphic/module.wasm',
      out: 'coa-synthesizer.wasm',
      version: '0.1.0',
    },
    {
      id: 'com.digitalarsenal.analysis.radiation-belts',
      rel: 'analysis/radiation-belts/dist/isomorphic/module.wasm',
      out: 'radiation-belts.wasm',
      version: '0.1.0',
    },
    {
      id: 'com.digitalarsenal.analysis.radiation-effects',
      rel: 'analysis/radiation-effects/dist/isomorphic/module.wasm',
      out: 'radiation-effects.wasm',
      version: '0.1.0',
    },
  ];

  const catalogEntries = [];
  const hashes = {};
  for (const s of specs) {
    const src = path.join(modulesDir, s.rel);
    const dst = path.join(pluginRoot, s.out);
    fs.copyFileSync(src, dst);
    const bytes = fs.readFileSync(dst);
    hashes[s.id] = crypto.createHash('sha256').update(bytes).digest('hex');
    catalogEntries.push({
      id: s.id,
      version: s.version,
      required_scope: 'orbpro:base',
      plain_path: s.out,
      content_type: 'application/wasm',
      grant_policy: 'open',
    });
  }

  fs.writeFileSync(
    path.join(pluginRoot, 'catalog.json'),
    JSON.stringify({ plugins: catalogEntries }, null, 2),
  );
  return hashes;
}

async function signInWalletSession(baseUrl, identity) {
  const pubHex = Buffer.from(identity.signingKey.publicKey).toString('hex');
  const chResp = await fetch(`${baseUrl}/api/auth/challenge`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      xpub: identity.xpub,
      client_pubkey_hex: pubHex,
      ts: Math.floor(Date.now() / 1000),
    }),
  });
  assert.equal(chResp.status, 200, `auth challenge failed (${chResp.status})`);
  const chJson = await chResp.json();
  const challengeRaw = Buffer.from(chJson.challenge, 'base64');
  const sig = await sdn.sign(identity.signingKey.privateKey, new Uint8Array(challengeRaw));
  const sigHex = Buffer.from(sig).toString('hex');

  const verResp = await fetch(`${baseUrl}/api/auth/verify`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      challenge_id: chJson.challenge_id,
      xpub: identity.xpub,
      client_pubkey_hex: pubHex,
      signature_hex: sigHex,
      challenge: chJson.challenge,
    }),
  });
  assert.equal(verResp.status, 200, `auth verify failed (${verResp.status})`);
  const rawCookies = verResp.headers.getSetCookie?.() ?? [
    verResp.headers.get('set-cookie') ?? '',
  ];
  const sessionPair = rawCookies
    .map((c) => c.split(';')[0].trim())
    .find((kv) => kv.startsWith('sdn_wallet_session='));
  assert.ok(sessionPair, 'verify response must return sdn_wallet_session cookie');
  return sessionPair;
}

function assertByteEqualFrameArrays(label, remoteFrames, localFrames) {
  assert.equal(
    remoteFrames.length,
    localFrames.length,
    `${label}: remote frame count (${remoteFrames.length}) != local frame count (${localFrames.length})`,
  );
  for (let i = 0; i < localFrames.length; i += 1) {
    const a = Buffer.from(remoteFrames[i]);
    const b = Buffer.from(localFrames[i]);
    assert.equal(
      Buffer.compare(a, b),
      0,
      `${label}: frame[${i}] differs between WasmEdge (${a.length} bytes) and Node WASM (${b.length} bytes)`,
    );
  }
}

async function main() {
  const tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-m6-thp-e2e-'));
  let daemon = null;
  let paidSdnNode = null;
  let freeSdnNode = null;
  let tmHarness = null;
  let coaHarness = null;

  try {
    await sdn.initHDWallet();

    const thpPublisher = await sdn.deriveIdentity(crypto.randomBytes(64), 0);
    const freeUser = await sdn.deriveIdentity(crypto.randomBytes(64), 1);
    const paidUser = await sdn.deriveIdentity(crypto.randomBytes(64), 2);

    const { dir: modulesDir } = resolveModulesDir(tmpRoot);
    const daemonBin = resolveOrBuildDaemonBinary(tmpRoot);

    const homeDir = path.join(tmpRoot, 'home');
    const storageDir = path.join(tmpRoot, 'storage');
    const pluginRoot = path.join(storageDir, 'plugins');
    fs.mkdirSync(homeDir, { recursive: true });
    fs.mkdirSync(storageDir, { recursive: true });

    const aotCache = path.join(os.homedir(), 'Library', 'Caches', 'flatsql-aot');
    if (fs.existsSync(aotCache)) {
      fs.mkdirSync(path.join(homeDir, 'Library', 'Caches'), { recursive: true });
      fs.symlinkSync(aotCache, path.join(homeDir, 'Library', 'Caches', 'flatsql-aot'));
    }

    const expectedHashes = stagePluginRoot(pluginRoot, modulesDir);
    const adminPort = await getFreePort();
    const kuboApiPort = await getFreePort();
    const kuboGwPort = await getFreePort();
    const baseUrl = `http://127.0.0.1:${adminPort}`;
    const configPath = path.join(tmpRoot, 'config.yaml');

    const freePubHex = Buffer.from(freeUser.signingKey.publicKey).toString('hex');
    const paidPubHex = Buffer.from(paidUser.signingKey.publicKey).toString('hex');
    const yaml = [
      'mode: full',
      'network:',
      '  listen:',
      '    - /ip4/127.0.0.1/tcp/0',
      '    - /ip4/127.0.0.1/tcp/0/ws',
      '  bootstrap: []',
      '  edge_relays: []',
      '  enable_relay: false',
      'storage:',
      `  path: ${JSON.stringify(storageDir)}`,
      '  max_size: 4GB',
      'peers:',
      '  strict_mode: false',
      '  enable_dht: false',
      '  enable_mdns: false',
      'admin:',
      '  enabled: true',
      `  listen_addr: "127.0.0.1:${adminPort}"`,
      '  require_auth: true',
      '  tls_mode: disabled',
      '  local_console: true',
      '  remote_requires_sealed: false',
      `  ipfs_api_url: "http://127.0.0.1:${kuboApiPort}"`,
      `  ipfs_gateway_url: "http://127.0.0.1:${kuboGwPort}"`,
      'users:',
      `  - xpub: ${JSON.stringify(freeUser.xpub)}`,
      `    signing_pubkey_hex: ${JSON.stringify(freePubHex)}`,
      '    trust_level: standard',
      '    name: "M6 Free User"',
      `  - xpub: ${JSON.stringify(paidUser.xpub)}`,
      `    signing_pubkey_hex: ${JSON.stringify(paidPubHex)}`,
      '    trust_level: standard',
      '    name: "M6 Subscribed User"',
      'publishing:',
      '  enabled: true',
      'flows:',
      '  mounts: []',
      'setup:',
      `  data_path: ${JSON.stringify(storageDir)}`,
      '',
    ].join('\n');
    fs.writeFileSync(configPath, yaml);

    const logPath = path.join(tmpRoot, 'daemon.log');
    const logFd = fs.openSync(logPath, 'w');
    daemon = spawn(daemonBin, ['daemon', '--allow-multi-daemon', '--config', configPath], {
      cwd: tmpRoot,
      env: {
        ...process.env,
        HOME: homeDir,
        SDN_PLUGIN_ROOT: pluginRoot,
      },
      stdio: ['ignore', logFd, logFd],
      detached: true,
    });

    let daemonExited = false;
    daemon.once('exit', () => {
      daemonExited = true;
    });

    const deadline = Date.now() + 90_000;
    let healthy = false;
    while (Date.now() < deadline) {
      if (daemonExited) {
        const logTail = fs.readFileSync(logPath, 'utf8').slice(-4000);
        throw new Error(`spacedatanetwork daemon exited before becoming healthy:\n${logTail}`);
      }
      try {
        const res = await fetch(`${baseUrl}/api/v1/health`);
        if (res.ok) {
          healthy = true;
          break;
        }
      } catch {
        // retry
      }
      await new Promise((r) => setTimeout(r, 250));
    }
    assert.ok(healthy, 'spacedatanetwork daemon did not reach /api/v1/health 200 in time');

    const nodeInfoRes = await fetch(`${baseUrl}/api/node/info`);
    assert.equal(nodeInfoRes.status, 200, 'GET /api/node/info should succeed');
    const nodeInfo = await nodeInfoRes.json();
    assert.equal(
      (nodeInfo.module_load_failures ?? []).length,
      0,
      `Daemon reported module_load_failures: ${JSON.stringify(nodeInfo.module_load_failures)}`,
    );
    const serverPeerId = nodeInfo.peer_id;
    const wsListenAddr = (nodeInfo.listen_addresses ?? []).find((a) => a.includes('/ws'));
    assert.ok(serverPeerId && wsListenAddr, `Expected peer_id and /ws listen address in /api/node/info`);
    const wsDialAddr = wsListenAddr.includes('/p2p/')
      ? wsListenAddr
      : `${wsListenAddr}/p2p/${serverPeerId}`;

    // =========================================================================
    // [1/4] Signed $THP Threat Lanes (all 7 phenomenologies) -> FlatSQL -> Verify
    // =========================================================================
    const thpLanes = await sdn.buildDefaultThreatProfileRecords({
      timestamp: '2025-01-01T00:00:00Z',
      signingPrivateKey: thpPublisher.signingKey.privateKey,
    });
    assert.equal(thpLanes.length, 7, 'Must build all 7 default $THP threat lanes');
    const categories = new Set();
    for (const lane of thpLanes) {
      assert.equal(lane.accessTier, thpAccessTier.OPEN, `${lane.thpId} must have ACCESS_TIER=OPEN`);
      assert.equal(lane.signed, true, `${lane.thpId} must be Ed25519 signed`);
      categories.add(lane.category);
      const unpacked = sdn.decodeThreatProfileRecord(lane.bytes);
      assert.ok(unpacked.LINKED_SCHEMA && unpacked.LINKED_RECORD_ID, `${lane.thpId} must reference companion record`);
      assert.ok(unpacked.ADDITIONAL_REFS.length >= 1, `${lane.thpId} must include ADDITIONAL_REFS`);
      const v = await sdn.verifyThreatProfileSignature(lane.bytes, thpPublisher.signingKey.publicKey);
      assert.equal(v.valid, true, `${lane.thpId} signature must verify`);
      assert.equal(v.signatureLength, 64);
    }
    assert.deepEqual(
      categories,
      new Set([
        thpThreatCategory.CO_ORBITAL,
        thpThreatCategory.DIRECT_ASCENT,
        thpThreatCategory.RF_JAMMER,
        thpThreatCategory.GNSS_DENIAL,
        thpThreatCategory.DIRECTED_ENERGY,
        thpThreatCategory.SPACE_WEATHER,
        thpThreatCategory.ISR_CUSTODY,
      ]),
      'All 7 threat phenomenologies must be represented',
    );
    const rfLane = sdn.decodeThreatProfileRecord(thpLanes.find((l) => l.slug === 'rf-jammer').bytes);
    const notPublicParam = rfLane.PARAMETERS.find((p) => p.PARAM_KEY === 'jammer_eirp_dbw');
    assert.ok(notPublicParam, 'rf-jammer lane must include jammer_eirp_dbw parameter');
    assert.equal(notPublicParam.PROVENANCE.CERTAINTY, 5, 'jammer_eirp_dbw must carry NOT_PUBLIC certainty');
    assert.ok(
      rfLane.PARAMETERS.some((p) => p.PARAM_KEY === 'WHAT_IF_TEMPLATE'),
      'rf-jammer lane must include analyst WHAT_IF_TEMPLATE',
    );

    const publishRes = await fetch(
      `${baseUrl}/api/v1/data/publish/batch/THP.fbs?provider_id=sdn-default-thp&source_name=thp-defaults&batch_id=m6-e2e-1`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/octet-stream' },
        body: sdn.encodeSizePrefixedFrames(thpLanes.map((l) => l.bytes)),
      },
    );
    assert.ok(publishRes.ok, `Publish $THP batch failed (${publishRes.status})`);

    const scanRes = await fetch(`${baseUrl}/api/v1/data/scan`, {
      method: 'POST',
      headers: { 'Content-Type': sdn.FLATBUFFER_STREAM_CONTENT_TYPE },
      body: encodeSizePrefixedQrpScanRequest('THP', 'sdn-default-thp', 'thp-defaults', 100),
    });
    assert.equal(scanRes.status, 200, `Scan $THP records failed (${scanRes.status})`);
    const scannedFrames = [];
    for (const frame of sdn.iterateSizePrefixedFrames(new Uint8Array(await scanRes.arrayBuffer()))) {
      const fid = sdn.extractFlatBufferFileIdentifier(frame);
      if (fid === '$THP') {
        scannedFrames.push(frame);
      }
    }
    assert.equal(scannedFrames.length, 7, 'FlatSQL scan must return all 7 published $THP lanes');
    const expectedById = new Map(thpLanes.map((l) => [l.thpId, l.bytes]));
    for (const storedBytes of scannedFrames) {
      const ver = await sdn.verifyThreatProfileSignature(storedBytes, thpPublisher.signingKey.publicKey);
      assert.equal(ver.valid, true, `Scanned $THP ${ver.thpId} signature must verify`);
      assert.equal(
        Buffer.compare(Buffer.from(storedBytes), Buffer.from(expectedById.get(ver.thpId))),
        0,
        `Scanned $THP ${ver.thpId} must be byte-identical to published bytes`,
      );
    }
    const tamperedObj = sdn.decodeThreatProfileRecord(scannedFrames[0]);
    tamperedObj.NAME = 'Tampered Threat Lane';
    const tamperedBuilder = new flatbuffers.Builder(1024);
    THP.finishTHPBuffer(tamperedBuilder, tamperedObj.pack(tamperedBuilder));
    const tamperedVer = await sdn.verifyThreatProfileSignature(
      tamperedBuilder.asUint8Array().slice(),
      thpPublisher.signingKey.publicKey,
    );
    assert.equal(tamperedVer.valid, false, 'Tampered $THP signature must fail verification');
    console.log('PASS [1/4] Signed $THP Threat Lanes (7 phenomenologies, OPEN, NOT_PUBLIC, FlatSQL round-trip + signature verify)');

    // =========================================================================
    // Build shared deterministic $SCR + $TMXRequest + $MCFG + companion inputs
    // =========================================================================
    const tmHost = createRuntimeHost();
    const pop = tmHost.populateEphemerisGrid({
      propagatorId: 'propagator/sdn-ap',
      catalogRevision: 'm6-e2e-rev1',
      windowStart: 1735689600,
      windowEnd: 1735689600 + 2 * 3600,
      stepS: 600,
      frame: 'TEME',
      records: [
        { id: '61001', noradId: 61001, declaredTheory: 'AP' },
        { id: '61002', noradId: 61002, declaredTheory: 'AP' },
      ],
      populateFn: ({ statesView, bandsView, stepCount, stepS }) => {
        fillScrStatesFromShells(
          statesView,
          bandsView,
          [
            { altKm: 550, incDeg: 53.0, raanDeg: 0, u0Deg: 0 },
            { altKm: 550.2, incDeg: 53.0, raanDeg: 0, u0Deg: 0.05 },
          ],
          stepCount,
          stepS,
        );
      },
    });
    const gridLease = tmHost.attachEphemerisGrid(pop.regionId, { moduleId: 'threat-matrix' });
    const scrBytes = gridLease.encodeScrGridFrame();
    gridLease.detach();

    const deterministicConfig = {
      run_id: 'm6-parity-run-1',
      target_scope: 'M6-E2E',
      propagator_id: 'propagator/sdn-ap',
      propagator_kind: 1,
      start_unix_s: 1735689600,
      threads: 1,
      max_chunks: 3,
      max_asset_buckets: 2,
      norad_ids: [61001, 61002],
      kp: 7.5,
      dst_nt: -220.0,
      sep_pfu_10mev: 1200.0,
      deterministic: true,
      measure_wall_time: false,
    };
    const tmxMcfgBytes = encodeModuleConfig(deterministicConfig);
    const tmxReqBytes = encodeTmxRequestRecord({
      requestId: 'm6-req-1',
      noradIds: [61001, 61002],
      windowStart: '2025-01-01T00:00:00Z',
      windowEnd: '2025-01-01T02:00:00Z',
      thpIds: thpLanes.map((l) => l.thpId),
    });
    const cdmBytes = encodeCdmRecord({});
    const gatewayBytes = encodeSiteRecord('gw-nordic', 69.65, 18.95, 100);
    const siteBytes = encodeSiteRecord('site-rf', 55.75, 37.62, 150, SiteType.MILITARY_BASE);

    const assessInputs = [
      { portId: 'config', schemaName: 'ModuleConfig.fbs', fileIdentifier: 'MCFG', rootType: 'ModuleConfig', payload: tmxMcfgBytes },
      { portId: 'grid', schemaName: 'SCR.fbs', fileIdentifier: '$SCR', rootType: 'SCR', payload: scrBytes },
      { portId: 'request', schemaName: 'TMX.fbs', fileIdentifier: '$TMX', rootType: 'TMX', payload: tmxReqBytes },
      ...thpLanes.map((l) => ({
        portId: 'threat_profile',
        schemaName: 'THP.fbs',
        fileIdentifier: '$THP',
        rootType: 'THP',
        payload: l.bytes,
      })),
      { portId: 'cdms', schemaName: 'CDM.fbs', fileIdentifier: '$CDM', rootType: 'CDM', payload: cdmBytes },
      { portId: 'sites', schemaName: 'SIT.fbs', fileIdentifier: '$SIT', rootType: 'SIT', payload: siteBytes },
      { portId: 'gateways', schemaName: 'SIT.fbs', fileIdentifier: '$SIT', rootType: 'SIT', payload: gatewayBytes },
    ];

    const tmWasmBytes = new Uint8Array(
      fs.readFileSync(path.join(pluginRoot, 'threat-matrix.wasm')),
    );
    tmHarness = await createBrowserModuleHarness({
      wasmSource: tmWasmBytes,
      surface: 'direct',
      initialMemoryBytes: 16 * 1024 * 1024,
      maximumMemoryBytes: 256 * 1024 * 1024,
    });
    const toSdkInputs = (list) =>
      list.map((inp) => ({
        portId: inp.portId,
        typeRef: {
          schemaName: inp.schemaName,
          fileIdentifier: inp.fileIdentifier,
          rootTypeName: inp.rootType,
          wireFormat: 'flatbuffer',
        },
        payload: inp.payload,
      }));

    const localTmResp = await tmHarness.invoke({
      methodId: 'assess_threat_matrix',
      inputs: toSdkInputs(assessInputs),
    });
    assert.equal(localTmResp.statusCode, 0, `Local threat-matrix failed: ${localTmResp.errorMessage}`);
    const localTmxFrames = localTmResp.outputs
      .filter((o) => o.portId === 'matrix')
      .map((o) => o.payload);
    assert.equal(localTmxFrames.length, 3, 'Local threat-matrix must produce 3 progressive $TMX chunks');

    // =========================================================================
    // [2/4] Fail-Closed 402 + X-SDN-Compute-Fallback: browser-local + WASM Fallback
    // =========================================================================
    const unauthRes = await sdn.invokeRemoteCompute({
      baseUrl,
      moduleId: 'com.digitalarsenal.analysis.threat-matrix',
      methodId: 'assess_threat_matrix',
      inputs: assessInputs,
    });
    assert.equal(unauthRes.ok, false);
    assert.equal(unauthRes.status, 402, 'Unauthenticated compute invoke must return HTTP 402');
    assert.equal(unauthRes.fallback, sdn.COMPUTE_FALLBACK_BROWSER_LOCAL);
    assert.equal(unauthRes.errorCode, 'subscription_required');

    const freeCookie = await signInWalletSession(baseUrl, freeUser);
    const freeRes = await sdn.invokeRemoteCompute({
      baseUrl,
      moduleId: 'com.digitalarsenal.analysis.threat-matrix',
      methodId: 'assess_threat_matrix',
      inputs: assessInputs,
      headers: { Cookie: freeCookie },
    });
    assert.equal(freeRes.ok, false);
    assert.equal(freeRes.status, 402, 'Free-plan subscriber compute invoke must return HTTP 402');
    assert.equal(freeRes.fallback, sdn.COMPUTE_FALLBACK_BROWSER_LOCAL);
    assert.equal(freeRes.errorCode, 'subscription_required');

    assert.equal(sdn.decodeTmxFrame(localTmxFrames[localTmxFrames.length - 1]).IS_FINAL_CHUNK, true);
    console.log('PASS [2/4] Fail-Closed HTTP 402 + X-SDN-Compute-Fallback: browser-local + Local WASM Fallback');

    // =========================================================================
    // [3/4] Active $ENT Subscriber Offload + Byte-for-Byte Parity (TMX / COA / DCO)
    // =========================================================================
    freeSdnNode = await sdn.SDNNode.create({
      identity: freeUser,
      edgeRelays: [wsDialAddr],
      includeIPFSBootstrap: false,
      enableStorage: false,
      enableDHT: false,
      enableIdentify: false,
      enableRelayProbing: false,
      allowPrivateAddressDial: true,
    });
    paidSdnNode = await sdn.SDNNode.create({
      identity: paidUser,
      edgeRelays: [wsDialAddr],
      includeIPFSBootstrap: false,
      enableStorage: false,
      enableDHT: false,
      enableIdentify: false,
      enableRelayProbing: false,
      allowPrivateAddressDial: true,
    });

    const entBytes = encodeEntitlementRecord({
      entitlementId: 'ent-m6-analysis-1',
      xpub: paidUser.xpub,
      peerId: paidSdnNode.peerId,
      plan: 'analysis',
      status: entitlementLifecycleStatus.Active,
      statusText: 'active',
    });
    const entRes = await fetch(`${baseUrl}/api/v1/admin/entitlements`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-flatbuffers' },
      body: entBytes,
    });
    assert.equal(entRes.status, 200, `POST /api/v1/admin/entitlements failed (${entRes.status})`);

    const paidCookie = await signInWalletSession(baseUrl, paidUser);
    const streamedChunks = [];
    const remoteTmRes = await sdn.invokeRemoteCompute({
      baseUrl,
      moduleId: 'com.digitalarsenal.analysis.threat-matrix',
      methodId: 'assess_threat_matrix',
      inputs: assessInputs,
      traceId: 1001n,
      outputStreamCap: 1,
      headers: { Cookie: paidCookie },
      onFrame: (frame) => {
        streamedChunks.push(frame);
      },
    });
    assert.equal(remoteTmRes.ok, true, 'Subscribed assess_threat_matrix compute invoke must succeed');
    assert.equal(remoteTmRes.moduleId, 'com.digitalarsenal.analysis.threat-matrix');
    assert.match(
      remoteTmRes.wasmSha256 ?? '',
      /^[0-9a-f]{64}$/,
      'X-SDN-Wasm-Sha256 must carry the 64-char hex portable WASM content hash',
    );
    assert.ok(expectedHashes['com.digitalarsenal.analysis.threat-matrix']);
    assert.equal(streamedChunks.length, 3, 'Must progressively stream all 3 $TMX chunks');
    assertByteEqualFrameArrays('assess_threat_matrix ($TMX)', remoteTmRes.rawFrames, localTmxFrames);
    const decodedChunks = remoteTmRes.rawFrames.map((f) => sdn.decodeTmxFrame(f));
    assert.deepEqual(
      decodedChunks.map((c) => [c.CHUNK_INDEX, c.IS_FINAL_CHUNK]),
      [
        [0, false],
        [1, false],
        [2, true],
      ],
    );

    // Store-backed $THP hydration check (TAB SIZE=0, SCHEMA_NAME=THP.fbs)
    const hydratedInputs = [
      { portId: 'config', schemaName: 'ModuleConfig.fbs', fileIdentifier: 'MCFG', rootType: 'ModuleConfig', payload: tmxMcfgBytes },
      { portId: 'grid', schemaName: 'SCR.fbs', fileIdentifier: '$SCR', rootType: 'SCR', payload: scrBytes },
      { portId: 'request', schemaName: 'TMX.fbs', fileIdentifier: '$TMX', rootType: 'TMX', payload: tmxReqBytes },
      { portId: 'threat_profile', schemaName: 'THP.fbs', fileIdentifier: '$THP', rootType: 'THP', payload: new Uint8Array(0) },
      { portId: 'cdms', schemaName: 'CDM.fbs', fileIdentifier: '$CDM', rootType: 'CDM', payload: cdmBytes },
      { portId: 'sites', schemaName: 'SIT.fbs', fileIdentifier: '$SIT', rootType: 'SIT', payload: siteBytes },
      { portId: 'gateways', schemaName: 'SIT.fbs', fileIdentifier: '$SIT', rootType: 'SIT', payload: gatewayBytes },
    ];
    const hydratedTmRes = await sdn.invokeRemoteCompute({
      baseUrl,
      moduleId: 'com.digitalarsenal.analysis.threat-matrix',
      methodId: 'assess_threat_matrix',
      inputs: hydratedInputs,
      traceId: 1004n,
      outputStreamCap: 1,
      headers: { Cookie: paidCookie },
    });
    assert.equal(hydratedTmRes.ok, true);
    assertByteEqualFrameArrays('assess_threat_matrix (FlatSQL-hydrated $THP)', hydratedTmRes.rawFrames, localTmxFrames);

    // COA Synthesizer parity (synthesize_coas + evaluate_coa)
    const coaWasmBytes = new Uint8Array(
      fs.readFileSync(path.join(pluginRoot, 'coa-synthesizer.wasm')),
    );
    coaHarness = await createBrowserModuleHarness({
      wasmSource: coaWasmBytes,
      surface: 'direct',
      initialMemoryBytes: 16 * 1024 * 1024,
      maximumMemoryBytes: 256 * 1024 * 1024,
    });

    const coaConfigBytes = encodeModuleConfig({
      norad_ids: [61001],
      target_miss_km: 2.0,
      target_pc: 1e-6,
      conjunction_sigma_km: 0.35,
      optimize_avoidance_trades: true,
      avoidance_all_axes: true,
      max_coas: 16,
      deterministic: true,
      measure_wall_time: false,
    });
    const synthInputs = [
      { portId: 'config', schemaName: 'ModuleConfig.fbs', fileIdentifier: 'MCFG', rootType: 'ModuleConfig', payload: coaConfigBytes },
      ...localTmxFrames.map((chunk) => ({
        portId: 'matrix',
        schemaName: 'TMX.fbs',
        fileIdentifier: '$TMX',
        rootType: 'TMX',
        payload: chunk,
      })),
      { portId: 'grid', schemaName: 'SCR.fbs', fileIdentifier: '$SCR', rootType: 'SCR', payload: scrBytes },
      { portId: 'cdms', schemaName: 'CDM.fbs', fileIdentifier: '$CDM', rootType: 'CDM', payload: cdmBytes },
    ];

    const localSynth = await coaHarness.invoke({
      methodId: 'synthesize_coas',
      inputs: toSdkInputs(synthInputs),
    });
    assert.equal(localSynth.statusCode, 0, `Local synthesize_coas failed: ${localSynth.errorMessage}`);
    const localSynthOutputs = localSynth.outputs.map((o) => o.payload);
    assert.ok(
      localSynthOutputs.some((b) => sdn.extractFlatBufferFileIdentifier(b) === '$COA') &&
        localSynthOutputs.some((b) => sdn.extractFlatBufferFileIdentifier(b) === '$DCO'),
      'synthesize_coas must produce both $COA and $DCO frames',
    );

    const remoteSynth = await sdn.invokeRemoteCompute({
      baseUrl,
      moduleId: 'com.digitalarsenal.analysis.coa-synthesizer',
      methodId: 'synthesize_coas',
      inputs: synthInputs,
      traceId: 1002n,
      outputStreamCap: 1,
      headers: { Cookie: paidCookie },
    });
    assert.equal(remoteSynth.ok, true);
    assertByteEqualFrameArrays('synthesize_coas ($COA+$DCO)', remoteSynth.rawFrames, localSynthOutputs);

    const firstCoaFrame = localSynthOutputs.find(
      (b) => sdn.extractFlatBufferFileIdentifier(b) === '$COA',
    );
    const evalInputs = [
      { portId: 'config', schemaName: 'ModuleConfig.fbs', fileIdentifier: 'MCFG', rootType: 'ModuleConfig', payload: coaConfigBytes },
      ...localTmxFrames.map((chunk) => ({
        portId: 'matrix',
        schemaName: 'TMX.fbs',
        fileIdentifier: '$TMX',
        rootType: 'TMX',
        payload: chunk,
      })),
      { portId: 'grid', schemaName: 'SCR.fbs', fileIdentifier: '$SCR', rootType: 'SCR', payload: scrBytes },
      { portId: 'cdms', schemaName: 'CDM.fbs', fileIdentifier: '$CDM', rootType: 'CDM', payload: cdmBytes },
      { portId: 'coa', schemaName: 'COA.fbs', fileIdentifier: '$COA', rootType: 'COA', payload: firstCoaFrame },
    ];

    const localEval = await coaHarness.invoke({
      methodId: 'evaluate_coa',
      inputs: toSdkInputs(evalInputs),
    });
    assert.equal(localEval.statusCode, 0, `Local evaluate_coa failed: ${localEval.errorMessage}`);
    const localEvalOutputs = localEval.outputs.map((o) => o.payload);

    const remoteEval = await sdn.invokeRemoteCompute({
      baseUrl,
      moduleId: 'com.digitalarsenal.analysis.coa-synthesizer',
      methodId: 'evaluate_coa',
      inputs: evalInputs,
      traceId: 1003n,
      outputStreamCap: 1,
      headers: { Cookie: paidCookie },
    });
    assert.equal(remoteEval.ok, true);
    assertByteEqualFrameArrays('evaluate_coa ($DCO)', remoteEval.rawFrames, localEvalOutputs);
    console.log('PASS [3/4] Subscribed SDN Compute Offload Streaming + Byte-for-Byte Parity (TMX, COA, DCO)');

    // =========================================================================
    // [4/4] Real libp2p /space-data-network/compute-invoke/1.0.0 Stream
    // =========================================================================
    const pivBytes = sdn.encodeComputePivotRequest({
      methodId: 'assess_threat_matrix',
      inputs: assessInputs,
      traceId: 2001n,
      outputStreamCap: 1,
    });
    const framedPiv = sdn.encodeSizePrefixedFrames([pivBytes]);

    const freeP2pReply = await freeSdnNode.dialProtocol(
      serverPeerId,
      COMPUTE_INVOKE_PROTOCOL_ID,
      framedPiv,
      [wsDialAddr],
    );
    const freeP2pFrames = [...sdn.iterateSizePrefixedFrames(freeP2pReply)];
    assert.ok(freeP2pFrames.length >= 1, 'Expected $QRP denial frame on unsubscribed p2p stream');
    const freeP2pQrp = sdn.decodeQrpErrorFrame(freeP2pFrames[0]);
    assert.equal(freeP2pQrp?.errorCode, 'subscription_required');

    const paidP2pReply = await paidSdnNode.dialProtocol(
      serverPeerId,
      COMPUTE_INVOKE_PROTOCOL_ID,
      framedPiv,
      [wsDialAddr],
    );
    const paidP2pFrames = [...sdn.iterateSizePrefixedFrames(paidP2pReply)];
    if (paidP2pFrames.length === 1) {
      const maybeErr = sdn.decodeQrpErrorFrame(paidP2pFrames[0]);
      if (maybeErr) {
        throw new Error(
          `libp2p compute-invoke returned $QRP error (${maybeErr.errorCode}): ${maybeErr.message}`,
        );
      }
    }
    assertByteEqualFrameArrays('libp2p compute-invoke ($TMX)', paidP2pFrames, localTmxFrames);
    console.log('PASS [4/4] libp2p /space-data-network/compute-invoke/1.0.0 Stream (fail-closed + byte parity)');

    console.log('PASS thp-compute-offload E2E');
  } finally {
    const withTimeout = (promise, ms = 2000) =>
      Promise.race([
        Promise.resolve(promise).catch(() => {}),
        new Promise((r) => setTimeout(r, ms)),
      ]);
    await Promise.all([
      freeSdnNode ? withTimeout(freeSdnNode.stop?.()) : Promise.resolve(),
      paidSdnNode ? withTimeout(paidSdnNode.stop?.()) : Promise.resolve(),
      tmHarness?.dispose ? withTimeout(tmHarness.dispose()) : Promise.resolve(),
      coaHarness?.dispose ? withTimeout(coaHarness.dispose()) : Promise.resolve(),
    ]);
    if (daemon?.pid) {
      try {
        process.kill(-daemon.pid, 'SIGTERM');
      } catch {
        // already exited
      }
      await new Promise((r) => setTimeout(r, 400));
      try {
        process.kill(-daemon.pid, 'SIGKILL');
      } catch {
        // already exited
      }
    }
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  }
}

main()
  .then(() => {
    process.exit(0);
  })
  .catch((err) => {
    console.error(err);
    process.exit(1);
  });
