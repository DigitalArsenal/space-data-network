#!/usr/bin/env node
// Regenerates the `expected` block of shared-test-vectors/flatsql-parity.json
// by running every case through the JS reference engine (flatsql/standalone).
// The Go host test (sdn-server/internal/flatsqlrt/parity_test.go) and the JS
// host test (sdn-js/src/flatsql-parity.test.ts) both assert these outputs —
// byte-for-byte isomorphism between WasmEdge and V8 hosts.
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

import * as flatbuffers from 'flatbuffers';
import { loadFlatSQLStandalone } from 'flatsql/standalone';
import { initFlatSQL } from 'flatsql/wasm';
import { OMM } from 'spacedatastandards.org/lib/js/OMM/OMM.js';

const vectorsPath = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../shared-test-vectors/flatsql-parity.json',
);

export function decodeParam(p) {
  switch (p.t) {
    case 'null':
      return null;
    case 'bool':
      return p.v;
    case 'i64':
      return p.v;
    case 'f64':
      return p.v;
    case 'str':
      return p.v;
    case 'bytes':
      return Uint8Array.from(Buffer.from(p.v, 'base64'));
    default:
      throw new Error(`unknown param tag: ${p.t}`);
  }
}

export function decodeParams(params) {
  return (params ?? []).map(decodeParam);
}

export async function runParityCases(vectors) {
  const flatsql = await loadFlatSQLStandalone();
  const db = flatsql.createDatabase(vectors.schema, 'parity');
  db.registerFileId(vectors.fileId, vectors.table);
  db.ingest(Uint8Array.from(Buffer.from(vectors.fixtureStreamBase64, 'base64')));

  const rawStreams = {};
  for (const c of vectors.rawStreamCases) {
    const stream = db.queryRawFlatBufferStream(c.sql, decodeParams(c.params));
    rawStreams[c.name] = {
      sha256: createHash('sha256').update(stream).digest('hex'),
      byteLength: stream.length,
    };
  }

  const queryCacheKeys = {};
  for (const c of vectors.queryCacheKeyCases) {
    queryCacheKeys[c.name] = flatsql.buildQueryCacheKey(
      c.dataset,
      c.artifactVersion,
      c.queryId,
      decodeParams(c.params),
    );
  }

  const responseArtifactKeys = {};
  for (const c of vectors.responseArtifactKeyCases) {
    responseArtifactKeys[c.name] = flatsql.buildResponseArtifactCacheKey(
      c.schemaName,
      c.schemaVersion,
      c.sql,
      {
        ...(c.format ? { format: c.format } : {}),
        publishEventKey: c.publishEventKey,
        projection: c.projection,
        params: decodeParams(c.params),
      },
    );
  }

  db.destroy();
  return { rawStreams, queryCacheKeys, responseArtifactKeys };
}

// ---------------------------------------------------------------------------
// Loop D.2 — epoch-profile store parity: a multi-provider $OMM corpus plus
// the server's engine-native epoch profile cases. Corpus and cases are
// generated deterministically here; `engineEpochSql` in the JSON is a
// HAND-MAINTAINED verbatim copy of sdn-server internal/storage/
// engine_records.go (both hosts assert their constants/builders equal it).
// Expected outputs come from the reference flatsql/wasm engine; the JS host
// test then drives the REAL sdn-js store public API
// (sdn-js/src/flatsql-parity.test.ts) and the Go host test the REAL server
// store (sdn-server/internal/storage/engine_records_parity_test.go), both
// asserting these exact bytes.
// ---------------------------------------------------------------------------

// 2026-05-10T00:00:00Z — matches the sdn-server engine_records_test.go era.
const EPOCH_BASE = Date.UTC(2026, 4, 10) / 1000; // 1778371200
const EPOCH_TARGET = EPOCH_BASE + 36 * 3600 + 0.5; // fractional: identical f64 TLV on both hosts
const EPOCH_TARGET_INTEGRAL = EPOCH_BASE + 36 * 3600; // integral: JS binds i64, Go binds f64 — results must still be byte-identical

const EPOCH_STORE_SOURCES = [
  // Two tagged providers plus the untagged default `local` partition.
  { name: 'celestrak-gp', tagged: true, norads: range(1001, 1020), epochs: [EPOCH_BASE, EPOCH_BASE + 2 * 86400, EPOCH_BASE + 4 * 86400] },
  { name: 'provider-two', tagged: true, norads: range(2001, 2010), epochs: [EPOCH_BASE + 3600, EPOCH_BASE + 2 * 86400 + 3600] },
  { name: 'local', tagged: false, norads: range(3001, 3005), epochs: [EPOCH_BASE] },
];

export const EPOCH_STORE_CASES = [
  { name: 'nearest_all', profile: 'nearest', source: '', epoch: EPOCH_TARGET, limit: -1 },
  { name: 'nearest_all_integral_epoch', profile: 'nearest', source: '', epoch: EPOCH_TARGET_INTEGRAL, limit: -1 },
  { name: 'nearest_celestrak', profile: 'nearest', source: 'celestrak-gp', epoch: EPOCH_TARGET, limit: -1 },
  { name: 'nearest_provider_two', profile: 'nearest', source: 'provider-two', epoch: EPOCH_TARGET, limit: -1 },
  { name: 'nearest_local_default_partition', profile: 'nearest', source: 'local', epoch: EPOCH_TARGET, limit: -1 },
  { name: 'nearest_limited', profile: 'nearest', source: '', epoch: EPOCH_TARGET, limit: 7 },
  { name: 'as_of_all', profile: 'as_of', source: '', epoch: EPOCH_TARGET, limit: -1 },
  { name: 'forward_all', profile: 'forward', source: '', epoch: EPOCH_TARGET, limit: -1 },
  { name: 'as_of_before_history_empty', profile: 'as_of', source: '', epoch: EPOCH_BASE - 86400.5, limit: -1 },
];

function range(from, to) {
  return Array.from({ length: to - from + 1 }, (_, i) => from + i);
}

function isoUtcSeconds(epochUnix) {
  return new Date(epochUnix * 1000).toISOString().replace('.000Z', 'Z');
}

// One UNPREFIXED-payload aligned frame: [u32le size][$OMM FlatBuffer] —
// exactly the record shape the engine ingests and streams back
// (spacedatastandards.org GENERATED builder only; no hand-written bindings).
function buildOmmFrame({ norad, epochUnix }) {
  const b = new flatbuffers.Builder(1024);
  const nameOff = b.createString(`SAT-${norad}`);
  const objectIdOff = b.createString(`2026-${norad}A`);
  const epochOff = b.createString(isoUtcSeconds(epochUnix));
  OMM.startOMM(b);
  OMM.addObjectName(b, nameOff);
  OMM.addObjectId(b, objectIdOff);
  OMM.addEpoch(b, epochOff);
  OMM.addNoradCatId(b, norad);
  OMM.addMeanMotion(b, 15.5);
  OMM.addEccentricity(b, 0.0001);
  OMM.addInclination(b, 53.0);
  OMM.addUserDefinedEpochTimestamp(b, epochUnix);
  const off = OMM.endOMM(b);
  OMM.finishSizePrefixedOMMBuffer(b, off);
  return b.asUint8Array();
}

export function buildEpochStoreCorpus() {
  const sources = EPOCH_STORE_SOURCES.map((source) => {
    const frames = [];
    for (const epochUnix of source.epochs) {
      for (const norad of source.norads) {
        frames.push(buildOmmFrame({ norad, epochUnix }));
      }
    }
    const stream = Buffer.concat(frames.map((f) => Buffer.from(f)));
    return {
      name: source.name,
      tagged: source.tagged,
      recordCount: frames.length,
      streamBase64: stream.toString('base64'),
    };
  });
  return { schemaName: 'OMM.fbs', standardId: 'OMM', sources };
}

export function buildEpochStoreDirectCases(engineEpochSql) {
  const orForm = engineEpochSql.nearest;
  // The direct-equality predicate form the D.1 flatsql fix
  // (vtab _source pushdown) covers — must return the same bytes as the
  // server's OR form.
  const pushdownForm = orForm.replace("(?1 = '' OR _source = ?1)", '_source = ?1');
  if (pushdownForm === orForm) {
    throw new Error('failed to derive the pushdown-form SQL from engineEpochSql.nearest');
  }
  const params = [
    { t: 'str', v: 'OMM@celestrak-gp' },
    { t: 'f64', v: EPOCH_TARGET },
    { t: 'i64', v: -1 },
  ];
  return [
    { name: 'direct_or_form_celestrak', sql: orForm, params, equalsCase: 'nearest_celestrak' },
    { name: 'direct_pushdown_celestrak', sql: pushdownForm, params, equalsCase: 'nearest_celestrak' },
  ];
}

function digestAlignedStream(stream) {
  let frameCount = 0;
  const view = new DataView(stream.buffer, stream.byteOffset, stream.byteLength);
  let offset = 0;
  while (offset < stream.byteLength) {
    const length = view.getUint32(offset, true);
    offset += 4 + length;
    frameCount += 1;
  }
  if (offset !== stream.byteLength) {
    throw new Error('misaligned size-prefixed stream');
  }
  return {
    sha256: createHash('sha256').update(stream).digest('hex'),
    byteLength: stream.length,
    frameCount,
  };
}

/**
 * Reference run for the epoch store cases on a bare flatsql/wasm engine
 * database (the standalone build has no source partitions): register the
 * corpus sources, build unified views, ingest per source, execute every
 * profile/direct case, digest the aligned streams.
 */
export async function runEpochStoreCases(vectors) {
  const flatsql = await initFlatSQL({ skipIntegrityCheck: true });
  const db = flatsql.createDatabase(vectors.schema, 'epoch-parity');
  db.registerFileId(vectors.fileId, vectors.table);
  for (const source of vectors.epochStoreCorpus.sources) {
    db.registerSource(source.name);
  }
  db.createUnifiedViews();
  for (const source of vectors.epochStoreCorpus.sources) {
    db.ingest(Uint8Array.from(Buffer.from(source.streamBase64, 'base64')), source.name);
  }

  const out = {};
  for (const c of vectors.epochStoreCases) {
    const sql = vectors.engineEpochSql[c.profile];
    if (!sql) throw new Error(`no engineEpochSql entry for profile ${c.profile}`);
    const shadow = c.source ? `${vectors.table}@${c.source}` : '';
    out[c.name] = digestAlignedStream(db.queryRawFlatBufferStream(sql, [shadow, c.epoch, c.limit]));
  }
  for (const c of vectors.epochStoreDirectCases ?? []) {
    out[c.name] = digestAlignedStream(db.queryRawFlatBufferStream(c.sql, decodeParams(c.params)));
  }
  db.destroy();
  return out;
}

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  const vectors = JSON.parse(readFileSync(vectorsPath, 'utf8'));
  if (!vectors.engineEpochSql) {
    throw new Error('engineEpochSql missing — hand-copy the constants from sdn-server/internal/storage/engine_records.go first');
  }
  vectors.epochStoreCorpus = buildEpochStoreCorpus();
  vectors.epochStoreCases = EPOCH_STORE_CASES;
  vectors.epochStoreDirectCases = buildEpochStoreDirectCases(vectors.engineEpochSql);
  vectors.expected = await runParityCases(vectors);
  vectors.expected.epochStoreStreams = await runEpochStoreCases(vectors);
  writeFileSync(vectorsPath, `${JSON.stringify(vectors, null, 2)}\n`);
  console.log(`wrote expected outputs to ${vectorsPath}`);
  console.log(JSON.stringify(vectors.expected, null, 2));
}
