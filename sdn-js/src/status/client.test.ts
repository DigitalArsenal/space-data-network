import * as flatbuffers from 'flatbuffers';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { NodeStatus, NodeStatusSet } from './generated/nst.js';
import {
  computeBackoffDelay,
  createNodeStatusClient,
  deriveStatusWsUrl,
  fetchNodeStatusOnce,
  type WebSocketLike,
} from './client';
import type { NodeStatusSetView } from './view-model';

function buildFrame(peerId = '12D3KooWSelf', lat = 12.5, lon = -8.25): Uint8Array {
  const builder = new flatbuffers.Builder(512);
  const peer = builder.createString(peerId);
  NodeStatus.startNodeStatus(builder);
  NodeStatus.addPeerId(builder, peer);
  NodeStatus.addIsOnline(builder, true);
  NodeStatus.addIsSelf(builder, true);
  NodeStatus.addLat(builder, lat);
  NodeStatus.addLon(builder, lon);
  const node = NodeStatus.endNodeStatus(builder);

  const src = builder.createString(peerId);
  const nodes = NodeStatusSet.createNodesVector(builder, [node]);
  NodeStatusSet.startNodeStatusSet(builder);
  NodeStatusSet.addNodes(builder, nodes);
  NodeStatusSet.addGeneratedAt(builder, BigInt(1_700_000_000_000));
  NodeStatusSet.addSourcePeerId(builder, src);
  const set = NodeStatusSet.endNodeStatusSet(builder);
  NodeStatusSet.finishSizePrefixedNodeStatusSetBuffer(builder, set);
  return builder.asUint8Array().slice();
}

function frameArrayBuffer(bytes: Uint8Array): ArrayBuffer {
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
}

/** Controllable WebSocket stand-in that records every instance created. */
class MockWebSocket implements WebSocketLike {
  static instances: MockWebSocket[] = [];
  static reset(): void {
    MockWebSocket.instances = [];
  }

  binaryType = 'blob';
  readyState = 0;
  url: string;
  onopen: ((event: unknown) => void) | null = null;
  onclose: ((event: unknown) => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  onmessage: ((event: { data?: unknown }) => void) | null = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  send(): void {
    /* status feed is push-only */
  }

  close(): void {
    this.closed = true;
    this.readyState = 3;
    this.onclose?.({});
  }

  // ---- test helpers ----
  open(): void {
    this.readyState = 1;
    this.onopen?.({});
  }

  deliver(bytes: Uint8Array): void {
    this.onmessage?.({ data: frameArrayBuffer(bytes) });
  }

  /** Simulate a server-side close (does not go through client teardown). */
  serverClose(): void {
    this.readyState = 3;
    this.onclose?.({});
  }
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

afterEach(() => {
  MockWebSocket.reset();
  vi.useRealTimers();
});

describe('deriveStatusWsUrl', () => {
  it('derives wss://host/ws/status from an https host', () => {
    expect(deriveStatusWsUrl('https://sdn.spaceaware.io')).toBe(
      'wss://sdn.spaceaware.io/ws/status',
    );
  });

  it('derives ws://host:port/ws/status from an http host with a port', () => {
    expect(deriveStatusWsUrl('http://localhost:8080')).toBe('ws://localhost:8080/ws/status');
  });

  it('appends /ws/status to a bare wss host', () => {
    expect(deriveStatusWsUrl('wss://node.example')).toBe('wss://node.example/ws/status');
  });

  it('treats a bare host as https → wss', () => {
    expect(deriveStatusWsUrl('node.example')).toBe('wss://node.example/ws/status');
  });

  it('preserves an explicit ws path', () => {
    expect(deriveStatusWsUrl('wss://node.example/ws/custom')).toBe('wss://node.example/ws/custom');
  });
});

describe('computeBackoffDelay', () => {
  const opts = { baseMs: 500, capMs: 30_000 };

  it('grows exponentially without jitter', () => {
    expect(computeBackoffDelay(0, opts, () => 0)).toBe(500);
    expect(computeBackoffDelay(1, opts, () => 0)).toBe(1000);
    expect(computeBackoffDelay(2, opts, () => 0)).toBe(2000);
    expect(computeBackoffDelay(3, opts, () => 0)).toBe(4000);
  });

  it('caps at capMs', () => {
    expect(computeBackoffDelay(20, opts, () => 0)).toBe(30_000);
    expect(computeBackoffDelay(20, opts, () => 1)).toBe(30_000);
  });

  it('adds bounded jitter', () => {
    // 500 base + 500 * 0.5 * 1 = 750
    expect(computeBackoffDelay(0, { ...opts, jitter: 0.5 }, () => 1)).toBe(750);
    // default jitter 0.3 → 500 + 150 = 650 at rng=1
    expect(computeBackoffDelay(0, opts, () => 1)).toBe(650);
  });
});

describe('createNodeStatusClient — remote mode', () => {
  it('decodes a delivered frame and fires subscribers with the view model', async () => {
    const client = createNodeStatusClient({
      url: 'wss://node.example/ws/status',
      WebSocket: MockWebSocket as unknown as new (url: string) => WebSocketLike,
    });

    const received: NodeStatusSetView[] = [];
    client.subscribe((view) => received.push(view));

    expect(MockWebSocket.instances).toHaveLength(1);
    const socket = MockWebSocket.instances[0];
    expect(socket.url).toBe('wss://node.example/ws/status');

    socket.open();
    socket.deliver(buildFrame('12D3KooWAlpha'));
    await flush();

    expect(received).toHaveLength(1);
    expect(received[0].sourcePeerId).toBe('12D3KooWAlpha');
    expect(received[0].nodes[0].peerId).toBe('12D3KooWAlpha');
    expect(client.current()?.nodes[0].isSelf).toBe(true);

    client.stop();
  });

  it('delivers the current view synchronously to late subscribers', async () => {
    const client = createNodeStatusClient({
      url: 'node.example',
      WebSocket: MockWebSocket as unknown as new (url: string) => WebSocketLike,
    });
    const socket = MockWebSocket.instances[0];
    socket.open();
    socket.deliver(buildFrame('12D3KooWBeta'));
    await flush();

    const late: NodeStatusSetView[] = [];
    client.subscribe((view) => late.push(view));
    expect(late).toHaveLength(1);
    expect(late[0].sourcePeerId).toBe('12D3KooWBeta');

    client.stop();
  });

  it('unsubscribe stops further delivery', async () => {
    const client = createNodeStatusClient({
      url: 'node.example',
      WebSocket: MockWebSocket as unknown as new (url: string) => WebSocketLike,
    });
    const socket = MockWebSocket.instances[0];
    socket.open();

    const seen: NodeStatusSetView[] = [];
    const unsub = client.subscribe((view) => seen.push(view));
    socket.deliver(buildFrame('a'));
    await flush();
    unsub();
    socket.deliver(buildFrame('b'));
    await flush();

    expect(seen).toHaveLength(1);
    client.stop();
  });

  it('reconnects with backoff after the socket closes', () => {
    vi.useFakeTimers();
    const client = createNodeStatusClient({
      url: 'wss://node.example/ws/status',
      backoffBaseMs: 100,
      backoffCapMs: 1000,
      WebSocket: MockWebSocket as unknown as new (url: string) => WebSocketLike,
    });

    expect(MockWebSocket.instances).toHaveLength(1);
    MockWebSocket.instances[0].open();
    // server drops the connection
    MockWebSocket.instances[0].serverClose();

    // nothing reconnects before the backoff delay elapses
    expect(MockWebSocket.instances).toHaveLength(1);
    // max first-attempt delay = base(100) + jitter(<=30) < 200
    vi.advanceTimersByTime(200);
    expect(MockWebSocket.instances).toHaveLength(2);

    client.stop();
  });

  it('stops reconnecting after stop()', () => {
    vi.useFakeTimers();
    const client = createNodeStatusClient({
      url: 'wss://node.example/ws/status',
      backoffBaseMs: 100,
      backoffCapMs: 1000,
      WebSocket: MockWebSocket as unknown as new (url: string) => WebSocketLike,
    });
    MockWebSocket.instances[0].serverClose();
    client.stop();
    vi.advanceTimersByTime(5000);
    expect(MockWebSocket.instances).toHaveLength(1);
  });

  it('stop() is idempotent and closes the live socket', () => {
    const client = createNodeStatusClient({
      url: 'wss://node.example/ws/status',
      WebSocket: MockWebSocket as unknown as new (url: string) => WebSocketLike,
    });
    const socket = MockWebSocket.instances[0];
    client.stop();
    client.stop();
    expect(socket.closed).toBe(true);
  });

  it('tolerates a malformed frame (fail-open) and keeps serving valid frames', async () => {
    const errors: unknown[] = [];
    const received: NodeStatusSetView[] = [];
    const client = createNodeStatusClient({
      url: 'wss://node.example/ws/status',
      WebSocket: MockWebSocket as unknown as new (url: string) => WebSocketLike,
      onError: (err) => errors.push(err),
    });
    client.subscribe((view) => received.push(view));
    const socket = MockWebSocket.instances[0];
    socket.open();

    // A malformed frame must never throw out of the client.
    expect(() => socket.onmessage?.({ data: new Uint8Array([9, 9, 9]).buffer })).not.toThrow();
    // An empty frame is ignored.
    socket.deliver(new Uint8Array(0));
    await flush();

    // The client is still healthy and decodes a subsequent valid frame.
    socket.deliver(buildFrame('12D3KooWRecover'));
    await flush();
    expect(received.at(-1)?.sourcePeerId).toBe('12D3KooWRecover');

    client.stop();
  });
});

describe('createNodeStatusClient — misconfiguration', () => {
  it('throws when neither url nor helia is provided', () => {
    expect(() => createNodeStatusClient({})).toThrow(/requires either/);
  });
});

describe('fetchNodeStatusOnce', () => {
  it('resolves with the first decoded frame then closes the socket', async () => {
    const promise = fetchNodeStatusOnce(
      'wss://node.example',
      MockWebSocket as unknown as new (url: string) => WebSocketLike,
    );
    const socket = MockWebSocket.instances[0];
    socket.open();
    socket.deliver(buildFrame('12D3KooWOnce'));
    const view = await promise;
    expect(view.sourcePeerId).toBe('12D3KooWOnce');
    expect(socket.closed).toBe(true);
  });

  it('rejects on timeout', async () => {
    vi.useFakeTimers();
    const promise = fetchNodeStatusOnce(
      'wss://node.example',
      MockWebSocket as unknown as new (url: string) => WebSocketLike,
      1000,
    );
    const rejection = expect(promise).rejects.toThrow(/timed out/);
    await vi.advanceTimersByTimeAsync(1001);
    await rejection;
  });
});
