// SDS 1.254.0 on the node, end to end (sdn-embed-str-skt-1-254-20261018):
// the node built from this checkout ingests a $STR row with distances and an
// OCTREE_HELIO_ICRS $SKT node through its loopback publish lane, and search
// (POST /api/v1/data/query, include_data) returns both with every field intact.
//   node scripts/sds-str-skt-node.e2e.mjs   (needs sdn-js/node_modules: npm ci in sdn-js)
// Synthetic wire fixtures: this exercises the embedded standards, not astronomy.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync, rmSync, mkdirSync, openSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';

const VERSION = '1.254.0';
const root = fileURLToPath(new URL('../', import.meta.url));
const server = join(root, 'sdn-server');
const installed = join(root, 'sdn-js/node_modules');
const sds = join(installed, 'spacedatastandards.org');
const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');
const ok = (line) => console.log(`PASS ${line}`);

// 1. The pins: the published package, the published Go module, the embeds.
assert.equal(JSON.parse(readFileSync(join(sds, 'package.json'))).version.split('+')[0], VERSION);
const go = (args, timeout = 600000) => {
  const r = spawnSync('go', args, { cwd: server, encoding: 'utf8', timeout });
  assert.ifError(r.error);
  assert.equal(r.status, 0, r.stdout + r.stderr);
  return r.stdout;
};
const mod = JSON.parse(go(['list', '-m', '-json', 'github.com/DigitalArsenal/spacedatastandards.org/lib/go']));
assert.equal(mod.Version, `v${VERSION}`);
assert.equal(mod.Replace, undefined, 'the published Go module, without a local replacement');
const metadata = join(server, 'internal/sds');
for (const name of ['STR', 'SKT', 'PHB']) {
  assert.equal(sha256(readFileSync(join(metadata, 'schemas', `${name}.fbs`))), sha256(readFileSync(join(sds, 'schema', name, 'main.fbs'))), `${name}.fbs embed`);
}
const manifest = JSON.parse(readFileSync(join(metadata, 'search-schemas/manifest.json')));
assert.equal(manifest.sdsVersion, VERSION, 'search metadata from the published dependency');
for (const output of ['STR.bfbs', 'SKT.bfbs']) {
  assert.equal(sha256(readFileSync(join(metadata, 'search-schemas', output))), manifest.outputs[output], output);
}
ok(`SDS ${VERSION}: package, Go module, STR/SKT/PHB embeds and STR/SKT search schemas`);

// 2. The records, built with the published JS bindings.
const load = (p) => import(pathToFileURL(p).href);
const flatbuffers = await load(join(installed, 'flatbuffers/js/flatbuffers.js'));
const STRm = await load(join(sds, 'lib/js/STR/main.js'));
const SKTm = await load(join(sds, 'lib/js/SKT/main.js'));
const str = Object.assign(new STRm.STRT(), {
  ID: 'fixture:str-distance', GAIA_DR3_SOURCE_ID: 4472832130942575872n, HIP_CAT_ID: 87937, CAT_VERSION: 'fixture',
  STAR_EPOCH: 2016.0, RA: 269.4485, DEC: 4.7398, PARALLAX: 546.98, PARALLAX_UNC: 0.04, PMRA: -801.55, PMDEC: 10362.39,
  GMAG: 8.19, BPMAG: 9.78, RPMAG: 6.99, RADIAL_VELOCITY: -110.4, DISTANCE_PC: 1.8266, DISTANCE_LO_PC: 1.8264,
  DISTANCE_HI_PC: 1.8268, DISTANCE_METHOD: STRm.strDistanceEstimator.BAILER_JONES_2021_PHOTOGEO, DISTANCE_REF: '2021AJ....161..147B',
});
const point = (x, y, z, mag, row) => Object.assign(new SKTm.SKTPoint3DT(), { X_PC: x, Y_PC: y, Z_PC: z, ABS_MAG: mag, COLOR: 34000, DIST_FRAC_UNC: 120, FLAGS: 0, ROW: row });
const skt = Object.assign(new SKTm.SKTT(), {
  LAYER_ID: 'fixture:octree', FRAME: SKTm.sktCelestialFrame.ICRS, PAYLOAD_KIND: SKTm.sktPayloadKind.POINT_SOURCES,
  PARTITION: SKTm.sktPartitionScheme.OCTREE_HELIO_ICRS, OCTREE_LEVEL: 2, OCTREE_KEY: 13n, CENTER_X_PC: -256, CENTER_Y_PC: 256,
  CENTER_Z_PC: 768, HALF_SIZE_PC: 256, ABS_MAG_BRIGHTEST: -1.25, ABS_MAG_FAINTEST: 4.5, POINT_COUNT: 2, EPOCH: 2016.0,
  POINTS_3D: [point(12.5, -3.25, 100.125, -1250, 0), point(-200.5, 17, -0.5, 4500, 1)], CITATION: 'fixture',
});
const encode = (name, type, record) => {
  const b = new flatbuffers.Builder(1024);
  type[`finish${name}Buffer`](b, record.pack(b));
  return b.asUint8Array();
};
const plain = (o) => JSON.parse(JSON.stringify(o, (k, v) => (typeof v === 'bigint' ? `${v}n` : v)));
const records = { STR: encode('STR', STRm.STR, str), SKT: encode('SKT', SKTm.SKT, skt) };
const expected = { STR: plain(str), SKT: plain(skt) };

// 3. The node built from this checkout, a fresh store, a loopback publish lane.
const freePort = () => new Promise((resolve, reject) => {
  const s = createServer();
  s.once('error', reject);
  s.listen(0, '127.0.0.1', () => { const { port } = s.address(); s.close(() => resolve(port)); });
});
const dir = mkdtempSync(join(tmpdir(), 'sds-str-skt-node-'));
const binary = join(dir, 'spacedatanetwork');
go(['build', '-o', binary, './cmd/spacedatanetwork']);
const port = { tcp: await freePort(), admin: await freePort(), lane: await freePort(), kubo: await freePort(), gateway: await freePort() };
mkdirSync(join(dir, 'kubo'));
writeFileSync(join(dir, 'config.json'), JSON.stringify({
  mode: 'full',
  network: { listen: [`/ip4/127.0.0.1/tcp/${port.tcp}`], bootstrap: [], max_connections: 32, enable_relay: false, dht_server: false },
  storage: { path: join(dir, 'data'), max_size: '1GiB' },
  setup: { data_path: dir },
  admin: { enabled: true, listen_addr: `127.0.0.1:${port.admin}`, require_auth: true, dev_auto_admin: true, tls_enabled: false,
    ipfs_api_url: `http://127.0.0.1:${port.kubo}`, ipfs_gateway_url: `http://127.0.0.1:${port.gateway}` },
  // dev_auto_admin admits loopback requests as the first Admin user, so one must exist: the BIP32 spec's
  // test-vector-1 master xpub, a public identifier only.
  users: [{ xpub: 'xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8', trust_level: 'admin', name: 'SDS embed E2E' }],
  asset_pins: { enabled: false, kubo_repo_path: join(dir, 'kubo') },
  peers: { enable_dht: false, enable_mdns: false, trusted_peers: [] },
  schemas: { validate: true, strict: true },
  publishing: { enabled: true, allowed_schemas: ['STR', 'STR.fbs', 'SKT', 'SKT.fbs'], local_publish_addr: `127.0.0.1:${port.lane}`, max_record_bytes: 1 << 20 },
  ingest: { enabled: false, spacetrack_enabled: false, udl_enabled: false },
  flows: { enabled: false },
  update: { enabled: false },
}, null, 1));
const log = openSync(join(dir, 'sdn.log'), 'a');
const child = spawn(binary, ['daemon', '--allow-multi-daemon', '--config', join(dir, 'config.json')], { cwd: dir, stdio: ['ignore', log, log] });
const admin = `http://127.0.0.1:${port.admin}`;
try {
  const deadline = Date.now() + 180000;
  for (;;) {
    try { if ((await fetch(`${admin}/api/node/info`)).ok) break; } catch { /* starting */ }
    if (child.exitCode !== null) throw new Error(`the node exited (${child.exitCode}): ${readFileSync(join(dir, 'sdn.log'), 'utf8').slice(-2000)}`);
    if (Date.now() > deadline) throw new Error('the node did not start within 180 s');
    await new Promise((r) => setTimeout(r, 500));
  }
  ok(`the node built from this checkout is up (pid ${child.pid})`);

  // 4. Ingest both through the lane; search returns both, every field intact.
  for (const name of ['STR', 'SKT']) {
    const res = await fetch(`http://127.0.0.1:${port.lane}/api/v1/data/publish/${name}.fbs`, { method: 'POST', headers: { 'content-type': 'application/octet-stream' }, body: records[name] });
    assert.ok(res.ok, `${name} publish: HTTP ${res.status} ${await res.clone().text()}`);
    const answer = await (await fetch(`${admin}/api/v1/data/query`, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ schema: `${name}.fbs`, include_data: true, limit: 10 }) })).json();
    const rows = answer.results ?? [];
    assert.equal(rows.length, 1, `${name}: one record found (${JSON.stringify(answer).slice(0, 300)})`);
    const bytes = Buffer.from(rows[0].data_base64, 'base64');
    assert.equal(bytes.toString('latin1', 4, 8), `$${name}`, `${name} identifier`);
    const type = name === 'STR' ? STRm.STR : SKTm.SKT;
    const back = plain(type[`getRootAs${name}`](new flatbuffers.ByteBuffer(new Uint8Array(bytes))).unpack());
    assert.deepEqual(back, expected[name], `${name}: every field round-trips`);
    ok(`${name}: ingested (${records[name].length} B) and returned by search with every field intact (${rows[0].cid})`);
  }
} finally {
  child.kill('SIGTERM');
  await Promise.race([new Promise((r) => child.once('exit', r)), new Promise((r) => setTimeout(r, 30000))]);
  rmSync(dir, { recursive: true, force: true });
}
console.log(`PASS SDS ${VERSION} STR distances and the SKT octree partition on the node`);
