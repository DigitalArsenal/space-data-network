import * as flatbuffers from 'flatbuffers';
import { QRP } from 'spacedatastandards.org/lib/js/QRP/QRP.js';
import { QRPColumn } from 'spacedatastandards.org/lib/js/QRP/QRPColumn.js';
import { QRPRow } from 'spacedatastandards.org/lib/js/QRP/QRPRow.js';
import { QRPValue } from 'spacedatastandards.org/lib/js/QRP/QRPValue.js';
import { qrpKind } from 'spacedatastandards.org/lib/js/QRP/qrpKind.js';
import { qrpStatus } from 'spacedatastandards.org/lib/js/QRP/qrpStatus.js';
import { qrpValueKind } from 'spacedatastandards.org/lib/js/QRP/qrpValueKind.js';
import { FlatSqlSyncError } from './flatsql-sync';

export const PEER_DIRECTORY_PROTOCOL_ID = '/space-data-network/peer-directory/1.0.0';
export interface ObservedSDNPeer {
  peerId: string;
  /** At most eight public browser addresses, sorted by the reporting node. */
  multiaddrs: string[];
  /** At most sixteen SDN identify protocols. */
  protocols: string[];
  agent: string;
  profileCid: string;
  connected: boolean;
  lastSeenMs: number;
  evidence: 'connected' | 'seen';
}
export interface ObservedSDNPeersPage {
  reporterPeerId: string;
  snapshotAtMs: number;
  peers: ObservedSDNPeer[];
  nextCursor: string;
  totalCount: number;
  /** The node's bounded snapshot omitted additional peers. */
  truncated: boolean;
}
export interface ObservedSDNPeersQuery {
  targetPeerId: string;
  limit?: number;
  cursor?: string;
  signal?: AbortSignal;
}
/** The transport must enforce maxBytes while reading and abort its stream on signal. */
export interface PeerDirectoryTransport {
  dialProtocol(targetPeerId: string, protocolId: string, payload: Uint8Array,
    candidateAddrs: undefined, options: { signal?: AbortSignal; maxBytes: number }): Promise<Uint8Array>;
}

export function encodeObservedSDNPeersRequest(query: Pick<ObservedSDNPeersQuery, 'limit' | 'cursor'>): Uint8Array {
  const limit = query.limit ?? 128;
  if (!Number.isInteger(limit) || limit < 1 || limit > 128) throw new Error('Directory limit must be between 1 and 128');
  if (query.cursor && !/^[A-Za-z0-9_-]{32}$/.test(query.cursor)) throw new Error('Invalid directory cursor');
  const b = new flatbuffers.Builder(128);
  const cursor = query.cursor ? b.createString(query.cursor) : 0;
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Request);
  QRP.addLimit(b, limit);
  if (cursor) QRP.addCursor(b, cursor);
  QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
  return b.asUint8Array().slice();
}

export function decodeObservedSDNPeers(bytes: Uint8Array): ObservedSDNPeersPage {
  if (bytes.length < 12 || bytes.length > 1048576 || new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getUint32(0, true) !== bytes.length - 4 ||
      new TextDecoder().decode(bytes.subarray(8, 12)) !== '$QRP') throw new Error('Invalid directory frame');
  const q = QRP.getSizePrefixedRootAsQRP(new flatbuffers.ByteBuffer(bytes));
  if (q.KIND() === qrpKind.Error) throw new FlatSqlSyncError(q.ERROR_CODE() ?? '', q.MESSAGE() ?? '', q.STATUS(), q.RETRY_AFTER_MS(), q.HTTP_STATUS());
  if (q.KIND() !== qrpKind.Page || q.STATUS() !== qrpStatus.Ok || q.rowsLength() > 128 || q.RECORD_COUNT() !== q.rowsLength() || q.columnsLength() !== 30) throw new Error('Invalid directory page');
  const names = new Map<string, number>();
  for (let i = 0; i < q.columnsLength(); i++) {
    const name = q.COLUMNS(i, new QRPColumn())?.NAME();
    if (!name || names.has(name)) throw new Error('Invalid directory columns');
    names.set(name, i);
  }
  const peers: ObservedSDNPeer[] = [];
  for (let i = 0; i < q.rowsLength(); i++) {
    const row = q.ROWS(i, new QRPRow());
    if (!row || row.cellsLength() !== names.size) throw new Error('Invalid directory row');
    const cell = (name: string): QRPValue => {
      const index = names.get(name);
      const value = index === undefined ? null : row.CELLS(index, new QRPValue());
      if (!value) throw new Error(`Missing directory column ${name}`);
      return value;
    };
    const text = (name: string): string => {
      const v = cell(name);
      if (v.KIND() === qrpValueKind.Null) return '';
      if (v.KIND() !== qrpValueKind.Text) throw new Error(`Invalid directory text ${name}`);
      return v.TEXT_VALUE() ?? '';
    };
    const int = (name: string): number => {
      const v = cell(name), n = Number(v.INT_VALUE());
      if (v.KIND() !== qrpValueKind.Int || !Number.isSafeInteger(n) || n < 0) throw new Error(`Invalid directory integer ${name}`);
      return n;
    };
    const evidence = text('EVIDENCE'), connected = int('CONNECTED');
    if ((evidence !== 'connected' && evidence !== 'seen') || connected > 1 || (evidence === 'connected') !== (connected === 1)) throw new Error('Invalid directory evidence');
    const multiaddrs = Array.from({ length: 8 }, (_, j) => text(`MULTIADDR_${j}`)).filter(Boolean);
    if (!multiaddrs.length || !text('PEER_ID')) throw new Error('Invalid directory peer');
    peers.push({ peerId: text('PEER_ID'), agent: text('AGENT'), profileCid: text('PROFILE_CID'), connected: connected === 1,
      lastSeenMs: int('LAST_SEEN_MS'), evidence, multiaddrs,
      protocols: Array.from({ length: 16 }, (_, j) => text(`SDN_PROTOCOL_${j}`)).filter(Boolean) });
  }
  const snapshotAtMs = Number(q.AS_OF_MS()), totalCount = Number(q.TOTAL_COUNT());
  const reporterPeerId = q.PRODUCER_PEER_ID() ?? '', nextCursor = q.NEXT_CURSOR() ?? '';
  if (!reporterPeerId || !Number.isSafeInteger(snapshotAtMs) || snapshotAtMs <= 0 || !Number.isSafeInteger(totalCount) || totalCount < peers.length ||
      (nextCursor && !/^[A-Za-z0-9_-]{32}$/.test(nextCursor))) throw new Error('Invalid directory snapshot');
  return { reporterPeerId, snapshotAtMs, peers, nextCursor, totalCount, truncated: q.TRUNCATED() };
}

export async function requestObservedSDNPeers(transport: PeerDirectoryTransport, query: ObservedSDNPeersQuery): Promise<ObservedSDNPeersPage> {
  query.signal?.throwIfAborted();
  const response = await transport.dialProtocol(query.targetPeerId, PEER_DIRECTORY_PROTOCOL_ID,
    encodeObservedSDNPeersRequest(query), undefined, { signal: query.signal, maxBytes: 1048576 });
  query.signal?.throwIfAborted();
  const page = decodeObservedSDNPeers(response);
  if (page.reporterPeerId !== query.targetPeerId) throw new Error('Directory reporter does not match the requested peer');
  return page;
}
