#!/usr/bin/env node
// The marketing team's tally of what is deployed on the network (docs/marketing/deployed.html; owner 2026-10-22: "as we
// are deploying modules to hosts, we need to keep a running tally of what modules are on what hosts and what they do").
// Read from the network, never typed: for each fleet host
//   - its identity (GET /api/v1/id: peer, build, listen addresses);
//   - the signed module catalog it serves, if any (GET /.well-known/sdn/modules.pmm: the size-prefixed $PMM FlatBuffer);
//   - over SDN, every lane it publishes: list-published (FLATSQL_SYNC_LIST_PUBLISHED_PROTOCOL_ID) for each SDS schema
//     (the node needs a schema per request), and each lane's newest publication manifest ($DPM, by CID) for its source,
//     licence and producer - a lane whose producer is another peer is served here as a mirror.
// "What it does" comes from the module's own description, else the marketing catalog's tagline for a module it names
// (docs/marketing/catalog.json), else its name and family; a lane's from its SDS schema's title and its source.
// Writes docs/marketing/deployments.json (the page's data). Live only: a host that does not answer is listed as such.
//
//   node scripts/build-marketing-deployments.mjs [--sdn-js <dir>] [--out <file>] [--hosts <file>] [--topology <file>]
//     --sdn-js    an installed @spacedatanetwork/sdn-js (built dist/; its flatbuffers and spacedatastandards.org) -
//                 default sdn-js/ in this repo (npm ci && npm run build there first)
//     --hosts     JSON [{ id, name, url, role? }] - default the fleet below
//     --topology  the stack's deployment/topology.json: each host's role from its assignments
// Run on every deploy and daily (the deploy procedures call it; see docs/marketing/deployed.html).
import { createRequire } from 'node:module';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const arg = (name, fallback) => { const i = process.argv.indexOf(`--${name}`); return i >= 0 ? process.argv[i + 1] : fallback; };
const sdnJsDir = path.resolve(arg('sdn-js', path.join(repo, 'sdn-js')));
const outFile = path.resolve(arg('out', path.join(repo, 'docs/marketing/deployments.json')));
const catalogFile = path.join(repo, 'docs/marketing/catalog.json');

// The fleet (deployment/topology.json in the stack names the hosts; their addresses are public endpoints only).
// vm-orbit-det-01 is on a private network: listed, and read only where the run can reach it.
const FLEET = [
  { id: 'host-01', name: 'space-data-network-01', url: 'https://sdn.spaceaware.io', role: 'Module catalog and update server; propagator and solar-system publication' },
  { id: 'host-02', name: 'space-data-network-02', url: 'https://kzwfwjn5ji4purkaknrwvpronfvibl14m4uj4zvolmjz02gajewxm0fo7nsyagx.spacedatanetwork.org', role: 'CelesTrak ingest node' },
  { id: 'vm-orbit-det-01', name: 'vm-orbit-det-01', url: 'http://10.100.10.20:5001', role: 'Orbit determination; weather ingest', private: true },
];
const hosts = arg('hosts') ? JSON.parse(readFileSync(arg('hosts'), 'utf8')) : FLEET;

const entry = path.join(sdnJsDir, 'dist/index.mjs');
if (!existsSync(entry)) { console.error(`no sdn-js build at ${entry} (npm ci && npm run build in sdn-js, or --sdn-js <installed package>)`); process.exit(2); }
const sdn = await import(pathToFileURL(entry));
const require = createRequire(path.join(sdnJsDir, 'package.json'));
const { ByteBuffer } = await import(pathToFileURL(require.resolve('flatbuffers')));
const sdsRoot = path.dirname(require.resolve('spacedatastandards.org/package.json'));
const sds = (name, file = name) => import(pathToFileURL(path.join(sdsRoot, 'lib/js', name, `${file}.js`)));
const { DPM } = await sds('DPM');
const { PMM } = await sds('PMM');
const { pmmAccessPolicy } = await sds('PMM', 'pmmAccessPolicy');
const { pmmEntryState } = await sds('PMM', 'pmmEntryState');
const { pluginCategory } = await sds('PMM', 'pluginCategory');

// Every SDS schema and its title, from its IDL: the comment line naming it ("Orbit Mean Elements Message (OMM)"), else the
// first sentence of the doc comment over its root table, else its code
const standards = JSON.parse(readFileSync(path.join(sdsRoot, 'dist/manifest.json'), 'utf8')).STANDARDS;
const schemaTitle = (code) => {
  const lines = (standards[code]?.IDL || '').split('\n'), named = new RegExp(`^\\/\\/\\/?\\s*(.+?)\\s*\\(\\$?${code}\\)\\s*$`);
  for (const line of lines) { const m = named.exec(line); if (m) return m[1]; }
  const at = lines.findIndex((l) => new RegExp(`^table ${code}\\b`).test(l)); let first = '';
  for (let i = at - 1; i >= 0 && /^\/\/\//.test(lines[i]); i--) first = lines[i].replace(/^\/\/\/\s*/, '');
  const sentence = first.split(/(?<=\.)\s/)[0].replace(/\.$/, '');
  return sentence && sentence.length <= 70 && /[a-z]/.test(sentence) ? sentence : code;
};
const schemas = Object.keys(standards).filter((k) => /^[A-Z][A-Z0-9]{2,3}$/.test(k)).sort();

const marketing = JSON.parse(readFileSync(catalogFile, 'utf8'));
const ACCESS = { [pmmAccessPolicy.ANONYMOUS]: 'free' };
const accessOf = (policy) => ACCESS[policy] ?? (/ENTITLED|PURCHASE|PAID/.test(pmmAccessPolicy[policy] || '') ? 'paid' : 'signed-in');
const words = (s) => String(s || '').toLowerCase().replace(/_/g, ' ').replace(/\b\w/, (c) => c.toUpperCase());
// a marketing module named by a deployed module's id (its last segment, or the whole id)
const marketingFor = (id) => { const tail = id.split('.').pop(); return marketing.modules.find((m) => m.id === tail || m.id === id || tail === m.id.replace(/-/g, '')); };

const timed = (ms, p) => Promise.race([p, new Promise((_, no) => setTimeout(() => no(new Error(`timed out after ${ms / 1000} s`)), ms))]);

async function catalogOf(host) {
  const r = await timed(20000, fetch(`${host.url}/.well-known/sdn/modules.pmm`, { headers: { Accept: 'application/x-flatbuffers' } }));
  if (!r.ok) return null;
  const bytes = new Uint8Array(await r.arrayBuffer());
  const pmm = (String.fromCharCode(...bytes.subarray(8, 12)) === '$PMM' ? PMM.getSizePrefixedRootAsPMM : PMM.getRootAsPMM)(new ByteBuffer(bytes));
  const modules = [];
  for (let i = 0; i < pmm.modulesLength(); i++) {
    const m = pmm.MODULES(i); if (m.ENTRY_STATE() !== pmmEntryState.ACTIVE) continue;
    const id = m.MODULE_ID(), mk = marketingFor(id), name = m.NAME() || mk?.name || id;
    modules.push({ kind: 'module', id, name, version: m.VERSION() || '', family: words(pluginCategory[m.PLUGIN_TYPE()]),
      what: m.DESCRIPTION() || mk?.tagline || `${name}: ${words(pluginCategory[m.PLUGIN_TYPE()])} module`, access: accessOf(m.ACCESS_POLICY()),
      license: m.LICENSE() || '', updated: m.UPDATED_AT() || '' });
  }
  return { provider: pmm.PROVIDER_NAME() || '', updated: pmm.UPDATED_AT() || '', modules };
}

async function lanesOf(host, id) {
  const dns = new URL(host.url).hostname;
  // (its advertised TLS WebSocket listeners first - host-02's <host>:443 name does not connect; private addresses never)
  const addrs = [...(id.listen_addresses || []).filter((a) => /\/tls\/ws|\/wss/.test(a) && !/\/ip4\/(10|127|172\.(1[6-9]|2\d|3[01])|192\.168)\./.test(a)).map((a) => `${a}/p2p/${id.peer_id}`), `/dns4/${dns}/tcp/443/tls/ws/p2p/${id.peer_id}`];
  const node = await sdn.createHeliaSDNNode({ edgeRelays: addrs, enableAutoDial: false, enableDHT: false, enableIdentify: true });
  try {
    let peer; for (let i = 0; i < 30 && !peer; i++) { peer = node.libp2p.getConnections().find((c) => String(c.remotePeer) === id.peer_id)?.remotePeer; if (!peer) await new Promise((r) => setTimeout(r, 1000)); }
    if (!peer) throw new Error('no SDN connection');
    const list = async (schema, cursor) => {
      const stream = await node.libp2p.dialProtocol(peer, sdn.FLATSQL_SYNC_LIST_PUBLISHED_PROTOCOL_ID);
      const chunks = []; const reading = (async () => { for await (const c of stream) chunks.push(c instanceof Uint8Array ? c : c.subarray()); })();
      if (!stream.send(sdn.encodeFlatSqlPublishedListRequest({ schema, cursor, limit: 500 }))) await stream.onDrain?.();
      await stream.close(); await reading;
      return sdn.decodeFlatSqlPublishedList(Buffer.concat(chunks));
    };
    const lanes = new Map();
    for (const code of schemas) {
      let cursor = '';
      do {
        const page = await timed(60000, list(`${code}.fbs`, cursor));
        for (const p of page.publications) {
          const key = `${code}|${p.providerId || ''}|${p.sourceName || ''}`, e = lanes.get(key) || { schema: code, providerId: p.providerId || '', sourceName: p.sourceName || '', publications: 0, records: 0, newest: null };
          e.publications++; e.records += Number(p.recordCount || 0); if (!e.newest || Number(p.publishedAtMs) > Number(e.newest.publishedAtMs)) e.newest = p;
          lanes.set(key, e);
        }
        cursor = page.nextCursor;
      } while (cursor);
    }
    const out = [];
    for (const e of lanes.values()) {
      let source = null, producer = '';
      if (e.newest.manifestCid) {
        try {
          const bytes = Buffer.from(await timed(30000, sdn.fetchCIDBytesFromHelia(node.helia, e.newest.manifestCid, { providerAddrs: addrs, maxBytes: 4 << 20 })));
          const dpm = (bytes.subarray(8, 12).toString() === '$DPM' ? DPM.getSizePrefixedRootAsDPM : DPM.getRootAsDPM)(new ByteBuffer(bytes)).unpack();
          producer = dpm.PROVIDER_PEER_ID || '';
          source = dpm.SOURCES.find((s) => s.SOURCE_NAME === e.sourceName) || dpm.SOURCES[0] || null;
        } catch { /* the lane listed without its manifest's facts */ }
      }
      const title = schemaTitle(e.schema), from = source?.SOURCE_URL ? new URL(source.SOURCE_URL).hostname.replace(/^www\./, '') : e.sourceName || e.providerId;
      out.push({ kind: 'flow', id: `${e.schema}:${e.providerId || '-'}/${e.sourceName || '-'}`, name: e.sourceName || e.providerId || e.schema,
        what: `${/record|message|entry|specification/i.test(title) ? title : `${title} records`} from ${from}`, publishes: { schema: `${e.schema}.fbs`, provider: e.providerId, source: e.sourceName }, access: 'free',
        // (mirrored: its manifest names another producer - or, without one, its provider is another host by name)
        origin: (producer ? producer !== id.peer_id : /^space-data-network-\d+$/.test(e.providerId) && e.providerId !== host.name) ? 'mirrored' : 'produced', producer, records: e.records, publications: e.publications,
        lastPublished: new Date(Number(e.newest.publishedAtMs)).toISOString(), license: source?.LICENSE || '', citation: source?.CITATION || '', sourceUrl: source?.SOURCE_URL || '' });
    }
    return out.sort((a, b) => a.publishes.schema.localeCompare(b.publishes.schema) || a.name.localeCompare(b.name));
  } finally { await node.stop(); }
}

const topology = arg('topology') ? JSON.parse(readFileSync(arg('topology'), 'utf8')) : null;
const roleFrom = (name) => { const nodes = topology?.science_role_assignments_20260906?.nodes || {}; for (const n of Object.values(nodes)) if (n.host === name) return n.desired_workload || n.deployment_state || ''; return ''; };

const started = Date.now(), report = { version: 1, generatedAt: '', generator: 'scripts/build-marketing-deployments.mjs', schemasAsked: schemas.length, hosts: [], items: [] };
for (const host of hosts) {
  const h = { id: host.id, name: host.name, endpoint: host.private ? '' : host.url, role: host.role || roleFrom(host.name), reachable: false, peerId: '', build: '', note: '' };
  report.hosts.push(h);
  try {
    const id = await timed(15000, (await timed(15000, fetch(`${host.url}/api/v1/id`))).json());
    h.reachable = true; h.peerId = id.peer_id; h.build = [id.agent_version, id.bundle_version].filter(Boolean).join(' · ');
    const catalog = await catalogOf(host).catch(() => null);
    if (catalog) { h.catalog = { provider: catalog.provider, updated: catalog.updated, modules: catalog.modules.length }; for (const m of catalog.modules) report.items.push({ host: host.id, ...m }); }
    const lanes = await lanesOf(host, id);
    for (const l of lanes) report.items.push({ host: host.id, ...l });
    h.flows = lanes.filter((l) => l.origin === 'produced').length; h.mirrored = lanes.length - h.flows;
    console.log(`${host.id}: ${catalog ? `${catalog.modules.length} catalog modules, ` : ''}${lanes.length} lanes (${h.flows} produced here)`);
  } catch (e) { h.note = host.private ? 'On a private network: not reachable from where this tally was made' : `Did not answer: ${e.message}`; console.log(`${host.id}: ${h.note}`); }
}
report.generatedAt = new Date().toISOString(); report.ms = Date.now() - started;
writeFileSync(outFile, JSON.stringify(report, null, 1) + '\n');
console.log(`wrote ${path.relative(repo, outFile)}: ${report.items.length} items on ${report.hosts.filter((h) => h.reachable).length} of ${report.hosts.length} hosts, ${(report.ms / 1000).toFixed(0)} s`);
process.exit(0);
