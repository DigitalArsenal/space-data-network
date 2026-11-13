import type { Libp2p, Stream } from '@libp2p/interface';
import { peerIdFromString } from '@libp2p/peer-id';

export const PEER_EPM_PROTOCOL_ID = '/spacedatanetwork/epm-exchange/1.0.0';
export interface PeerEpmQuery {
  targetPeerId: string;
  /** Omit to request the answering node's own profile. */
  subjectPeerId?: string;
  signal?: AbortSignal;
  /** A stricter caller cap, never greater than the protocol's 64 KiB. */
  maxBytes?: number;
}
export class PeerEpmError extends Error {
  constructor(readonly status: number) {
    super(status === 1 ? 'Peer profile not found' : status === 2 ? 'Peer profile request failed' : `Unknown peer profile status ${status}`);
    this.name = 'PeerEpmError';
  }
}

async function readEpm(stream: Stream, maxBytes: number): Promise<Uint8Array> {
  const header = new Uint8Array(8);
  let headerLength = 0, length: number | undefined, received = 0;
  let body: Uint8Array | undefined;
  for await (const part of stream) {
    const chunk = part.subarray();
    let offset = 0;
    if (headerLength < 8) {
      const take = Math.min(8 - headerLength, chunk.length);
      header.set(chunk.subarray(0, take), headerLength);
      headerLength += take;
      offset = take;
      if (headerLength === 8) {
        const view = new DataView(header.buffer);
        const status = view.getUint32(0, true);
        length = view.getUint32(4, true);
        if (length > maxBytes) throw new Error('Peer profile exceeds byte limit');
        if (status !== 0) {
          if (length !== 0) throw new Error('Peer profile error response carries a payload');
          throw new PeerEpmError(status);
        }
        if (length === 0) throw new Error('Peer profile response is empty');
        body = new Uint8Array(length);
      }
    }
    const remaining = chunk.length - offset;
    if (remaining) {
      if (!body || received + remaining > body.length) throw new Error('Peer profile response has trailing bytes');
      body.set(chunk.subarray(offset), received);
      received += remaining;
    }
  }
  if (headerLength !== 8) throw new Error('Peer profile response header is truncated');
  if (!body || received !== length) throw new Error('Peer profile response payload is truncated');
  return body;
}

/** Returns the existing exchange's raw EPM bytes. Signature verification is a separate operation. */
export async function requestPeerEpm(libp2p: Pick<Libp2p, 'dialProtocol'>, query: PeerEpmQuery): Promise<Uint8Array> {
  const maxBytes = query.maxBytes ?? 65536;
  if (!Number.isInteger(maxBytes) || maxBytes < 1 || maxBytes > 65536) throw new Error('Peer profile byte limit must be between 1 and 65536');
  const target = peerIdFromString(query.targetPeerId);
  if (query.subjectPeerId) peerIdFromString(query.subjectPeerId);
  const subject = new TextEncoder().encode(query.subjectPeerId ?? '');
  if (subject.length > 128) throw new Error('Peer profile subject exceeds 128 bytes');
  const request = new Uint8Array(4 + subject.length);
  new DataView(request.buffer).setUint32(0, subject.length, true);
  request.set(subject, 4);
  const timeout = AbortSignal.timeout(25000);
  const signal = query.signal ? AbortSignal.any([query.signal, timeout]) : timeout;
  signal.throwIfAborted();
  const stream = await libp2p.dialProtocol(target, PEER_EPM_PROTOCOL_ID, { signal });
  const abort = () => stream.abort(signal.reason instanceof Error ? signal.reason : new Error('Peer profile request canceled'));
  signal.addEventListener('abort', abort, { once: true });
  const reading = readEpm(stream, maxBytes);
  const writing = (async () => {
    signal.throwIfAborted();
    if (!stream.send(request)) await stream.onDrain({ signal });
    await stream.close({ signal });
  })();
  try {
    const [result] = await Promise.all([reading, writing]);
    signal.throwIfAborted();
    return result;
  } catch (error) {
    stream.abort(error instanceof Error ? error : new Error(String(error)));
    await Promise.allSettled([reading, writing]);
    throw error;
  } finally {
    signal.removeEventListener('abort', abort);
  }
}
