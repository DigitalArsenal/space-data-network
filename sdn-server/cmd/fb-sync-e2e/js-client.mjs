#!/usr/bin/env node
// The sdn-js leg of the fb-sync E2E: the published sdn-js client (dist)
// dials the Go provider over libp2p TCP and reads, over flatsql-sync 2.0.0,
// (1) every read-chunk page of the OMM lanes and (2) the list-published page
// and one read-shard of the published segment. It prints one `key=value`
// line per measurement; main.go compares them with the provider's bytes.
//
//   node js-client.mjs <sdn-js dir> <provider multiaddr> <provider peer id>
//     <published provider id> <published source name> <published batch id>
import { createHash } from 'node:crypto';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

const [sdnJsDir, addr, peer, providerId, sourceName, batchId] = process.argv.slice(2);
const { createDefaultLibp2pFlatSqlSyncClient } = await import(pathToFileURL(join(sdnJsDir, 'dist/ui/index.mjs')).href);

// The provider is on loopback: the only dial the default gater refuses.
const client = await createDefaultLibp2pFlatSqlSyncClient([addr], { requestTimeoutMs: 30_000, allowPrivateAddressDial: true });
const target = { targetPeerId: peer, candidateAddrs: [addr] };
try {
  // (1) read-chunk: the run streams, size prefixes included, in page order.
  const frames = createHash('sha256');
  let records = 0;
  let pages = 0;
  let runs = 0;
  let cursor = '';
  do {
    const chunk = await client.readFlatSqlSyncChunk({ ...target, schema: 'OMM', limit: 1000, ...(cursor ? { cursor } : {}) });
    pages += 1;
    runs += chunk.runs.length;
    for (const run of chunk.runs) frames.update(run.stream);
    records += chunk.records.length;
    cursor = chunk.header.nextCursor;
  } while (cursor && pages < 100);
  console.log(`read-chunk-records=${records}`);
  console.log(`read-chunk-pages=${pages}`);
  console.log(`read-chunk-runs=${runs}`);
  console.log(`read-chunk-frames-sha256=${frames.digest('hex')}`);

  // (2) list-published, then the listed shard's bytes.
  const lane = { ...target, schema: 'OMM.fbs', providerId, sourceName, batchId, queryProfile: 'dataset-publication-offset-v1' };
  const list = await client.listFlatSqlPublished({ ...lane, limit: 10 });
  const publication = list.publications[0];
  console.log(`list-published-total=${list.totalCount}`);
  console.log(`list-published-rows=${list.publications.length}`);
  console.log(`list-published-shard-cid=${publication?.shardCid ?? ''}`);
  console.log(`list-published-shard-sha256=${publication?.shardSha256 ?? ''}`);
  console.log(`list-published-at-ms=${publication?.publishedAtMs ?? 0}`);
  if (publication?.shardCid) {
    const shard = await client.readFlatSqlPublishedShard({ ...lane, cid: publication.shardCid });
    console.log(`read-shard-bytes=${shard.streamBytes.byteLength}`);
    console.log(`read-shard-sha256=${createHash('sha256').update(shard.streamBytes).digest('hex')}`);
  }
} catch (error) {
  console.log(`error=${String(error?.message ?? error).replace(/\s+/g, ' ')}`);
} finally {
  await client.stop?.();
}
// libp2p leaves timers behind after stop(); every result is printed.
process.exit(0);
