/**
 * Browser stream capability adapter tests (task sdn-stream-connector).
 *
 * Computable-outcome tests only: envelope parity with the Go hosts
 * (kubo/sdn/sdnservices/stream_cap_test.go is the reference), per-kind
 * fail-closed gating, drop-oldest backpressure accounting, terminal
 * semantics, and byte-identical limits. A scripted WebSocket double is
 * injected through the webSocketImpl seam so the transport is deterministic.
 */

import { describe, expect, it } from 'vitest';
import {
  createStreamCapabilityAdapter,
  STREAM_DEFAULT_FRAME_BYTES,
  STREAM_MAX_FRAME_BYTES,
  STREAM_MAX_HANDLES,
  STREAM_QUEUE_DEPTH,
  type StreamFrameEnvelope,
} from './stream-cap.js';

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  url: string;
  binaryType = 'blob';
  readyState = 0;
  sent: Uint8Array[] = [];
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
    queueMicrotask(() => {
      this.readyState = 1;
      this.onopen?.();
    });
  }

  send(data: Uint8Array): void {
    this.sent.push(data);
  }

  close(): void {
    if (this.readyState === 3) return;
    this.readyState = 3;
    queueMicrotask(() => this.onclose?.({ code: 1000 }));
  }

  // test controls
  receive(bytes: Uint8Array): void {
    this.onmessage?.({ data: bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) });
  }
}

function collector(): { events: StreamFrameEnvelope[]; sink: (e: StreamFrameEnvelope) => void } {
  const events: StreamFrameEnvelope[] = [];
  return { events, sink: (e) => void events.push(e) };
}

async function settle(): Promise<void> {
  for (let i = 0; i < 10; i++) await Promise.resolve();
}

function adapter(sink: (e: StreamFrameEnvelope) => void, granted?: string[]) {
  FakeWebSocket.instances = [];
  return createStreamCapabilityAdapter({
    onStreamFrame: sink,
    grantedCapabilities: granted,
    webSocketImpl: FakeWebSocket as unknown as typeof WebSocket,
  });
}

describe('stream capability adapter (browser shim)', () => {
  it('limits are byte-identical to the Go hosts', () => {
    expect(STREAM_MAX_HANDLES).toBe(8);
    expect(STREAM_DEFAULT_FRAME_BYTES).toBe(1 << 20);
    expect(STREAM_MAX_FRAME_BYTES).toBe(16 << 20);
    expect(STREAM_QUEUE_DEPTH).toBe(256);
  });

  it('fails closed on tcp/tls with the host-capability-denied envelope', async () => {
    const { sink } = collector();
    const stream = adapter(sink, ['tcp', 'tls', 'websocket']);
    for (const kind of ['tcp', 'tls']) {
      const err = await stream
        .open({ kind, addr: '127.0.0.1:5631' })
        .then(() => null, (e: unknown) => e as Record<string, unknown>);
      expect(err).not.toBeNull();
      expect(err!.code).toBe('host-capability-denied');
      expect(err!.capability).toBe(kind);
      expect(err!.operation).toBe('stream.open');
    }
  });

  it('fails closed on an ungranted kind (per-kind re-check)', async () => {
    const stream = adapter(collector().sink, ['tcp']); // websocket NOT granted
    await expect(
      stream.open({ kind: 'websocket', url: 'ws://example.invalid/feed' }),
    ).rejects.toMatchObject({
      code: 'host-capability-denied',
      capability: 'websocket',
    });
  });

  it('open/frame/close delivers the Go-host envelope with seq and dropped', async () => {
    const { events, sink } = collector();
    const stream = adapter(sink);
    const res = (await stream.open({
      kind: 'websocket',
      url: 'ws://example.invalid/aivdm',
    })) as { handle: string; kind: string };
    expect(res.kind).toBe('websocket');
    await settle();
    expect(events[0]).toMatchObject({
      handle: res.handle,
      event: 'opened',
      encoding: 'base64',
      seq: 0,
      dropped: 0,
    });

    const ws = FakeWebSocket.instances[0];
    const payload = new Uint8Array([0x00, 0x01, 0xfe, 0xff]);
    ws.receive(payload);
    await settle();
    const frame = events.find((e) => e.event === 'frame')!;
    expect(frame.seq).toBe(1);
    expect(Uint8Array.from(atob(frame.data!), (c) => c.charCodeAt(0))).toEqual(
      payload,
    );

    await stream.send({ handle: res.handle, data: 'AIVDM,test', encoding: 'utf8' });
    expect(new TextDecoder().decode(ws.sent[0])).toBe('AIVDM,test');

    await stream.close({ handle: res.handle });
    await settle();
    const last = events[events.length - 1];
    expect(last).toMatchObject({ event: 'closed', reason: 'closed by module' });
    // Handle freed — nothing may follow the terminal event.
    await expect(stream.send({ handle: res.handle, data: 'x' })).rejects.toThrow(
      /unknown stream handle/,
    );
  });

  it('refuses oversize sends and terminates on oversize inbound frames', async () => {
    const { events, sink } = collector();
    const stream = adapter(sink);
    const res = (await stream.open({
      kind: 'websocket',
      url: 'ws://example.invalid/x',
      max_frame_bytes: 16,
    })) as { handle: string };
    await expect(
      stream.send({ handle: res.handle, data: 'a'.repeat(17) }),
    ).rejects.toThrow(/16-byte limit/);
    FakeWebSocket.instances[0].receive(new Uint8Array(17));
    await settle();
    const terminal = events[events.length - 1];
    expect(terminal.event).toBe('error');
    expect(terminal.reason).toMatch(/16-byte limit/);
  });

  it('refuses max_frame_bytes above the host ceiling', async () => {
    const stream = adapter(collector().sink);
    await expect(
      stream.open({
        kind: 'websocket',
        url: 'ws://example.invalid/x',
        max_frame_bytes: STREAM_MAX_FRAME_BYTES + 1,
      }),
    ).rejects.toThrow(/host ceiling/);
  });

  it('enforces the concurrent handle limit', async () => {
    const stream = adapter(collector().sink);
    for (let i = 0; i < STREAM_MAX_HANDLES; i++) {
      await stream.open({ kind: 'websocket', url: 'ws://example.invalid/n' });
    }
    await expect(
      stream.open({ kind: 'websocket', url: 'ws://example.invalid/n' }),
    ).rejects.toThrow(/handle limit/);
  });

  it('refuses handshake headers loudly (no silent divergence from Go hosts)', async () => {
    const stream = adapter(collector().sink);
    await expect(
      stream.open({
        kind: 'websocket',
        url: 'ws://example.invalid/x',
        headers: { 'X-Feed-Auth': 'tok' },
      }),
    ).rejects.toThrow(/handshake headers/);
  });

  it('drop-oldest backpressure counts drops cumulatively and never silently', async () => {
    // A sink that never yields lets the queue fill: block delivery by making
    // the first sink call hang until released.
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const events: StreamFrameEnvelope[] = [];
    let first = true;
    const stream = adapter(async (e) => {
      if (first) {
        first = false;
        await gate;
      }
      events.push(e);
    });
    const res = (await stream.open({
      kind: 'websocket',
      url: 'ws://example.invalid/flood',
    })) as { handle: string };
    void res;
    const ws = FakeWebSocket.instances[0];
    const total = STREAM_QUEUE_DEPTH + 40;
    for (let i = 0; i < total; i++) ws.receive(new TextEncoder().encode(`${i}`));
    release();
    await settle();
    const frames = events.filter((e) => e.event === 'frame');
    // 40 oldest frames were dropped and counted.
    const lastFrame = frames[frames.length - 1];
    expect(lastFrame.dropped).toBe(40);
    const firstDelivered = new TextDecoder().decode(
      Uint8Array.from(atob(frames[0].data!), (c) => c.charCodeAt(0)),
    );
    expect(Number(firstDelivered)).toBe(40); // head of queue after drop-oldest
  });
});
