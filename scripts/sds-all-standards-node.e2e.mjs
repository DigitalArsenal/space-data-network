// SDS 1.255.0 on the node, every standard (sdn-sds-1-255-all-standards-20261018):
// the node built from this checkout embeds every published standard byte for
// byte, accepts a publish of a real $CVR record, one record of every standard
// new to the node, a $DPM CAR_ARCHIVE asset with THROUGH_SEQ, and records of
// existing standards, and returns each by its CID with every field intact.
//   node scripts/sds-all-standards-node.e2e.mjs   (needs sdn-js/node_modules: npm ci in sdn-js)
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, readdirSync, writeFileSync, rmSync, mkdirSync, openSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';

const VERSION = '1.255.0';
// Standards the node did not embed before this pin (CVR first, FMB/FCK/FSG new
// at 1.255.0), then existing standards that must still round-trip.
const ADDED = ['ABA', 'ACT', 'AGR', 'APR', 'ASG', 'BPF', 'CLM', 'CSO', 'CVR', 'DAV', 'DCL', 'ERM', 'ETS', 'EVL', 'EXO', 'FCK',
  'FIL', 'FMB', 'FSG', 'GCT', 'GLX', 'LFV', 'MDE', 'MPL', 'NDS', 'NEV', 'NHS', 'NST', 'ODR', 'OIL', 'PNV', 'PPS', 'PSS', 'QRP',
  'RND', 'RPC', 'SBC', 'SCR', 'SKQ', 'SKR', 'SLP', 'SRG', 'TBP', 'TCT', 'TRH', 'WHA', 'XMT'];
const EXISTING = ['OMM', 'CAT', 'CDM', 'OCM', 'STR', 'SKT', 'DPM'];

const root = fileURLToPath(new URL('../', import.meta.url));
const server = join(root, 'sdn-server');
const installed = join(root, 'sdn-js/node_modules');
const sds = join(installed, 'spacedatastandards.org');
const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex');
const ok = (line) => console.log(`PASS ${line}`);
const env = { ...process.env, WASMEDGE_DIR: join(process.env.HOME, '.sdn-wasmedge-static-macos-patched/prefix') };
for (const name of ['CGO_CFLAGS', 'CGO_CXXFLAGS', 'CGO_LDFLAGS']) delete env[name];

// 1. The pins and the embeds: every published standard, byte for byte.
assert.equal(JSON.parse(readFileSync(join(sds, 'package.json'))).version.split('+')[0], VERSION);
const go = (args, timeout = 900000) => {
  const r = spawnSync('go', args, { cwd: server, encoding: 'utf8', timeout, env });
  assert.ifError(r.error);
  assert.equal(r.status, 0, r.stdout + r.stderr);
  return r.stdout;
};
const mod = JSON.parse(go(['list', '-m', '-json', 'github.com/DigitalArsenal/spacedatastandards.org/lib/go']));
assert.equal(mod.Version, `v${VERSION}`);
assert.equal(mod.Replace, undefined, 'the published Go module, without a local replacement');
const metadata = join(server, 'internal/sds');
const published = readdirSync(join(sds, 'schema'), { withFileTypes: true }).filter((d) => d.isDirectory()).map((d) => d.name);
for (const name of published) {
  assert.equal(sha256(readFileSync(join(metadata, 'schemas', `${name}.fbs`))), sha256(readFileSync(join(sds, 'schema', name, 'main.fbs'))), `${name}.fbs embed`);
}
const manifest = JSON.parse(readFileSync(join(metadata, 'search-schemas/manifest.json')));
assert.equal(manifest.sdsVersion, VERSION, 'search metadata from the published dependency');
for (const [output, digest] of Object.entries(manifest.outputs)) {
  assert.equal(sha256(readFileSync(join(metadata, 'search-schemas', output))), digest, output);
}
ok(`SDS ${VERSION}: package, Go module, all ${published.length} standards embedded byte-identical, ${Object.keys(manifest.outputs).length} search schemas`);

// 2. The records, built with the published JS bindings.
const load = (p) => import(pathToFileURL(p).href);
const flatbuffers = await load(join(installed, 'flatbuffers/js/flatbuffers.js'));
const idlOf = (code) => readFileSync(join(sds, 'schema', code, 'main.fbs'), 'utf8');
const rootOf = (idl) => idl.match(/^\s*root_type\s+(\w+)\s*;/m)?.[1];
const identOf = (idl) => idl.match(/^\s*file_identifier\s+"([^"]+)"\s*;/m)?.[1];
// Field declarations of `table <name>` in an IDL, comments stripped.
const tableFields = (idl, name) => {
  const start = idl.search(new RegExp(`^table\\s+${name}\\s*\\{`, 'm'));
  if (start < 0) return null;
  const body = idl.slice(idl.indexOf('{', start) + 1, idl.indexOf('\n}', start));
  return body.split('\n').map((l) => l.replace(/\/\/.*$/, '').trim()).filter(Boolean)
    .map((l) => l.match(/^(\w+)\s*:\s*([^=;(]+?)\s*(?:=[^;(]*)?(\([^)]*\))?\s*;/)).filter(Boolean)
    .map(([, field, type, attrs]) => ({ field, type: type.trim(), required: /\brequired\b/.test(attrs ?? '') }));
};
const includeClosure = (code, seen = new Set()) => {
  if (seen.has(code)) return seen;
  seen.add(code);
  for (const [, dep] of idlOf(code).matchAll(/include\s+"\.\.\/(\w+)\/main\.fbs"/g)) includeClosure(dep, seen);
  return seen;
};
const findTable = (code, type) => {
  for (const dep of includeClosure(code)) { const f = tableFields(idlOf(dep), type); if (f) return f; }
  return null;
};
// A minimal valid record: every required field set (recursively), the first
// string field carries a marker so the round trip proves real content.
const fill = (code, m, type, marker) => {
  const object = new m[`${type}T`]();
  const fields = findTable(code, type) ?? [];
  let marked = false;
  for (const { field, type: t, required } of fields) {
    const vector = t.startsWith('[');
    const inner = vector ? t.slice(1, -1).trim() : t;
    if (!required && !(t === 'string' && marker && !marked)) continue;
    let value;
    if (inner === 'string') value = marker && !marked ? `fixture:${marker}` : 'fixture';
    else if (/^u?(byte|int8|uint8)$/.test(inner)) value = [1, 2, 3];
    else if (findTable(code, inner)) value = fill(code, m, inner, null);
    else continue;
    if (inner === 'string' && marker && !marked) marked = true;
    object[field] = vector && !Array.isArray(value) ? [value] : value;
  }
  return object;
};
const modules = {};
const record = async (code, configure) => {
  const m = modules[code] ??= await load(join(sds, 'lib/js', code, 'main.js'));
  const idl = idlOf(code);
  const type = rootOf(idl);
  const object = fill(code, m, type, code);
  configure?.(object, m);
  const b = new flatbuffers.Builder(1024);
  b.finish(object.pack(b), identOf(idl));
  return { code, type, m, bytes: b.asUint8Array(), object };
};
const plain = (o) => JSON.parse(JSON.stringify(o, (k, v) => (typeof v === 'bigint' ? `${v}n` : v)));

const records = [];
// The real $CVR: the realism test of one error-model cell (the RPO coast's
// covariance in the Cosmos 2612 event bundle states one like it).
records.push(await record('CVR', (cvr, m) => Object.assign(cvr, {
  RULE: m.covarianceRule.PooledModel, MODEL_ID: 'sdn-error-model:leo-450-650km:v3', REGIME: 'LEO 450-650 km, drag-dominated',
  AGE_DAYS: 1.5, HORIZON_DAYS: 3, MEASURED_LINEAGE: 'OMM GP fits vs SP ephemeris, 2026-09-01/2026-10-01', TEST_WINDOW: '2026-10-01/2026-10-15',
  MEAN_D2_OVER_3: 1.04, COVERAGE_95: 0.947, SAMPLES: 18432, OBJECTS: 612, SCALE_FACTOR: 1.18,
  CALIBRATION: m.covarianceCalibration.Calibrated, EVIDENCE: 'bafkreifixturecvrevidence',
})));
for (const code of ADDED.filter((c) => c !== 'CVR')) {
  if (!identOf(idlOf(code))) { ok(`${code}: no file_identifier, embedded for its include closure only`); continue; }
  records.push(await record(code));
}
for (const code of EXISTING.filter((c) => c !== 'DPM')) records.push(await record(code));
records.push(await record('DPM', (dpm, m) => {
  const asset = Object.assign(new m.DPMAssetT(), { ASSET_KIND: m.publicationAssetKind.CAR_ARCHIVE, CID: 'bafybeifixturecarroot',
    DATA_ROOT: 'bafybeifixturecarroot', BYTE_LENGTH: 1048576n, THROUGH_SEQ: 987654321n });
  dpm.ASSETS = [asset];
}));
const dpm = records.at(-1);
assert.ok(dpm.object.ASSETS?.[0]?.THROUGH_SEQ === 987654321n, 'DPM carries a CAR_ARCHIVE asset with THROUGH_SEQ');

// 3. The node built from this checkout, a fresh store, a loopback publish lane.
const freePort = () => new Promise((resolve, reject) => {
  const s = createServer();
  s.once('error', reject);
  s.listen(0, '127.0.0.1', () => { const { port } = s.address(); s.close(() => resolve(port)); });
});
const dir = mkdtempSync(join(tmpdir(), 'sds-all-standards-node-'));
const binary = join(dir, 'spacedatanetwork');
go(['build', '-o', binary, './cmd/spacedatanetwork']);
const port = { tcp: await freePort(), admin: await freePort(), lane: await freePort(), kubo: await freePort(), gateway: await freePort() };
mkdirSync(join(dir, 'kubo'));
const allowed = records.flatMap(({ code }) => [code, `${code}.fbs`]);
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
  publishing: { enabled: true, allowed_schemas: allowed, local_publish_addr: `127.0.0.1:${port.lane}`, max_record_bytes: 1 << 20 },
  ingest: { enabled: false, spacetrack_enabled: false, udl_enabled: false },
  flows: { enabled: false },
  update: { enabled: false },
}, null, 1));
const log = openSync(join(dir, 'sdn.log'), 'a');
const child = spawn(binary, ['daemon', '--allow-multi-daemon', '--config', join(dir, 'config.json')], { cwd: dir, stdio: ['ignore', log, log], env });
const admin = `http://127.0.0.1:${port.admin}`;
const failures = [];
try {
  const deadline = Date.now() + 180000;
  for (;;) {
    try { if ((await fetch(`${admin}/api/node/info`)).ok) break; } catch { /* starting */ }
    if (child.exitCode !== null) throw new Error(`the node exited (${child.exitCode}): ${readFileSync(join(dir, 'sdn.log'), 'utf8').slice(-2000)}`);
    if (Date.now() > deadline) throw new Error('the node did not start within 180 s');
    await new Promise((r) => setTimeout(r, 500));
  }
  ok(`the node built from this checkout is up (pid ${child.pid})`);

  // 4. Publish each; the node stores it and returns it by its CID, every field intact.
  for (const { code, type, m, bytes, object } of records) {
    try {
      const res = await fetch(`http://127.0.0.1:${port.lane}/api/v1/data/publish/${code}.fbs`, { method: 'POST', headers: { 'content-type': 'application/octet-stream' }, body: bytes });
      const text = await res.text();
      assert.equal(res.status, 201, `${code} publish: HTTP ${res.status} ${text.slice(0, 300)}`);
      const { cid } = JSON.parse(text);
      assert.ok(cid, `${code}: publish answered a CID`);
      const got = await fetch(`${admin}/api/v1/data/records/${code}.fbs/${encodeURIComponent(cid)}`);
      assert.equal(got.status, 200, `${code} get by CID: HTTP ${got.status} ${(await got.clone().text()).slice(0, 300)}`);
      const back = new Uint8Array(await got.arrayBuffer());
      assert.equal(Buffer.from(back).toString('latin1', 4, 8), identOf(idlOf(code)), `${code} identifier`);
      const decoded = plain(m[type][`getRootAs${type}`](new flatbuffers.ByteBuffer(back)).unpack());
      assert.deepEqual(decoded, plain(object), `${code}: every field round-trips`);
      ok(`${code}: published (${bytes.length} B), stored and returned by CID ${cid}, ${Buffer.compare(Buffer.from(back), Buffer.from(bytes)) === 0 ? 'byte-identical' : 'fields identical'}`);
    } catch (error) {
      failures.push(`${code}: ${error.message.split('\n')[0]}`);
    }
  }
} finally {
  child.kill('SIGTERM');
  await Promise.race([new Promise((r) => child.once('exit', r)), new Promise((r) => setTimeout(r, 30000))]);
  if (failures.length) console.error(readFileSync(join(dir, 'sdn.log'), 'utf8').split('\n').filter((l) => /ERROR|WARN/.test(l)).slice(-20).join('\n'));
  rmSync(dir, { recursive: true, force: true });
}
assert.deepEqual(failures, [], `records that did not round-trip:\n${failures.join('\n')}`);
console.log(`PASS SDS ${VERSION}: ${records.length} records (CVR, ${ADDED.length - 1} more added standards, DPM CAR_ARCHIVE, existing standards) round-trip through the node by CID`);
