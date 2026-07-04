#!/usr/bin/env node
// Loop D.6 — REST-STREAM CLIENT GATE (the client-side mirror of
// sdn-server/scripts/wirespeed-gate.sh). Standing benchmark comparing:
//
//   (a1) consume-only : HttpTransport.queryData (the D.3 flatbuffers-first
//        client) fetching GET /api/v1/data/omm/bulk and iterating the
//        aligned stream with the zero-copy frames() iterator;
//   (a2) stream+ingest: RemoteEpochStreamClient.fetchEpochStream — the same
//        request PLUS ingestFlatBufferStream into a REAL
//        FlatSQLEngineRecordStore (FlatSQL-WASM engine, fresh store per
//        run — the dedupe ledger makes replays no-ops);
//   (b)  baseline     : the SAME bytes fetched from the SAME server with a
//        bare fetch() + arrayBuffer(), discarded;
//   (c)  raw-TCP wire_speed_probe reference: a minimal socket write of the
//        same bytes (reported alongside, NOT part of the gate — mirrors the
//        Go gate's probe protocol: one "wire_speed_probe\n" line, then the
//        payload).
//
// The server is a REAL node:http server in a SEPARATE child process serving
// a PRE-MATERIALIZED engine stream — the body is produced by
// FlatSQLEngineRecordStore.queryEpochRawStream (epoch-nearest profile, the
// SQL proven byte-identical to sdn-server engine_records.go by the D.2
// isomorphism harness) — with the exact C.4 response shape:
//   Content-Type: application/vnd.sdn.flatbuffers.stream
//   ETag: W/"fnv1a64-<16hex>"   (shape-exact; value is an opaque validator)
//   X-SDN-Record-Count / X-SDN-Stream-Format: flatsql-size-prefixed-le-u32
//   If-None-Match match -> 304.
//
// THE GATE (mirrors the server gate's semantics): consume-only (a1) must
// sustain >= 99% of baseline (b) best-run throughput — it gates the
// byte-movement overhead the sdn-js transport adds over a bare fetch of the
// same bytes. Stream+ingest (a2) is REPORTED with a residue breakdown but
// NOT gated: the server gate never included storage materialization either,
// and ingest adds inherently non-transport work (per-frame dedupe-key
// hashing, the copy into wasm linear memory, engine parse+insert).
//
// HONESTY NOTES (read the C.5/C.5b/C.5c/C.9 saga in
// SDN_FLATSQL_REWRITE_LOOP.md before "fixing" a miss here): a loopback
// baseline at 8.6 MB is a handful of milliseconds — both arms are
// noise-dominated, run variance easily exceeds 1%. The server gate reads
// 97.5% best and stays honestly UNFLIPPED. Report the measured %, never
// fudge. Known-miss override (CLEARLY LABELED):
//   SDN_D6_ALLOW_BLOCKED=1 npm run measure:rest-stream
//
// Knobs: SDN_D6_RECORDS (default 29000 — the server gate's 8.6MB/29K
// shape), SDN_D6_RUNS (default 15), SDN_D6_WARMUP (default 5), --json.

import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import http from 'node:http';
import net from 'node:net';
import { performance } from 'node:perf_hooks';
import { fileURLToPath, pathToFileURL } from 'node:url';

import {
  FLATBUFFER_STREAM_CONTENT_TYPE,
  FlatSQLEngineRecordStore,
  HttpTransport,
  RemoteEpochStreamClient,
  decodeFlatSqlSizePrefixedStream,
  flatSqlSizePrefixedStreamInfo,
} from '../dist/index.mjs';

import {
  OMM_STANDARD,
  BENCH_EPOCH_BASE,
  buildBenchCorpus,
  throughputStats,
  recordsPerSecond,
  fmtMBps,
  fmtMs,
  fmtRecs,
  envInt,
  argValue,
} from './lib/d6-bench-common.mjs';

const BULK_PATH = '/api/v1/data/omm/bulk';
// Pinned query epoch: after every corpus record's epoch, so the nearest
// profile is deterministic and the 200/304 replay pair stays byte-stable.
const QUERY_EPOCH = BENCH_EPOCH_BASE + 2 * 86400;

function fnv1a64Hex(bytes) {
  // Canonical FNV-1a 64 (the C.5c host/wasm-parity etag hash), BigInt form —
  // one-time server-side setup cost, never in a timed window.
  const prime = 0x100000001b3n;
  const mask = 0xffffffffffffffffn;
  let hash = 0xcbf29ce484222325n;
  for (let i = 0; i < bytes.length; i += 1) {
    hash ^= BigInt(bytes[i]);
    hash = (hash * prime) & mask;
  }
  return hash.toString(16).padStart(16, '0');
}

// ---------------------------------------------------------------------------
// --serve: child process — REAL engine store -> pre-materialized stream ->
// node:http + raw-TCP servers. Prints one JSON ready-line on stdout.
// ---------------------------------------------------------------------------
async function serve(recordCount) {
  const corpus = buildBenchCorpus(recordCount);
  const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
  await store.ingestFlatBufferStream('OMM', corpus.streamBytes, {
    source: 'celestrak-gp',
    recordKeyPrefix: 'shard:rest-bench',
    persist: false,
  });
  // The C.4 body: epoch-nearest bulk stream out of the REAL engine
  // (byte-identical to the Go server for the same store contents — D.2).
  const stream = store.queryEpochRawStream('OMM', {
    profile: 'nearest',
    epoch: QUERY_EPOCH,
    limit: -1,
    source: 'celestrak-gp',
  });
  const info = flatSqlSizePrefixedStreamInfo(stream);
  if (info.totalRecordCount !== recordCount) {
    throw new Error(`materialized ${info.totalRecordCount}/${recordCount} records`);
  }
  const body = Buffer.from(stream.buffer, stream.byteOffset, stream.byteLength);
  const etag = `W/"fnv1a64-${fnv1a64Hex(stream)}"`;
  const sha256 = createHash('sha256').update(body).digest('hex');
  await store.close();

  const httpServer = http.createServer((req, res) => {
    if (req.method !== 'GET' || !req.url?.startsWith(BULK_PATH)) {
      res.writeHead(404, { 'Content-Type': 'application/json' });
      res.end('{"error":"not found"}');
      return;
    }
    if (req.headers['if-none-match'] === etag) {
      res.writeHead(304, { ETag: etag });
      res.end();
      return;
    }
    res.writeHead(200, {
      'Content-Type': FLATBUFFER_STREAM_CONTENT_TYPE,
      'Content-Length': String(body.byteLength),
      ETag: etag,
      'X-SDN-Record-Count': String(recordCount),
      'X-SDN-Stream-Format': 'flatsql-size-prefixed-le-u32',
    });
    res.end(body);
  });
  const tcpServer = net.createServer((socket) => {
    // wire_speed_probe protocol (Go gate mirror): consume one request line,
    // write the payload, close.
    let consumed = false;
    socket.on('data', (chunk) => {
      if (consumed) return;
      if (chunk.includes(0x0a)) {
        consumed = true;
        socket.end(body);
      }
    });
    socket.on('error', () => {});
  });
  await new Promise((resolve) => httpServer.listen(0, '127.0.0.1', resolve));
  await new Promise((resolve) => tcpServer.listen(0, '127.0.0.1', resolve));
  process.stdout.write(`${JSON.stringify({
    ready: true,
    httpPort: httpServer.address().port,
    tcpPort: tcpServer.address().port,
    byteLength: body.byteLength,
    recordCount,
    etag,
    sha256,
  })}\n`);
  // Stay alive until the parent closes stdin (or kills us).
  process.stdin.resume();
  process.stdin.on('end', () => process.exit(0));
}

// ---------------------------------------------------------------------------
// Orchestrator (default mode)
// ---------------------------------------------------------------------------

async function spawnServer(recordCount) {
  const child = spawn(process.execPath, [fileURLToPath(import.meta.url), '--serve', '--records', String(recordCount)], {
    stdio: ['pipe', 'pipe', 'inherit'],
  });
  const ready = await new Promise((resolve, reject) => {
    let buffer = '';
    const timer = setTimeout(() => reject(new Error('benchmark server did not become ready within 120s')), 120_000);
    child.stdout.on('data', (chunk) => {
      buffer += chunk.toString();
      const line = buffer.split('\n').find((l) => l.includes('"ready":true'));
      if (line) {
        clearTimeout(timer);
        resolve(JSON.parse(line));
      }
    });
    child.on('exit', (code) => {
      clearTimeout(timer);
      reject(new Error(`benchmark server exited early (code ${code})`));
    });
  });
  return { child, ready };
}

async function timed(fn) {
  const started = performance.now();
  const value = await fn();
  return { ms: performance.now() - started, value };
}

/**
 * Interleaved measurement: warm every arm, then run the arms ROUND-ROBIN
 * (baseline_i, consume_i, ... per round) so clock drift, GC pressure and
 * connection-pool warmth hit all arms equally — sequential blocks let the
 * later arm ride a warmer process and bias the ratio either way (observed
 * both directions on loopback before interleaving).
 */
async function measureInterleaved({ warmup, runs }, arms) {
  for (const arm of arms) {
    for (let i = 0; i < warmup; i += 1) await arm.fn();
  }
  const durations = new Map(arms.map((arm) => [arm.name, []]));
  for (let round = 0; round < runs; round += 1) {
    for (const arm of arms) {
      // Each arm times its own measured section (setup like store opens
      // stays outside the reported duration).
      durations.get(arm.name).push(await arm.fn());
    }
  }
  return durations;
}

function rawTcpProbe(port, expectedBytes) {
  return new Promise((resolve, reject) => {
    const socket = net.connect(port, '127.0.0.1', () => {
      socket.write('wire_speed_probe\n');
    });
    let received = 0;
    socket.on('data', (chunk) => {
      received += chunk.byteLength;
    });
    socket.on('end', () => {
      if (received !== expectedBytes) {
        reject(new Error(`tcp probe read ${received}/${expectedBytes} bytes`));
        return;
      }
      resolve(received);
    });
    socket.on('error', reject);
  });
}

async function main() {
  const argv = process.argv.slice(2);
  const recordCount = Number.parseInt(argValue(argv, '--records') ?? '', 10) || envInt('SDN_D6_RECORDS', 29000);
  if (argv.includes('--serve')) {
    await serve(recordCount);
    return;
  }
  const runs = Number.parseInt(argValue(argv, '--runs') ?? '', 10) || envInt('SDN_D6_RUNS', 15);
  const warmup = Number.parseInt(argValue(argv, '--warmup') ?? '', 10) || envInt('SDN_D6_WARMUP', 5);
  const jsonOnly = argv.includes('--json');
  const allowBlocked = process.env.SDN_D6_ALLOW_BLOCKED === '1';

  const { child, ready } = await spawnServer(recordCount);
  const exitCode = { value: 0 };
  try {
    const baseUrl = `http://127.0.0.1:${ready.httpPort}`;
    const sizeBytes = ready.byteLength;
    const sizeMB = sizeBytes / (1024 * 1024);
    const cfg = { warmup, runs };
    const request = {
      schema: 'OMM',
      profile: 'nearest',
      epoch: QUERY_EPOCH,
      source: 'celestrak-gp',
    };

    const transport = new HttpTransport(baseUrl);
    let consumeSha = null;

    // ---- arms (measured INTERLEAVED, round-robin per run) -----------------
    // (b) baseline: bare fetch of the same bytes, discarded.
    const baselineArm = async () => {
      const started = performance.now();
      const resp = await fetch(baseUrl + BULK_PATH, {
        headers: { Accept: FLATBUFFER_STREAM_CONTENT_TYPE },
      });
      const buf = await resp.arrayBuffer();
      const ms = performance.now() - started;
      if (buf.byteLength !== sizeBytes) throw new Error(`baseline read ${buf.byteLength}/${sizeBytes} bytes`);
      return ms;
    };

    // (a1) consume-only: queryData + zero-copy frames() iterate.
    const consumeArm = async () => {
      const started = performance.now();
      const result = await transport.queryData(request);
      let frames = 0;
      for (const frame of result.frames()) {
        if (frame.byteLength === 0) throw new Error('empty frame');
        frames += 1;
      }
      const ms = performance.now() - started;
      if (result.stream.byteLength !== sizeBytes || frames !== ready.recordCount || result.recordCount !== ready.recordCount) {
        throw new Error(`consumed ${result.stream.byteLength} bytes / ${frames} frames, header ${result.recordCount}`);
      }
      if (!consumeSha) consumeSha = createHash('sha256').update(result.stream).digest('hex');
      return ms;
    };

    // (a1-fetch) queryData WITHOUT the frame walk — isolates what the
    // transport itself adds over a bare fetch (URL/param build, header
    // reads, the no-copy Uint8Array wrap) from per-frame iteration cost.
    const fetchOnlyArm = async () => {
      const started = performance.now();
      const result = await transport.queryData(request);
      const ms = performance.now() - started;
      if (result.stream.byteLength !== sizeBytes) {
        throw new Error(`queryData read ${result.stream.byteLength}/${sizeBytes} bytes`);
      }
      return ms;
    };

    // (a2) stream+ingest: the REAL public path — RemoteEpochStreamClient.
    // fetchEpochStream (ONE conditional-capable request +
    // ingestFlatBufferStream into a REAL engine store). Fresh store + client
    // per run, opened OUTSIDE the timed section — the content-key dedupe
    // ledger makes replays no-ops, and a cached etag would flip the client
    // to the 304 local path (a different measurement). Node has no
    // IndexedDB — journal flush is structurally absent here (browser flush
    // cost is NOT covered; stated honestly).
    let lastIngestClient = null;
    const ingestArm = async () => {
      const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
      try {
        const client = new RemoteEpochStreamClient(new HttpTransport(baseUrl), store);
        const run = await timed(() => client.fetchEpochStream(request));
        if (run.value.stream.byteLength !== sizeBytes || run.value.ingested !== ready.recordCount || run.value.fromLocalStore) {
          throw new Error(`ingest arm: ${run.value.stream.byteLength} bytes, ${run.value.ingested} records, local=${run.value.fromLocalStore}`);
        }
        if (run.value.etag !== ready.etag) throw new Error('etag did not round-trip');
        // Keep the full conditional path honest on the last store: the
        // follow-up request must 304 and the local-store replay must be
        // byte-identical to the server body (D.2 SQL identity, live).
        if (lastIngestClient === 'verify') {
          const replay = await client.fetchEpochStream(request);
          if (!replay.fromLocalStore) throw new Error('expected a 304 local-store replay');
          const replaySha = createHash('sha256').update(replay.stream).digest('hex');
          if (replaySha !== ready.sha256) {
            throw new Error('304 local replay is not byte-identical to the server body');
          }
          lastIngestClient = 'verified';
        }
        return run.ms;
      } finally {
        await store.close();
      }
    };

    // (c) raw-TCP wire_speed_probe reference.
    const tcpArm = async () => {
      const started = performance.now();
      await rawTcpProbe(ready.tcpPort, sizeBytes);
      return performance.now() - started;
    };

    const durations = await measureInterleaved(cfg, [
      { name: 'baseline', fn: baselineArm },
      { name: 'consume', fn: consumeArm },
      { name: 'fetchOnly', fn: fetchOnlyArm },
      { name: 'ingest', fn: ingestArm },
      { name: 'tcp', fn: tcpArm },
    ]);
    if (consumeSha !== ready.sha256) {
      throw new Error(`consume-arm body sha256 ${consumeSha} != served ${ready.sha256}`);
    }
    // 304/local-replay verification pass (outside the measured rounds).
    lastIngestClient = 'verify';
    await ingestArm();
    if (lastIngestClient !== 'verified') throw new Error('304 replay verification did not run');
    const baselineDurs = durations.get('baseline');
    const consumeDurs = durations.get('consume');
    const fetchOnlyDurs = durations.get('fetchOnly');
    const ingestDurs = durations.get('ingest');
    const tcpDurs = durations.get('tcp');

    // Phase split (3 instrumented runs, reported as medians): the same two
    // calls fetchEpochStream makes, timed individually.
    const ingestPhases = [];
    for (let i = 0; i < 3; i += 1) {
      const store = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
      try {
        const fetched = await timed(() => new HttpTransport(baseUrl).queryData(request));
        const ingested = await timed(() => store.ingestFlatBufferStream('OMM', fetched.value.stream, {
          source: 'celestrak-gp',
          persist: false,
        }));
        if (ingested.value !== ready.recordCount) throw new Error('phase-split ingest mismatch');
        ingestPhases.push({ fetchMs: fetched.ms, ingestMs: ingested.ms });
      } finally {
        await store.close();
      }
    }

    // ---- Residue decomposition (single-shot, approximate) ------------------
    // Where the ingest arm's bytes actually go, measured on the same stream
    // outside the run loop: the u32 walk, the zero-copy frame decode, the
    // per-frame fnv1a32 dedupe-key hash (JS replica, identical math), and a
    // bare engine ingest (copy into wasm linear memory + parse + insert).
    const residResp = await fetch(baseUrl + BULK_PATH, { headers: { Accept: FLATBUFFER_STREAM_CONTENT_TYPE } });
    const residStream = new Uint8Array(await residResp.arrayBuffer());
    const tWalk = await timed(() => flatSqlSizePrefixedStreamInfo(residStream));
    const tDecode = await timed(() => decodeFlatSqlSizePrefixedStream(residStream));
    const frames = tDecode.value;
    const tKeys = await timed(() => {
      let acc = 0;
      for (const frame of frames) {
        let h = 0x811c9dc5;
        for (let j = 0; j < frame.length; j += 1) {
          h ^= frame[j];
          h = Math.imul(h, 0x01000193);
        }
        acc ^= h >>> 0;
      }
      return acc;
    });
    const bareStore = await FlatSQLEngineRecordStore.open({ schemas: [OMM_STANDARD] });
    const tBareIngest = await timed(() => bareStore.ingestFlatBufferStream('OMM', residStream, {
      source: 'celestrak-gp',
      recordKeyPrefix: 'shard:residue',
      persist: false,
    }));
    await bareStore.close();

    // ---- Report + gate ------------------------------------------------------
    const baseline = throughputStats(sizeBytes, baselineDurs);
    const consume = throughputStats(sizeBytes, consumeDurs);
    const fetchOnly = throughputStats(sizeBytes, fetchOnlyDurs);
    const ingest = throughputStats(sizeBytes, ingestDurs);
    const tcp = throughputStats(sizeBytes, tcpDurs);
    const pctBest = (consume.bestMBps / baseline.bestMBps) * 100;
    const pctMedian = (consume.medianMBps / baseline.medianMBps) * 100;
    const fetchOnlyPctBest = (fetchOnly.bestMBps / baseline.bestMBps) * 100;
    const fetchOnlyPctMedian = (fetchOnly.medianMBps / baseline.medianMBps) * 100;
    const ingestPctBest = (ingest.bestMBps / baseline.bestMBps) * 100;
    const ingestPctMedian = (ingest.medianMBps / baseline.medianMBps) * 100;
    const medianOf = (values) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)];
    const fetchMedian = medianOf(ingestPhases.map((p) => p.fetchMs));
    const ingestPhaseMedian = medianOf(ingestPhases.map((p) => p.ingestMs));

    const log = (line) => { if (!jsonOnly) console.log(line); };
    log(`D6 REST-STREAM CLIENT GATE — ${ready.recordCount} records, ${sizeBytes} bytes (${sizeMB.toFixed(1)} MB) per transfer, ${runs} runs (+${warmup} warmup), loopback 127.0.0.1, server=child node:http`);
    log(`D6   (a1) queryData consume : best ${fmtMBps(consume.bestMBps)} (${fmtMs(consume.bestMs)})  median ${fmtMBps(consume.medianMBps)} (${fmtMs(consume.medianMs)})`);
    log(`D6   (a1-fetch) no frame walk: best ${fmtMBps(fetchOnly.bestMBps)} (${fmtMs(fetchOnly.bestMs)})  median ${fmtMBps(fetchOnly.medianMBps)} (${fmtMs(fetchOnly.medianMs)})  = ${fetchOnlyPctBest.toFixed(2)}%/${fetchOnlyPctMedian.toFixed(2)}% of baseline`);
    log(`D6   (a2) stream+ingest     : best ${fmtMBps(ingest.bestMBps)} (${fmtMs(ingest.bestMs)}, ${fmtRecs(recordsPerSecond(ready.recordCount, ingest.bestMs))})  median ${fmtMBps(ingest.medianMBps)} (${fmtMs(ingest.medianMs)}, ${fmtRecs(recordsPerSecond(ready.recordCount, ingest.medianMs))})`);
    log(`D6   (b)  bare-fetch baseline: best ${fmtMBps(baseline.bestMBps)} (${fmtMs(baseline.bestMs)})  median ${fmtMBps(baseline.medianMBps)} (${fmtMs(baseline.medianMs)})`);
    log(`D6   (c)  raw TCP reference : best ${fmtMBps(tcp.bestMBps)} (${fmtMs(tcp.bestMs)})  median ${fmtMBps(tcp.medianMBps)} (${fmtMs(tcp.medianMs)})  (reference only)`);
    log(`D6   gate: consume/baseline = ${pctBest.toFixed(2)}% (best) / ${pctMedian.toFixed(2)}% (median); requirement >= 99%`);
    log(`D6   info: ingest/baseline  = ${ingestPctBest.toFixed(2)}% (best) / ${ingestPctMedian.toFixed(2)}% (median) — NOT gated (storage materialization)`);
    log(`D6   ingest-arm phase medians: fetch ${fmtMs(fetchMedian)} + engine ingest ${fmtMs(ingestPhaseMedian)}`);
    log('D6   residue decomposition (single-shot, approximate; same stream):');
    log(`D6     u32 stream walk      ${fmtMs(tWalk.ms)}`);
    log(`D6     zero-copy frame decode ${fmtMs(tDecode.ms)} (${frames.length} subarray views)`);
    log(`D6     dedupe-key fnv1a32   ${fmtMs(tKeys.ms)} (per-frame content hash, JS)`);
    log(`D6     bare engine ingest   ${fmtMs(tBareIngest.ms)} (one wasm copy-in + parse + insert, prefixed keys)`);

    const report = {
      generatedAt: new Date().toISOString(),
      benchmark: 'rest-stream client gate (loop D.6)',
      transfer: { recordCount: ready.recordCount, byteLength: sizeBytes, sizeMB: Number(sizeMB.toFixed(2)), etag: ready.etag, sha256: ready.sha256 },
      config: { runs, warmup },
      arms: {
        queryDataConsume: consume,
        queryDataFetchOnly: fetchOnly,
        streamPlusIngest: { ...ingest, phaseMedians: { fetchMs: Number(fetchMedian.toFixed(3)), ingestMs: Number(ingestPhaseMedian.toFixed(3)) } },
        bareFetchBaseline: baseline,
        rawTcpReference: tcp,
      },
      gate: {
        requirementPct: 99,
        consumeOfBaselineBestPct: Number(pctBest.toFixed(2)),
        consumeOfBaselineMedianPct: Number(pctMedian.toFixed(2)),
        fetchOnlyOfBaselineBestPct: Number(fetchOnlyPctBest.toFixed(2)),
        fetchOnlyOfBaselineMedianPct: Number(fetchOnlyPctMedian.toFixed(2)),
        ingestOfBaselineBestPct: Number(ingestPctBest.toFixed(2)),
        ingestOfBaselineMedianPct: Number(ingestPctMedian.toFixed(2)),
        pass: pctBest >= 99,
        allowBlockedOverride: allowBlocked,
      },
      residueSingleShotMs: {
        u32Walk: Number(tWalk.ms.toFixed(3)),
        frameDecode: Number(tDecode.ms.toFixed(3)),
        dedupeKeyFnv1a32: Number(tKeys.ms.toFixed(3)),
        bareEngineIngest: Number(tBareIngest.ms.toFixed(3)),
      },
    };
    if (jsonOnly) console.log(JSON.stringify(report, null, 2));
    else console.log(JSON.stringify(report, null, 2));

    if (pctBest >= 99) {
      log(`D6 GATE: PASS (${pctBest.toFixed(2)}% of baseline)`);
    } else {
      const msg = `D6 GATE: FAIL — queryData stream consumption is ${pctBest.toFixed(2)}% (best) / ${pctMedian.toFixed(2)}% (median) of the bare-fetch baseline, requirement >= 99%.`;
      if (allowBlocked) {
        log(msg);
        log('D6 GATE OVERRIDE: SDN_D6_ALLOW_BLOCKED=1 set — reporting the known-miss state instead of failing '
          + '(loopback transfers this small are noise-dominated: both arms share fetch + arrayBuffer; the only '
          + 'added client work is the Uint8Array wrap, header reads, and the zero-copy u32 frame walk — see the '
          + 'residue decomposition above).');
      } else {
        log(msg);
        exitCode.value = 1;
      }
    }
  } finally {
    child.stdin.end();
    child.kill();
  }
  process.exitCode = exitCode.value;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  main().catch((error) => {
    console.error(error instanceof Error ? (error.stack ?? error.message) : error);
    process.exit(1);
  });
}
