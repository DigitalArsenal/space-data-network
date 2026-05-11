import { describe, expect, it } from 'vitest';
import {
  FLATSQL_SYNC_PROTOCOL_ID,
  decodeFlatSqlSyncChunk,
  encodeFlatSqlSyncRequest,
  requestFlatSqlSyncChunk,
  type FlatSqlSyncTransport,
} from './flatsql-sync';

const encoder = new TextEncoder();
const decoder = new TextDecoder();

describe('FlatSQL sync protocol client', () => {
  it('encodes resumable read_chunk requests as a length-prefixed JSON frame', () => {
    const frame = encodeFlatSqlSyncRequest({
      targetPeerId: '16Uiu2HTest',
      schema: 'OMM.fbs',
      providerId: 'space-data-network-02',
      sourceName: 'celestrak-gp',
      cursor: 'cursor-1',
      snapshotId: 'snapshot-1',
      head: 'head-1',
      queryProfile: 'ordered-offset-v1',
      limit: 25_000,
    });

    const length = new DataView(frame.buffer, frame.byteOffset, 4).getUint32(0, false);
    const payload = JSON.parse(decoder.decode(frame.slice(4, 4 + length))) as Record<string, unknown>;
    expect(payload).toMatchObject({
      op: 'read_chunk',
      schema: 'OMM.fbs',
      provider_id: 'space-data-network-02',
      source_name: 'celestrak-gp',
      cursor: 'cursor-1',
      snapshot_id: 'snapshot-1',
      head: 'head-1',
      query_profile: 'ordered-offset-v1',
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

  it('dials the FlatSQL sync protocol and decodes header plus raw FlatBuffer frames', async () => {
    const rawRecord = new Uint8Array([1, 2, 3, 4]);
    const response = concatFrames([
      encoder.encode(JSON.stringify({
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
      })),
      rawRecord,
    ]);
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
