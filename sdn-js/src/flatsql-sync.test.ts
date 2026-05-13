import { describe, expect, it } from 'vitest';
import {
  FLATSQL_SYNC_PROTOCOL_ID,
  decodeFlatSqlSyncChunk,
  decodeFlatSqlSyncDatastores,
  decodeFlatSqlSyncManifest,
  encodeFlatSqlSyncRequest,
  requestFlatSqlSyncDatastores,
  requestFlatSqlWireSpeedProbe,
  requestFlatSqlSyncChunk,
  requestFlatSqlSyncManifest,
  type FlatSqlSyncTransport,
} from './flatsql-sync';

const encoder = new TextEncoder();
const decoder = new TextDecoder();

describe('FlatSQL sync protocol client', () => {
  it('encodes resumable read_chunk requests as a length-prefixed JSON frame', () => {
    const frame = encodeFlatSqlSyncRequest({
      targetPeerId: '16Uiu2HTest',
      schema: 'OMM.fbs',
      datastoreKey: 'sdn-ds-v1-history',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      cursor: 'cursor-1',
      snapshotId: 'snapshot-1',
      head: 'head-1',
      queryProfile: 'ordered-offset-v1',
      syncFilter: "EPOCH >= '2026-05-01T00:00:00Z'",
      limit: 25_000,
    });

    const length = new DataView(frame.buffer, frame.byteOffset, 4).getUint32(0, false);
    const payload = JSON.parse(decoder.decode(frame.slice(4, 4 + length))) as Record<string, unknown>;
    expect(payload).toMatchObject({
      op: 'read_chunk',
      schema: 'OMM.fbs',
      datastore_key: 'sdn-ds-v1-history',
      provider_id: 'space-data-network-02',
      source_name: 'celestrak-gp',
      cursor: 'cursor-1',
      snapshot_id: 'snapshot-1',
      head: 'head-1',
      query_profile: 'ordered-offset-v1',
      sync_filter: "EPOCH >= '2026-05-01T00:00:00Z'",
      limit: 25_000,
    });
  });

  it('encodes scan-bound read_chunk requests with record refs for direct FlatBuffer streaming', () => {
    const frame = encodeFlatSqlSyncRequest({
      targetPeerId: '16Uiu2HTest',
      schema: 'OMM.fbs',
      op: 'read_chunk',
      scanHash: 'scan-hash',
      chunkHash: 'chunk-hash',
      snapshotId: 'snapshot-1',
      head: 'head-1',
      nextCursor: 'cursor-2',
      totalCount: 2,
      highWaterMark: '1:2:3:2',
      records: [{
        schemaName: 'OMM.fbs',
        cid: 'cid-1',
        peerId: 'source:celestrak',
        providerId: 'space-data-network-02',
        sourceName: 'celestrak-gp',
        sizeBytes: 256,
      }],
    });

    const length = new DataView(frame.buffer, frame.byteOffset, 4).getUint32(0, false);
    const payload = JSON.parse(decoder.decode(frame.slice(4, 4 + length))) as Record<string, unknown>;
    expect(payload).toMatchObject({
      op: 'read_chunk',
      schema: 'OMM.fbs',
      scan_hash: 'scan-hash',
      chunk_hash: 'chunk-hash',
      snapshot_id: 'snapshot-1',
      head: 'head-1',
      next_cursor: 'cursor-2',
      total_count: 2,
      high_water_mark: '1:2:3:2',
      records: [{
        schema_name: 'OMM.fbs',
        cid: 'cid-1',
        peer_id: 'source:celestrak',
        provider_id: 'space-data-network-02',
        source_name: 'celestrak-gp',
        size_bytes: 256,
      }],
    });
  });

  it('dials the FlatSQL sync protocol and decodes header plus the native FlatSQL FlatBuffer stream', async () => {
    const rawRecord = new Uint8Array([1, 2, 3, 4]);
    const response = concatJsonFrameAndFlatSqlStream(
      {
        schema: 'OMM.fbs',
        total_count: 1,
        count: 1,
        limit: 25_000,
        offset: 0,
        cursor: 'MA',
        next_cursor: '',
        snapshot_id: 'snapshot-1',
        head: 'snapshot-1',
        high_water_mark: '1:2:3:1',
        scan_hash: 'chunk-1',
        chunk_hash: 'chunk-1',
        query_profile: 'ordered-offset-v1',
        sync_protocol: FLATSQL_SYNC_PROTOCOL_ID,
        max_chunk_size: 50_000,
        transports: ['http', 'libp2p-websocket', 'libp2p-webrtc'],
        results: [{ schema_name: 'OMM.fbs', cid: 'cid-1', peer_id: 'peer-1', size_bytes: rawRecord.byteLength }],
      },
      [rawRecord],
    );
    const calls: Array<{ peer: string; protocol: string; payload: Uint8Array; addrs?: string[] }> = [];
    const transport: FlatSqlSyncTransport = {
      async dialProtocol(targetPeerId, protocolId, payload, candidateAddrs) {
        calls.push({ peer: targetPeerId, protocol: protocolId, payload, addrs: candidateAddrs });
        return response;
      },
    };

    const chunk = await requestFlatSqlSyncChunk(transport, {
      targetPeerId: '16Uiu2HTest',
      candidateAddrs: ['/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/16Uiu2HTest'],
      schema: 'OMM.fbs',
      cursor: 'MA',
    });

    expect(calls).toHaveLength(1);
    expect(calls[0]?.protocol).toBe(FLATSQL_SYNC_PROTOCOL_ID);
    expect(calls[0]?.addrs).toEqual(['/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/16Uiu2HTest']);
    expect(chunk.header).toMatchObject({
      schema: 'OMM.fbs',
      snapshotId: 'snapshot-1',
      head: 'snapshot-1',
      chunkHash: 'chunk-1',
      maxChunkSize: 50_000,
    });
    expect(chunk.records).toHaveLength(1);
    expect(Array.from(chunk.records[0] ?? [])).toEqual([1, 2, 3, 4]);
    expect(Array.from(chunk.recordStream)).toEqual([4, 0, 0, 0, 1, 2, 3, 4]);
  });

  it('surfaces protocol error frames', () => {
    const response = concatFrames([
      encoder.encode(JSON.stringify({
        status: 'error',
        sync_protocol: FLATSQL_SYNC_PROTOCOL_ID,
        error: { message: 'schema is required' },
      })),
    ]);
    expect(() => decodeFlatSqlSyncChunk(response)).toThrow('schema is required');
  });

  it('dials open_manifest and decodes published shard CID segments', async () => {
    const response = concatFrames([
      encoder.encode(JSON.stringify({
        manifest_id: 'manifest-1',
        schema: 'OMM.fbs',
        provider_id: 'space-data-network-02',
        source_name: 'celestrak-gp',
        total_count: 50000,
        total_bytes: 8000000,
        snapshot_id: 'snapshot-1',
        head: 'snapshot-1',
        high_water_mark: '1:2:3:50000',
        query_profile: 'dataset-publication-offset-v1',
        sync_protocol: FLATSQL_SYNC_PROTOCOL_ID,
        max_chunk_size: 50000,
        transports: ['libp2p-websocket', 'libp2p-webrtc'],
        artifact_bundles: [{
          role: 'shard-group-car',
          cid: 'bafkcar',
          byte_count: 9000000,
          sha256: 'car-sha',
          format: 'car-v1',
          segment_start: 0,
          segment_count: 1,
        }],
        segments: [{
          index: 0,
          cursor: 'MA',
          next_cursor: '',
          row_count: 50000,
          byte_count: 8000000,
          chunk_hash: 'result-sha',
          cid: 'bafkshard',
          index_cid: 'bafkindex',
          manifest_cid: 'bafkmanifest',
          shard_sha256: 'shard-sha',
        }],
      })),
    ]);
    const calls: Array<{ peer: string; payload: Uint8Array }> = [];
    const transport: FlatSqlSyncTransport = {
      async dialProtocol(targetPeerId, _protocolId, payload) {
        calls.push({ peer: targetPeerId, payload });
        return response;
      },
    };

    const manifest = await requestFlatSqlSyncManifest(transport, {
      targetPeerId: '16Uiu2HTest',
      schema: 'OMM.fbs',
      queryProfile: 'dataset-publication-offset-v1',
      limit: 50_000,
    });

    const requestLength = new DataView(calls[0]?.payload.buffer ?? new ArrayBuffer(0), calls[0]?.payload.byteOffset ?? 0, 4).getUint32(0, false);
    const requestPayload = JSON.parse(decoder.decode(calls[0]?.payload.slice(4, 4 + requestLength))) as Record<string, unknown>;
    expect(requestPayload).toMatchObject({
      op: 'open_manifest',
      schema: 'OMM.fbs',
      query_profile: 'dataset-publication-offset-v1',
      limit: 50_000,
    });
    expect(manifest.segments).toEqual([
      expect.objectContaining({
        cid: 'bafkshard',
        indexCid: 'bafkindex',
        manifestCid: 'bafkmanifest',
        shardSha256: 'shard-sha',
        rowCount: 50000,
      }),
    ]);
    expect(manifest.artifactBundles).toEqual([
      {
        role: 'shard-group-car',
        cid: 'bafkcar',
        byteCount: 9000000,
        sha256: 'car-sha',
        format: 'car-v1',
        segmentStart: 0,
        segmentCount: 1,
      },
    ]);
  });

  it('dials list_datastores and decodes SDN datastore identities', async () => {
    const response = concatFrames([
      encoder.encode(JSON.stringify({
        count: 1,
        results: [{
          key: 'sdn-ds-v1-history',
          updated_at: 1778712000,
          identity: {
            schema_name: 'OMM.fbs',
            source_peer_id: 'source:history',
            source_public_key: 'history-public-key',
            provider_id: 'space-data-network-02',
            source_name: 'celestrak-gp-historical',
          },
        }],
      })),
    ]);
    const calls: Array<{ peer: string; payload: Uint8Array }> = [];
    const transport: FlatSqlSyncTransport = {
      async dialProtocol(targetPeerId, _protocolId, payload) {
        calls.push({ peer: targetPeerId, payload });
        return response;
      },
    };

    const datastores = await requestFlatSqlSyncDatastores(transport, {
      targetPeerId: '16Uiu2HTest',
    });

    const requestLength = new DataView(calls[0]?.payload.buffer ?? new ArrayBuffer(0), calls[0]?.payload.byteOffset ?? 0, 4).getUint32(0, false);
    const requestPayload = JSON.parse(decoder.decode(calls[0]?.payload.slice(4, 4 + requestLength))) as Record<string, unknown>;
    expect(requestPayload).toMatchObject({ op: 'list_datastores' });
    expect(datastores).toEqual({
      count: 1,
      results: [expect.objectContaining({
        key: 'sdn-ds-v1-history',
        identity: expect.objectContaining({
          schemaName: 'OMM.fbs',
          sourcePublicKey: 'history-public-key',
          sourceName: 'celestrak-gp-historical',
        }),
      })],
    });
    expect(decodeFlatSqlSyncDatastores(response).results[0]?.key).toBe('sdn-ds-v1-history');
  });

  it('measures wire speed with a bounded FlatSQL sync protocol probe', async () => {
    const probeBytes = new Uint8Array([7, 9, 11, 13]);
    const response = concatJsonFrameAndRawBytes(
      {
        op: 'wire_speed_probe',
        status: 'ok',
        sync_protocol: FLATSQL_SYNC_PROTOCOL_ID,
        probe_bytes: probeBytes.byteLength,
        payload_bytes: probeBytes.byteLength,
      },
      probeBytes,
    );
    const calls: Array<{ peer: string; protocol: string; payload: Uint8Array; addrs?: string[] }> = [];
    const transport: FlatSqlSyncTransport = {
      async dialProtocol(targetPeerId, protocolId, payload, candidateAddrs) {
        calls.push({ peer: targetPeerId, protocol: protocolId, payload, addrs: candidateAddrs });
        return response;
      },
    };
    const clockValues = [1000, 1250];
    const result = await requestFlatSqlWireSpeedProbe(
      transport,
      {
        targetPeerId: '16Uiu2HTest',
        candidateAddrs: ['/dns4/celestrak.eth/tcp/443/wss/p2p/16Uiu2HTest'],
        probeBytes: probeBytes.byteLength,
      },
      () => clockValues.shift() ?? 1250,
    );

    const requestLength = new DataView(calls[0]?.payload.buffer ?? new ArrayBuffer(0), calls[0]?.payload.byteOffset ?? 0, 4).getUint32(0, false);
    const requestPayload = JSON.parse(decoder.decode(calls[0]?.payload.slice(4, 4 + requestLength))) as Record<string, unknown>;
    expect(calls[0]).toMatchObject({
      peer: '16Uiu2HTest',
      protocol: FLATSQL_SYNC_PROTOCOL_ID,
      addrs: ['/dns4/celestrak.eth/tcp/443/wss/p2p/16Uiu2HTest'],
    });
    expect(requestPayload).toMatchObject({
      op: 'wire_speed_probe',
      probe_bytes: probeBytes.byteLength,
    });
    expect(result).toMatchObject({
      requestedBytes: probeBytes.byteLength,
      payloadBytes: probeBytes.byteLength,
      elapsedMs: 250,
      bytesPerSecond: 16,
      syncProtocol: FLATSQL_SYNC_PROTOCOL_ID,
    });
  });

  it('surfaces open_manifest protocol error frames', () => {
    const response = concatFrames([
      encoder.encode(JSON.stringify({
        status: 'error',
        sync_protocol: FLATSQL_SYNC_PROTOCOL_ID,
        error: { message: 'schema is required' },
      })),
    ]);
    expect(() => decodeFlatSqlSyncManifest(response)).toThrow('schema is required');
  });
});

function concatFrames(frames: Uint8Array[]): Uint8Array {
  const totalLength = frames.reduce((sum, frame) => sum + 4 + frame.byteLength, 0);
  const out = new Uint8Array(totalLength);
  let offset = 0;
  for (const frame of frames) {
    new DataView(out.buffer).setUint32(offset, frame.byteLength, false);
    offset += 4;
    out.set(frame, offset);
    offset += frame.byteLength;
  }
  return out;
}

function concatJsonFrameAndRawBytes(header: Record<string, unknown>, payload: Uint8Array): Uint8Array {
  const headerBytes = encoder.encode(JSON.stringify(header));
  const out = new Uint8Array(4 + headerBytes.byteLength + payload.byteLength);
  new DataView(out.buffer).setUint32(0, headerBytes.byteLength, false);
  out.set(headerBytes, 4);
  out.set(payload, 4 + headerBytes.byteLength);
  return out;
}

function concatJsonFrameAndFlatSqlStream(header: Record<string, unknown>, records: Uint8Array[]): Uint8Array {
  const headerBytes = encoder.encode(JSON.stringify(header));
  const streamBytes = flatSqlSizePrefixedStream(records);
  const out = new Uint8Array(4 + headerBytes.byteLength + streamBytes.byteLength);
  new DataView(out.buffer).setUint32(0, headerBytes.byteLength, false);
  out.set(headerBytes, 4);
  out.set(streamBytes, 4 + headerBytes.byteLength);
  return out;
}

function flatSqlSizePrefixedStream(records: Uint8Array[]): Uint8Array {
  const totalLength = records.reduce((sum, frame) => sum + 4 + frame.byteLength, 0);
  const out = new Uint8Array(totalLength);
  const view = new DataView(out.buffer);
  let offset = 0;
  for (const frame of records) {
    view.setUint32(offset, frame.byteLength, true);
    offset += 4;
    out.set(frame, offset);
    offset += frame.byteLength;
  }
  return out;
}
