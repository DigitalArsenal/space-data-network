#!/usr/bin/env node
// end_to_end: cmd node sdn-js/scripts/published-package-sync2.e2e.mjs
//
// The published @spacedatanetwork/sdn-js speaks flatsql-sync 2.0. The script installs
// the version in sdn-js/package.json from the npm registry into an empty temp project
// and, from that install alone, lists host-01's publications for
// STR.fbs orbpro-star-catalog/sky2000-l0 over /space-data-network/flatsql-sync/2.0.0/list-published.
//
// SDN_JS_E2E_SPEC=<npm spec or tarball path> installs that instead of the registry version.
// Exit 0 = pass, 1 = fail.
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HOST = '16Uiu2HAmNpWvv4aG4U67cex3LfUJpdrXKXbvcxn6Gm33RPM3MTCf';
const ADDR = `/dns4/sdn.spaceaware.io/tcp/443/tls/ws/p2p/${HOST}`;
const LANE = { schema: 'STR.fbs', providerId: 'orbpro-star-catalog', sourceName: 'sky2000-l0' };
const LIST_PUBLISHED_2_0 = '/space-data-network/flatsql-sync/2.0.0/list-published';

const self = fileURLToPath(import.meta.url);

if (process.argv[2] === '--probe') {
  await probe(process.argv[3]);
} else {
  await main();
}

async function main() {
  const pkg = JSON.parse(fs.readFileSync(path.join(path.dirname(self), '..', 'package.json'), 'utf8'));
  const spec = process.env.SDN_JS_E2E_SPEC || `${pkg.name}@${pkg.version}`;
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'sdn-js-sync2-e2e-'));
  try {
    fs.writeFileSync(path.join(tmp, 'package.json'), JSON.stringify({ name: 'sdn-js-sync2-e2e', private: true, type: 'module' }));
    console.log(`install ${spec} into ${tmp}`);
    const install = spawnSync('npm', ['install', '--no-audit', '--no-fund', '--ignore-scripts', spec], { cwd: tmp, encoding: 'utf8', timeout: 300000 });
    if (install.status !== 0) fail(`npm install ${spec} failed (exit ${install.status}): ${`${install.stdout}${install.stderr}`.trim().split('\n').slice(-6).join(' | ')}`);

    const installed = JSON.parse(fs.readFileSync(path.join(tmp, 'node_modules', pkg.name, 'package.json'), 'utf8'));
    if (!process.env.SDN_JS_E2E_SPEC && installed.version !== pkg.version) fail(`installed ${installed.version}, expected ${pkg.version}`);
    console.log(`installed ${installed.name}@${installed.version}`);

    const probeFile = path.join(tmp, 'probe.mjs');
    fs.copyFileSync(self, probeFile);
    const run = spawnSync(process.execPath, [probeFile, '--probe', pkg.name], { cwd: tmp, encoding: 'utf8', timeout: 120000 });
    process.stdout.write(run.stdout || '');
    process.stderr.write(run.stderr || '');
    if (run.status !== 0) fail(`probe exited ${run.status}`);
    const line = (run.stdout || '').trim().split('\n').filter((l) => l.startsWith('RESULT ')).pop();
    if (!line) fail('probe printed no RESULT line');
    const result = JSON.parse(line.slice('RESULT '.length));
    if (!(result.publications >= 1)) fail(`list-published returned ${result.publications} publications`);
    if (result.protocol !== LIST_PUBLISHED_2_0) fail(`dialed ${result.protocol}, expected ${LIST_PUBLISHED_2_0}`);
    console.log(`PASS ${installed.name}@${installed.version}: ${result.publications} publication(s) for ${LANE.schema} ${LANE.providerId}/${LANE.sourceName} from host-01 over ${result.protocol} in ${result.ms} ms (first batch ${result.firstBatchId}, ${result.firstRecordCount} records)`);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

function fail(message) {
  console.error(`FAIL ${message}`);
  process.exit(1);
}

// Runs from the temp project, so every bare import resolves from its node_modules: the
// installed package and its own dependencies, nothing from this repository.
async function probe(pkgName) {
  const guard = setTimeout(() => { console.error('FAIL probe timed out after 100 s'); process.exit(1); }, 100000);
  let node;
  try {
    const sdn = await import(pkgName);
    for (const name of ['requestFlatSqlPublishedList', 'requestFlatSqlSyncChunk', 'requestFlatSqlSyncManifest']) {
      if (typeof sdn[name] !== 'function') throw new Error(`${pkgName} does not export ${name}`);
    }
    if (sdn.FLATSQL_SYNC_LIST_PUBLISHED_PROTOCOL_ID !== LIST_PUBLISHED_2_0) throw new Error(`FLATSQL_SYNC_LIST_PUBLISHED_PROTOCOL_ID is ${sdn.FLATSQL_SYNC_LIST_PUBLISHED_PROTOCOL_ID}`);
    for (const name of ['FLATSQL_SYNC_PROTOCOL_ID', 'encodeFlatSqlSyncRequest']) {
      if (name in sdn) throw new Error(`${pkgName} still exports the 1.0 ${name}`);
    }
    const { multiaddr } = await import('@multiformats/multiaddr');

    node = await sdn.createHeliaSDNNode({ edgeRelays: [ADDR], enableAutoDial: false, enableDHT: false, enableIdentify: true, allowPrivateAddressDial: true });
    try { await node.libp2p.dial(multiaddr(ADDR)); } catch (error) { console.log(`dial: ${error.message}`); }
    const deadline = Date.now() + 30000;
    const connection = () => node.libp2p.getConnections().find((c) => String(c.remotePeer) === HOST);
    while (!connection() && Date.now() < deadline) await new Promise((r) => setTimeout(r, 300));
    const peer = connection()?.remotePeer;
    if (!peer) throw new Error(`no connection to ${HOST} within 30 s`);

    let protocol = '';
    const transport = {
      async dialProtocol(_target, protocolId, payload) {
        protocol = protocolId;
        const stream = await node.libp2p.dialProtocol(peer, protocolId);
        const chunks = [];
        const reading = (async () => { for await (const c of stream) chunks.push(c instanceof Uint8Array ? c : c.subarray()); })();
        if (!stream.send(payload)) await stream.onDrain?.();
        await stream.close();
        await reading;
        return new Uint8Array(Buffer.concat(chunks));
      },
    };
    const t0 = Date.now();
    const list = await sdn.requestFlatSqlPublishedList(transport, { targetPeerId: HOST, ...LANE, cursor: '', limit: 50 });
    const first = list.publications[0];
    console.log(`RESULT ${JSON.stringify({ publications: list.publications.length, protocol, ms: Date.now() - t0, firstBatchId: first?.batchId ?? null, firstRecordCount: first?.recordCount ?? null })}`);
  } catch (error) {
    console.error(`FAIL ${error.message}`);
    process.exitCode = 1;
  } finally {
    clearTimeout(guard);
    try { await node?.stop?.(); } catch { /* the node is going away */ }
    process.exit(process.exitCode ?? 0);
  }
}
