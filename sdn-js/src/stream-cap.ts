/**
 * Generic byte-stream connector — browser capability adapter
 * (task sdn-stream-connector; Hermes + Janus rulings 2026-08-16).
 *
 * The SAME hook surface as the Go hosts (kubo/sdn/sdnservices/stream_cap.go,
 * sdn-server/internal/modulert/caps/stream.go), exposed through the
 * createModuleHostCapabilityAdapters family->op pattern:
 *
 *   stream.open  {kind:"tcp"|"tls"|"websocket", url|addr, headers?, timeout_ms?, max_frame_bytes?}
 *                -> { handle, kind }
 *   stream.send  {handle, data, encoding:"utf8"|"base64"} -> true
 *   stream.close {handle} -> true
 *
 * Inbound events are host-push, mirroring on_pubsub_message: every event is
 * handed to the onStreamFrame callback (routed by the browser harness to the
 * guest's declared "on_stream_frame" method) with the byte-identical envelope
 * the Go hosts emit:
 *
 *   { handle, event: "opened"|"frame"|"closed"|"error",
 *     data?: <base64>, encoding: "base64", seq, dropped, reason? }
 *
 * Isomorphism story (Janus ruling): the surface is IDENTICAL tri-runtime;
 * availability is runtime-dependent and gated on capability, never sniffed.
 * A browser page cannot open raw sockets, so kind "tcp" and kind "tls" fail
 * closed at stream.open with the module-SDK host denial envelope verbatim
 * (code "host-capability-denied") — the same shape a Go host returns for a
 * missing grant. kind "websocket" is backed by the native WebSocket API.
 * Headers cannot be attached to a browser WebSocket handshake; a module that
 * passes headers gets a refusal (silent divergence from the Go host would be
 * a P1 isomorphism defect — loud refusal is the contract).
 *
 * Host limits are byte-identical to the Go hosts: max 8 concurrent handles,
 * default frame ceiling 1 MiB (module may set max_frame_bytes, host absolute
 * ceiling 16 MiB; an oversized inbound frame is a terminal "error", an
 * oversized send is refused, never truncated), inbound queue depth 256 with
 * drop-oldest backpressure and a surfaced cumulative `dropped` counter, idle
 * timeout 5 minutes. Reconnect is MODULE-side only.
 */

export const STREAM_MAX_HANDLES = 8;
export const STREAM_DEFAULT_FRAME_BYTES = 1 << 20;
export const STREAM_MAX_FRAME_BYTES = 16 << 20;
export const STREAM_QUEUE_DEPTH = 256;
export const STREAM_IDLE_TIMEOUT_MS = 5 * 60 * 1000;
export const STREAM_INBOUND_METHOD = 'on_stream_frame';

export interface StreamFrameEnvelope {
  handle: string;
  event: 'opened' | 'frame' | 'closed' | 'error';
  data?: string; // base64
  encoding: 'base64';
  seq: number;
  dropped: number;
  reason?: string;
}

export type StreamFrameSink = (
  envelope: StreamFrameEnvelope,
) => void | Promise<void>;

export interface StreamCapabilityAdapterOptions {
  /**
   * Delivery sink for inbound events — the browser harness routes this to the
   * guest's declared "on_stream_frame" method (same wiring as
   * onPubsubMessage -> on_pubsub_message).
   */
  onStreamFrame?: StreamFrameSink;
  /**
   * Granted capability names for this module (from its approved manifest).
   * Fail closed: a kind whose capability is absent is refused at open.
   * Defaults to websocket-only — the only kind a browser can genuinely back.
   */
  grantedCapabilities?: Iterable<string>;
  /** Test seam: WebSocket constructor override. */
  webSocketImpl?: typeof WebSocket;
}

/** Module-SDK host denial envelope, byte-compatible with nodeHost.js. */
class StreamHostCapabilityError extends Error {
  readonly code = 'host-capability-denied';
  readonly capability: string;
  readonly operation: string;
  constructor(message: string, capability: string, operation: string) {
    super(message);
    this.name = 'HostCapabilityError';
    this.capability = capability;
    this.operation = operation;
  }
}

interface StreamHandleState {
  id: string;
  kind: string;
  ws: WebSocket;
  maxFrame: number;
  queue: StreamFrameEnvelope[];
  draining: boolean;
  seq: number;
  dropped: number;
  terminal: boolean;
  idleTimer: ReturnType<typeof setTimeout> | null;
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
  return btoa(binary);
}

function base64ToBytes(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

/**
 * Build the "stream" capability adapter family. Wire the result into
 * createModuleHostCapabilityAdapters' output (module-host-adapters.ts) as
 * `adapters.stream`.
 */
export function createStreamCapabilityAdapter(
  options: StreamCapabilityAdapterOptions = {},
): Record<string, (params: Record<string, unknown>) => Promise<unknown>> {
  const granted = new Set(options.grantedCapabilities ?? ['websocket']);
  const sink = options.onStreamFrame;
  const WS =
    options.webSocketImpl ??
    (typeof WebSocket !== 'undefined' ? WebSocket : undefined);
  const handles = new Map<string, StreamHandleState>();
  let nextId = 0;

  function emit(st: StreamHandleState, ev: Omit<StreamFrameEnvelope, 'handle' | 'encoding' | 'seq' | 'dropped'>): void {
    if (st.terminal) return;
    const terminal = ev.event === 'closed' || ev.event === 'error';
    if (st.queue.length >= STREAM_QUEUE_DEPTH && !terminal) {
      st.queue.shift();
      st.dropped++;
    }
    st.queue.push({
      handle: st.id,
      encoding: 'base64',
      seq: 0, // assigned at delivery, matching the Go deliverLoop
      dropped: 0,
      ...ev,
    });
    if (terminal) st.terminal = true;
    void drain(st);
  }

  async function drain(st: StreamHandleState): Promise<void> {
    if (st.draining) return;
    st.draining = true;
    try {
      while (st.queue.length > 0) {
        const ev = st.queue.shift()!;
        ev.seq = st.seq++;
        ev.dropped = st.dropped;
        if (sink) {
          try {
            await sink(ev);
          } catch {
            // Best-effort, like on_pubsub_message: a guest without the
            // method simply never sees inbound frames.
          }
        }
        if (ev.event === 'closed' || ev.event === 'error') {
          forget(st);
          return;
        }
      }
    } finally {
      st.draining = false;
    }
  }

  function forget(st: StreamHandleState): void {
    if (st.idleTimer) clearTimeout(st.idleTimer);
    st.idleTimer = null;
    if (handles.get(st.id) === st) handles.delete(st.id);
    try {
      if (st.ws.readyState === 0 || st.ws.readyState === 1) st.ws.close();
    } catch {
      /* already closed */
    }
  }

  function armIdle(st: StreamHandleState): void {
    if (st.idleTimer) clearTimeout(st.idleTimer);
    st.idleTimer = setTimeout(() => {
      emit(st, { event: 'closed', reason: 'idle' });
      try {
        st.ws.close();
      } catch {
        /* already closed */
      }
    }, STREAM_IDLE_TIMEOUT_MS);
  }

  return {
    open: async (params) => {
      const kind = String(params.kind ?? '');
      if (kind !== 'tcp' && kind !== 'tls' && kind !== 'websocket') {
        throw new Error('stream.open kind must be "tcp", "tls" or "websocket"');
      }
      // Fail closed on grant, exactly like the Go hosts (per-kind re-check).
      if (!granted.has(kind)) {
        throw new StreamHostCapabilityError(
          `Capability "${kind}" is not granted for this Browser host.`,
          kind,
          'stream.open',
        );
      }
      // Raw sockets are impossible in-page: identical surface, runtime-
      // dependent availability, loud refusal (Janus isomorphism ruling; these
      // kinds are on BrowserIncompatibleCapabilityIds, so an approved browser
      // artifact never carries the grant — this branch is defense in depth).
      if (kind !== 'websocket') {
        throw new StreamHostCapabilityError(
          `Capability "${kind}" is not granted for this Browser host.`,
          kind,
          'stream.open',
        );
      }
      if (!WS) throw new Error('stream.open: no WebSocket implementation available');
      const url = String(params.url ?? '');
      if (!/^wss?:\/\//.test(url)) {
        throw new Error('stream.open websocket url must be ws:// or wss://');
      }
      if (
        params.headers &&
        Object.keys(params.headers as Record<string, unknown>).length > 0
      ) {
        // Browsers cannot attach handshake headers — refuse loudly rather
        // than silently diverge from the Go host behavior.
        throw new Error(
          'stream.open: custom handshake headers are not available in the browser runtime — carry feed auth in the URL or first frame (module-side policy)',
        );
      }
      if (handles.size >= STREAM_MAX_HANDLES) {
        throw new Error(
          `stream handle limit reached (${STREAM_MAX_HANDLES} concurrent per module)`,
        );
      }
      const maxFrameRaw = Number(params.max_frame_bytes ?? 0);
      const maxFrame =
        Number.isFinite(maxFrameRaw) && maxFrameRaw > 0
          ? Math.floor(maxFrameRaw)
          : STREAM_DEFAULT_FRAME_BYTES;
      if (maxFrame > STREAM_MAX_FRAME_BYTES) {
        throw new Error(
          `max_frame_bytes exceeds the host ceiling of ${STREAM_MAX_FRAME_BYTES} bytes`,
        );
      }
      const timeoutRaw = Number(params.timeout_ms ?? 0);
      const timeoutMs =
        Number.isFinite(timeoutRaw) && timeoutRaw > 0 ? timeoutRaw : 30000;

      const ws = new WS(url);
      ws.binaryType = 'arraybuffer';
      const id = `s${++nextId}`;
      const st: StreamHandleState = {
        id,
        kind,
        ws,
        maxFrame,
        queue: [],
        draining: false,
        seq: 0,
        dropped: 0,
        terminal: false,
        idleTimer: null,
      };

      await new Promise<void>((resolve, reject) => {
        const timer = setTimeout(() => {
          try {
            ws.close();
          } catch {
            /* noop */
          }
          reject(new Error(`stream.open websocket dial failed: timeout after ${timeoutMs}ms`));
        }, timeoutMs);
        ws.onopen = () => {
          clearTimeout(timer);
          resolve();
        };
        ws.onerror = () => {
          clearTimeout(timer);
          reject(new Error('stream.open websocket dial failed'));
        };
      });

      handles.set(id, st);
      armIdle(st);
      emit(st, { event: 'opened' });

      ws.onmessage = (event: MessageEvent) => {
        let bytes: Uint8Array;
        if (typeof event.data === 'string') {
          bytes = new TextEncoder().encode(event.data);
        } else {
          bytes = new Uint8Array(event.data as ArrayBuffer);
        }
        if (bytes.length > st.maxFrame) {
          emit(st, {
            event: 'error',
            reason: `inbound frame exceeds the ${st.maxFrame}-byte limit for this handle`,
          });
          try {
            ws.close();
          } catch {
            /* noop */
          }
          return;
        }
        armIdle(st);
        emit(st, { event: 'frame', data: bytesToBase64(bytes) });
      };
      ws.onerror = () => {
        emit(st, { event: 'error', reason: 'websocket transport error' });
      };
      ws.onclose = (event: CloseEvent) => {
        emit(st, {
          event: 'closed',
          reason:
            event.code === 1000 || event.code === 1001 || event.code === 1005
              ? 'eof'
              : `close code ${event.code}`,
        });
      };

      return { handle: id, kind };
    },

    send: async (params) => {
      const handle = String(params.handle ?? '');
      const st = handles.get(handle);
      if (!st) throw new Error(`unknown stream handle: "${handle}"`);
      if (!granted.has(st.kind)) {
        throw new StreamHostCapabilityError(
          `Capability "${st.kind}" is not granted for this Browser host.`,
          st.kind,
          'stream.send',
        );
      }
      const encoding = String(params.encoding ?? 'utf8');
      const raw = String(params.data ?? '');
      const bytes =
        encoding === 'base64'
          ? base64ToBytes(raw)
          : new TextEncoder().encode(raw);
      if (bytes.length > st.maxFrame) {
        throw new Error(
          `stream.send frame exceeds the ${st.maxFrame}-byte limit for this handle`,
        );
      }
      if (st.ws.readyState !== 1) {
        throw new Error('stream.send failed: socket is not open');
      }
      st.ws.send(bytes);
      return true;
    },

    close: async (params) => {
      const handle = String(params.handle ?? '');
      const st = handles.get(handle);
      if (!st) throw new Error(`unknown stream handle: "${handle}"`);
      emit(st, { event: 'closed', reason: 'closed by module' });
      try {
        st.ws.close();
      } catch {
        /* already closed */
      }
      return true;
    },
  };
}
