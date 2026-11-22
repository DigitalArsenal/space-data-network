// Prove the publication on a SCRATCH node: a node of its own (own throwaway key, loopback, no bootstrap), the records
// published to it, signed into dataset publications by that node, read back over the query API and the flatsql-sync wire
// listing, and compared field by field (check.mjs).
//
//   SDS_PIN=<spacedatastandards.org at the pin> SDN_NODE_BINARY=<built node> node local-proof.mjs <work-dir> [--keep]
//
// <work-dir> belongs under /Volumes/ramdisk (a flush-heavy node run freezes the desktop on the internal disk).
// --keep leaves the node running and writes <work-dir>/node.json (pid, admin, ws multiaddr, peer id) for a console run; stop it by that PID.
// The two commands below are the SAME two calls the production step makes (publish-host.md), against another address.
import { spawn, spawnSync } from "node:child_process";
import { mkdirSync, openSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import net from "node:net";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import { NETWORKS, RESENT, frame, lanesOf } from "./build-records.mjs";

// One lane pair per network: provider `sdn-ssa-<network>`, so a network stands alone on the node.
// Three passes, as on the production node: round 1 (the publication of 2026-11-18), round 2 appended to the same lanes and to new
// ones (2026-11-19), round 3 a lane sent again whole (RESENT: the LeoLabs radars with their outlines, 2026-11-20).
// `total` is what the lane holds after the batch: the node signs the whole lane, so the signed count is the total, not the batch.
const lanesFrom = (builder, rounds) => rounds.flatMap((round) => builder.NETWORKS.flatMap((network) => builder.lanesOf(network, round).map((lane) => {
  const whole = builder.lanesOf(network).find((l) => l.schema === lane.schema);
  const total = round === 1 ? lane.count : whole.count;
  return { ...lane, network: network.slug, round, total };
})));
export const LANES = lanesFrom({ NETWORKS, lanesOf }, [1, 2, 3]);

const work = process.argv[2];
const keep = process.argv.includes("--keep");
// --previous <dir>: the recipe as it was when rounds 1 and 2 went out (a copy of its files), so rounds 1 and 2 publish the records
// the production node holds and round 3 is sent over them, as it will be there.
const previousAt = process.argv.indexOf("--previous");
const previous = previousAt > 0 ? await import(pathToFileURL(path.join(path.resolve(process.argv[previousAt + 1]), "build-records.mjs")).href) : null;
const PUBLISHED = previous ? [...lanesFrom(previous, [1, 2]), ...LANES.filter((lane) => lane.round === 3)] : LANES;
if (!work || !process.env.SDN_NODE_BINARY) throw new Error("Usage: SDN_NODE_BINARY=<node> SDS_PIN=<pin> local-proof.mjs <work-dir under /Volumes/ramdisk> [--keep] [--previous <recipe dir of rounds 1 and 2>]");
if (!work.startsWith("/Volumes/ramdisk/")) throw new Error("the work dir must be under /Volumes/ramdisk");
rmSync(work, { recursive: true, force: true });
mkdirSync(path.join(work, "kubo"), { recursive: true });

const freePort = () => new Promise((ok, fail) => {
  const s = net.createServer();
  s.once("error", fail);
  s.listen(0, "127.0.0.1", () => { const { port } = s.address(); s.close(() => ok(port)); });
});
const port = { tcp: await freePort(), ws: await freePort(), admin: await freePort(), lane: await freePort(), kubo: await freePort(), gateway: await freePort() };
const config = {
  mode: "full",
  network: { listen: [`/ip4/127.0.0.1/tcp/${port.tcp}`, `/ip4/127.0.0.1/tcp/${port.ws}/ws`], bootstrap: [], max_connections: 128, enable_relay: false, dht_server: false },
  storage: { path: path.join(work, "data"), max_size: "1GiB" },
  setup: { data_path: work },
  admin: { enabled: true, listen_addr: `127.0.0.1:${port.admin}`, require_auth: true, local_console: true, tls_enabled: false,
    ipfs_api_url: `http://127.0.0.1:${port.kubo}`, ipfs_gateway_url: `http://127.0.0.1:${port.gateway}` },
  asset_pins: { enabled: false, kubo_repo_path: path.join(work, "kubo") },
  peers: { enable_dht: false, enable_mdns: false, trusted_peers: [] },
  schemas: { validate: true, strict: false },
  publishing: { enabled: true, allowed_schemas: ["SIT.fbs", "SNR.fbs", "DPM.fbs"], local_publish_addr: `127.0.0.1:${port.lane}`, max_record_bytes: 1 << 20 },
  ingest: { enabled: false, spacetrack_enabled: false, udl_enabled: false },
  flows: { enabled: false, storage_path: path.join(work, "flows"), services: [], mounts: [] },
  update: { enabled: false },
};
writeFileSync(path.join(work, "config.json"), JSON.stringify(config, null, 1));
const log = openSync(path.join(work, "sdn.log"), "a");
const child = spawn(process.env.SDN_NODE_BINARY, ["daemon", "--allow-multi-daemon", "--config", path.join(work, "config.json")], { cwd: work, stdio: ["ignore", log, log], detached: keep });
console.log(`node pid ${child.pid}`);
const admin = `http://127.0.0.1:${port.admin}`;
let info;
for (const t0 = Date.now(); ; ) {
  try { const r = await fetch(`${admin}/api/node/info`); if (r.ok) { info = await r.json(); break; } } catch { /* not yet */ }
  if (child.exitCode !== null) throw new Error(`the node exited (${child.exitCode}); see ${work}/sdn.log`);
  if (Date.now() - t0 > 240_000) throw new Error("the node did not answer in 240 s");
  await new Promise((r) => setTimeout(r, 500));
}
console.log(`node up: peer ${info.peer_id}`);


const { QRP, QRPT } = await import(pathToFileURL(path.join(process.env.SDS_PIN, "lib/js/QRP/QRP.js")).href);
const { qrpKind } = await import(pathToFileURL(path.join(process.env.SDS_PIN, "lib/js/QRP/qrpKind.js")).href);
const fbPath = createRequire(path.join(process.env.SDS_PIN, "package.json")).resolve("flatbuffers");
const fbm = await import(pathToFileURL(fbPath).href);
const u32 = (bytes, at) => new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getUint32(at, true);
/** The lane's records as the stream check.mjs reads (u32 length + bare record), taken from the node's query answer. */
async function readLane(lane, batchId) {
  const request = Object.assign(new QRPT(), { KIND: qrpKind.Request, SCHEMA_NAME: lane.schema, PROVIDER_ID: lane.providerId, SOURCE_NAME: lane.sourceName, PAGE: 1, LIMIT: 100, ...(batchId ? { BATCH_ID: batchId } : {}) });
  const b = new fbm.Builder(256);
  b.finishSizePrefixed(request.pack(b), "$QRP");
  const answer = await fetch(`${admin}/api/v1/data/query`, { method: "POST", headers: { "Content-Type": "application/vnd.sdn.flatbuffers.stream" }, body: b.asUint8Array() });
  const raw = new Uint8Array(await answer.arrayBuffer());
  if (!answer.ok) throw new Error(`read back ${lane.schema}: HTTP ${answer.status} ${new TextDecoder().decode(raw).replace(/[^\x20-\x7e]/g, " ").slice(0, 300)}`);
  const records = [];
  for (let at = 0; at < raw.length; ) {
    const headerSize = u32(raw, at);
    const page = QRP.getRootAsQRP(new fbm.ByteBuffer(raw.subarray(at + 4, at + 4 + headerSize)));
    at += 4 + headerSize;
    for (let i = 0; i < page.RECORD_COUNT(); i += 1) {
      const n = u32(raw, at);
      records.push({ bytes: raw.subarray(at + 4, at + 4 + n) });
      at += 4 + n;
    }
  }
  return frame(records);
}

let failed = null;
try {
  const readback = path.join(work, "readback");
  mkdirSync(readback, { recursive: true });
  for (const lane of PUBLISHED) {
    // 1. the records, as a batch on the lane's provider/source (the publisher's own loopback lane)
    const body = frame(lane.records);
    const q = new URLSearchParams({ provider_id: lane.providerId, source_name: lane.sourceName, batch_id: `${lane.sourceName}-${lane.round}`, source_url: lane.sourceUrl });
    const put = await fetch(`http://127.0.0.1:${port.lane}/api/v1/data/publish/batch/${lane.schema}?${q}`, { method: "POST", headers: { "Content-Type": "application/vnd.sdn.flatbuffers.stream" }, body });
    const putText = await put.text();
    console.log(`publish.batch round ${lane.round} ${lane.schema} ${lane.sourceName} (${lane.count} records) -> ${put.status} ${putText.slice(0, 200)}`);
    if (!put.ok) throw new Error(`publish ${lane.schema}`);
    // 2. the node signs and announces the lane as a dataset publication
    // A lane sent again whole (RESENT) is signed and read as its own batch: the node keeps every batch a lane was sent, so a
    // whole-lane signing would carry the old records beside the new ones (round 3 of 2026-11-20: 24 records, not 12).
    const batchId = RESENT[lane.round] ? `${lane.sourceName}-${lane.round}` : undefined;
    const sign = await fetch(`${admin}/api/v1/admin/dataset-updates/publish`, { method: "POST", body: JSON.stringify({ schema: lane.schema, providerId: lane.providerId, sourceName: lane.sourceName, ...(batchId ? { batchId } : {}) }) });
    const signed = await sign.json();
    console.log(`dataset-updates/publish ${lane.schema} -> ${sign.status} recordCount=${signed.recordCount} manifestCid=${signed.manifestCid}`);
    if (!sign.ok || signed.recordCount !== (batchId ? lane.count : lane.total)) throw new Error(`sign ${lane.schema}: ${JSON.stringify(signed).slice(0, 300)}`);
    // 3. read back over the query API: one $QRP Request frame in; runs of [$QRP Page][RECORD_COUNT record frames] out
    const bytes = await readLane(lane, batchId);
    console.log(`query ${lane.schema} ${lane.providerId} -> ${bytes.length} bytes`);
    mkdirSync(path.join(readback, lane.network), { recursive: true });
    writeFileSync(path.join(readback, lane.network, lane.file), bytes);
  }
  // 4. the checker reads the node's bytes, not the builder's
  const here = path.dirname(fileURLToPath(import.meta.url));
  const check = spawnSync("node", [path.join(here, "check.mjs"), readback], { encoding: "utf8", env: process.env, maxBuffer: 64 << 20 });
  writeFileSync(path.join(work, "check-readback.log"), check.stdout + check.stderr);
  console.log(check.stdout.trim().split("\n").slice(-1)[0], `(full read-back log: ${work}/check-readback.log)`);
  if (check.status !== 0) throw new Error(`the read-back differs: ${check.stderr.slice(0, 400)}`);
  writeFileSync(path.join(work, "node.json"), JSON.stringify({ pid: child.pid, admin, peerId: info.peer_id, ws: `/ip4/127.0.0.1/tcp/${port.ws}/ws/p2p/${info.peer_id}`, lane: port.lane }, null, 1));
  console.log("LOCAL-PROOF PASS");
} catch (error) {
  failed = error;
  console.error(`LOCAL-PROOF FAIL: ${error.message}`);
} finally {
  if (keep && !failed) child.unref();
  else {
    child.kill("SIGTERM");
    await new Promise((r) => setTimeout(r, 3000));
  }
}
process.exit(failed ? 1 : 0);
