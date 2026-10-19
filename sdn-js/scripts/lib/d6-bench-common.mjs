// Loop D.6 — shared helpers for the client-throughput benchmarks
// (scripts/measure-flatsql-sync-throughput.mjs --local-ingest and
// scripts/measure-rest-stream.mjs).
//
// Corpus generation mirrors scripts/generate-flatsql-parity-vectors.mjs:
// deterministic $OMM records built ONLY with the spacedatastandards.org
// GENERATED FlatBuffer builder (never hand-written bindings), concatenated
// into the engine wire format — an aligned stream of size-prefixed
// FlatBuffers ([u32le length][bytes], the length prefix written by
// finishSizePrefixedOMMBuffer doubles as the frame header).

import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';

import * as flatbuffers from 'flatbuffers';
import { OMM } from 'spacedatastandards.org/lib/js/OMM/OMM.js';
import { QRP } from 'spacedatastandards.org/lib/js/QRP/QRP.js';
import { qrpKind } from 'spacedatastandards.org/lib/js/QRP/qrpKind.js';

const require = createRequire(import.meta.url);

export const OMM_SCHEMA = readFileSync(
  require.resolve('spacedatastandards.org/schema/OMM/main.fbs'),
  'utf8',
);

export const OMM_STANDARD = {
  standardId: 'OMM',
  tableName: 'OMM',
  fileId: '$OMM',
  schema: OMM_SCHEMA,
};

// 2026-05-10T00:00:00Z — the same corpus era the D.2 parity vectors use.
export const BENCH_EPOCH_BASE = Date.UTC(2026, 4, 10) / 1000;

function isoUtcSeconds(epochUnix) {
  return new Date(epochUnix * 1000).toISOString().replace('.000Z', 'Z');
}

/**
 * One engine wire frame: a size-prefixed $OMM FlatBuffer (generated
 * builder). Carries a full mean-element set so the frame lands at ~296
 * bytes — the server wirespeed gate's per-record transfer shape
 * (8.6 MB / 29K records).
 */
export function buildOmmFrame({ norad, epochUnix }) {
  const b = new flatbuffers.Builder(1024);
  const nameOff = b.createString(`SAT-${norad}`);
  const objectIdOff = b.createString(`2026-${norad}A`);
  const epochOff = b.createString(isoUtcSeconds(epochUnix));
  const centerOff = b.createString('EARTH');
  const frameOff = b.createString('TEME');
  OMM.startOMM(b);
  OMM.addObjectName(b, nameOff);
  OMM.addObjectId(b, objectIdOff);
  OMM.addCenterName(b, centerOff);
  OMM.addReferenceFrame(b, frameOff);
  OMM.addEpoch(b, epochOff);
  OMM.addNoradCatId(b, norad);
  OMM.addElementSetNo(b, 999);
  OMM.addRevAtEpoch(b, 10000 + (norad % 5000));
  OMM.addMeanMotion(b, 15.5 + (norad % 100) / 1000);
  OMM.addEccentricity(b, 0.0001 + (norad % 10) / 100000);
  OMM.addInclination(b, 53.0 + (norad % 40) / 10);
  OMM.addRaOfAscNode(b, (norad * 7) % 360);
  OMM.addArgOfPericenter(b, (norad * 11) % 360);
  OMM.addMeanAnomaly(b, (norad * 13) % 360);
  OMM.addBstar(b, 0.00012345);
  OMM.addMeanMotionDot(b, 0.00000123);
  OMM.addMeanMotionDdot(b, 0);
  OMM.addSemiMajorAxis(b, 6798.1);
  OMM.addUserDefinedEpochTimestamp(b, epochUnix);
  const off = OMM.endOMM(b);
  OMM.finishSizePrefixedOMMBuffer(b, off);
  return b.asUint8Array().slice();
}

/**
 * Deterministic benchmark corpus: `recordCount` distinct objects, one epoch
 * each (so an epoch-nearest bulk query returns every record — the server
 * wirespeed gate's transfer shape). Returns the aligned stream plus
 * per-record metadata for datasync framing.
 */
export function buildBenchCorpus(recordCount) {
  const frames = [];
  let totalBytes = 0;
  for (let i = 0; i < recordCount; i += 1) {
    const frame = buildOmmFrame({
      norad: 100000 + i,
      epochUnix: BENCH_EPOCH_BASE + (i % 86400),
    });
    frames.push(frame);
    totalBytes += frame.byteLength;
  }
  const streamBytes = new Uint8Array(totalBytes);
  let offset = 0;
  for (const frame of frames) {
    streamBytes.set(frame, offset);
    offset += frame.byteLength;
  }
  return { recordCount, streamBytes, frames };
}

/**
 * A flatsql-sync 2.0.0 read-chunk reply: runs of [$QRP{KIND=Page} header]
 * [RECORD_COUNT size-prefixed record frames], one run per lane.
 * `runs`: [{ providerId, sourceName, batchId, originId, frames }].
 */
export function encodeSyncResponseBytes(page, runs) {
  const parts = [];
  for (const run of runs) {
    const b = new flatbuffers.Builder(256);
    const str = (value) => (value ? b.createString(value) : 0);
    const schema = str(page.schema);
    const cursor = str(page.cursor);
    const nextCursor = str(page.nextCursor);
    const etag = str(page.etag);
    const providerId = str(run.providerId);
    const sourceName = str(run.sourceName);
    const batchId = str(run.batchId);
    const originId = str(run.originId);
    QRP.startQRP(b);
    QRP.addKind(b, qrpKind.Page);
    if (schema) QRP.addSchemaName(b, schema);
    if (providerId) QRP.addProviderId(b, providerId);
    if (sourceName) QRP.addSourceName(b, sourceName);
    if (batchId) QRP.addBatchId(b, batchId);
    if (originId) QRP.addOriginId(b, originId);
    if (cursor) QRP.addCursor(b, cursor);
    if (nextCursor) QRP.addNextCursor(b, nextCursor);
    if (etag) QRP.addEtag(b, etag);
    QRP.addLimit(b, page.limit ?? 0);
    QRP.addRecordCount(b, run.frames.length);
    QRP.addTotalCount(b, BigInt(page.totalCount ?? 0));
    QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
    parts.push(b.asUint8Array().slice(), ...run.frames);
  }
  const bytes = new Uint8Array(parts.reduce((total, part) => total + part.byteLength, 0));
  let offset = 0;
  for (const part of parts) {
    bytes.set(part, offset);
    offset += part.byteLength;
  }
  return bytes;
}

/** Best/median throughput over run durations for a fixed transfer size (server c5Stats mirror). */
export function throughputStats(sizeBytes, runMillis) {
  const sorted = [...runMillis].sort((a, b) => a - b);
  const best = sorted[0];
  const median = sorted[Math.floor(sorted.length / 2)];
  const mbps = (ms) => sizeBytes / (1024 * 1024) / (ms / 1000);
  return {
    sizeBytes,
    runMillis: runMillis.map((ms) => Number(ms.toFixed(3))),
    bestMs: best,
    medianMs: median,
    bestMBps: mbps(best),
    medianMBps: mbps(median),
  };
}

export function recordsPerSecond(recordCount, ms) {
  return recordCount / (ms / 1000);
}

export function fmtMBps(v) {
  return `${v.toFixed(1).padStart(9)} MB/s`;
}

export function fmtMs(v) {
  return `${v.toFixed(2)}ms`;
}

export function fmtRecs(v) {
  if (v >= 1e6) return `${(v / 1e6).toFixed(2)}M rec/s`;
  return `${Math.round(v / 1e3)}K rec/s`;
}

export function envInt(name, def) {
  const v = Number.parseInt(process.env[name] ?? '', 10);
  return Number.isFinite(v) && v > 0 ? v : def;
}

export function argValue(argv, flag) {
  const index = argv.indexOf(flag);
  if (index === -1 || index + 1 >= argv.length) return null;
  return argv[index + 1];
}
