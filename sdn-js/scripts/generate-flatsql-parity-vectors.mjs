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

import { loadFlatSQLStandalone } from 'flatsql/standalone';

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

const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  const vectors = JSON.parse(readFileSync(vectorsPath, 'utf8'));
  vectors.expected = await runParityCases(vectors);
  writeFileSync(vectorsPath, `${JSON.stringify(vectors, null, 2)}\n`);
  console.log(`wrote expected outputs to ${vectorsPath}`);
  console.log(JSON.stringify(vectors.expected, null, 2));
}
