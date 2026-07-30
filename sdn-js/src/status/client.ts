/**
 * Node status client.
 *
 * `createNodeStatusClient(opts)` produces a small subscribe/current/stop client
 * that surfaces a {@link NodeStatusSetView} in one of two modes:
 *
 * - **remote**: connect to a node's `/ws/status` WebSocket feed, decode the
 *   size-prefixed `$NST` binary frames, and re-emit on every update.
 *   Auto-reconnects with capped exponential backoff + jitter.
 * - **helia**: given an SDN Helia node ({@link HeliaSDNNode} from
 *   `createHeliaSDNNode`), assemble the same view client-side from the local
 *   libp2p peer id, connection state to the two SDN bootstrap peers, EPM/vCard
 *   resolution, and opportunistic geo coordinates from any reachable remote
 *   `/ws/status` feed.
 *
 * Both modes are fail-open: transient errors never throw out of the client;
 * they are reported through `opts.onError` (when provided) and the client keeps
 * running.
 */

import type { HeliaSDNNode } from '../helia';
import { SPACEDATASTANDARDS_VERSION, SUITE_VERSION } from '../version-info.generated';
import {
  decodeNodeStatusSet,
  type NodeStatusSetView,
  type NodeStatusView,
} from './view-model';

/** Minimal structural WebSocket used by the client (browser / `ws` / mocks). */
export interface WebSocketLike {
  binaryType: string;
  readonly readyState: number;
  send(data: string | ArrayBufferLike | ArrayBufferView): void;
  close(code?: number, reason?: string): void;
  onopen: ((event: unknown) => void) | null;
  onclose: ((event: unknown) => void) | null;
  onerror: ((event: unknown) => void) | null;
  onmessage: ((event: { data?: unknown }) => void) | null;
}

/** Constructor shape for a {@link WebSocketLike}. */
export type WebSocketCtor = new (url: string) => WebSocketLike;

/** A subscriber for status set updates. */
export type NodeStatusListener = (view: NodeStatusSetView) => void;

/** The shared client interface exposed by both modes. */
export interface NodeStatusClient {
  /**
   * Subscribe to status updates. The current view (if any) is delivered
   * synchronously on subscribe. Returns an unsubscribe function.
   */
  subscribe(cb: NodeStatusListener): () => void;
  /** The most recently emitted view, or `null` before the first update. */
  current(): NodeStatusSetView | null;
  /** Stop the client, closing sockets/timers. Idempotent. */
  stop(): void;
}

/** Options for {@link createNodeStatusClient}. */
export interface NodeStatusClientOptions {
  /**
   * Remote mode: URL of the node's status feed. Accepts `wss://`, `ws://`,
   * `https://`, `http://`, or a bare host. When no explicit `/ws/...` path is
   * present, `/ws/status` is appended (https → wss, http → ws).
   */
  url?: string;
  /** Helia mode: an SDN Helia node from `createHeliaSDNNode`. */
  helia?: HeliaSDNNode;
  /** Helia mode: reassembly interval in ms (default 5000). */
  refreshIntervalMs?: number;
  /** Remote mode: reconnect backoff base in ms (default 500). */
  backoffBaseMs?: number;
  /** Remote mode: reconnect backoff cap in ms (default 30000). */
  backoffCapMs?: number;
  /**
   * WebSocket implementation. Defaults to `globalThis.WebSocket`. Provide for
   * Node without a global WebSocket, or a mock for tests.
   */
  WebSocket?: WebSocketCtor;
  /**
   * Helia mode: enable opportunistic geo enrichment (one short-lived fetch of a
   * reachable remote `/ws/status` per bootstrap node). Default `true`.
   */
  geo?: boolean;
  /** Called with non-fatal errors (fail-open). */
  onError?: (error: unknown) => void;
}

/** Backoff tuning for reconnect delay computation. */
export interface BackoffOptions {
  baseMs: number;
  capMs: number;
  /** Jitter as a fraction (0..1) of the delay, added randomly. Default 0.3. */
  jitter?: number;
}

const DEFAULT_BACKOFF_BASE_MS = 500;
const DEFAULT_BACKOFF_CAP_MS = 30_000;
const DEFAULT_HELIA_REFRESH_MS = 5_000;
const DEFAULT_GEO_FETCH_TIMEOUT_MS = 3_000;
const DEFAULT_ONCE_FETCH_TIMEOUT_MS = 5_000;

/**
 * Compute a capped exponential backoff delay with jitter. Pure and
 * deterministic for a given `rng`, so reconnect timing is unit-testable.
 *
 * @param attempt zero-based reconnect attempt count
 */
export function computeBackoffDelay(
  attempt: number,
  options: BackoffOptions,
  rng: () => number = Math.random,
): number {
  const base = Math.max(1, options.baseMs);
  const cap = Math.max(base, options.capMs);
  const safeAttempt = Math.max(0, Math.floor(attempt));
  const exponential = Math.min(cap, base * 2 ** safeAttempt);
  const jitterFraction = options.jitter ?? 0.3;
  const jitter = exponential * jitterFraction * Math.min(1, Math.max(0, rng()));
  return Math.min(cap, Math.floor(exponential + jitter));
}

/**
 * Normalize a status feed URL to a `ws`/`wss` URL ending in a `/ws/...` path.
 *
 * - `wss://host` / `ws://host`  → adds `/ws/status` when no path is present
 * - `https://host`             → `wss://host/ws/status`
 * - `http://host`              → `ws://host/ws/status`
 * - `host`                     → `wss://host/ws/status`
 */
export function deriveStatusWsUrl(input: string): string {
  let raw = (input ?? '').trim();
  if (raw.length === 0) throw new Error('createNodeStatusClient: empty status URL');
  if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(raw)) raw = `https://${raw}`;

  const parsed = new URL(raw);
  let protocol: string;
  if (parsed.protocol === 'wss:' || parsed.protocol === 'ws:') protocol = parsed.protocol;
  else if (parsed.protocol === 'https:') protocol = 'wss:';
  else protocol = 'ws:';

  let path = parsed.pathname;
  if (!path || path === '/') path = '/ws/status';

  return `${protocol}//${parsed.host}${path}${parsed.search}`;
}

interface StatusHub {
  emit(view: NodeStatusSetView): void;
  subscribe(cb: NodeStatusListener): () => void;
  current(): NodeStatusSetView | null;
}

function createHub(): StatusHub {
  const listeners = new Set<NodeStatusListener>();
  let latest: NodeStatusSetView | null = null;
  return {
    emit(view) {
      latest = view;
      for (const cb of [...listeners]) {
        try {
          cb(view);
        } catch {
          /* isolate subscriber errors */
        }
      }
    },
    subscribe(cb) {
      listeners.add(cb);
      if (latest) {
        try {
          cb(latest);
        } catch {
          /* isolate subscriber errors */
        }
      }
      return () => {
        listeners.delete(cb);
      };
    },
    current() {
      return latest;
    },
  };
}

async function toUint8Array(data: unknown): Promise<Uint8Array | null> {
  if (data == null) return null;
  if (data instanceof Uint8Array) return data;
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (ArrayBuffer.isView(data)) {
    const view = data as ArrayBufferView;
    return new Uint8Array(view.buffer, view.byteOffset, view.byteLength);
  }
  if (typeof Blob !== 'undefined' && data instanceof Blob) {
    return new Uint8Array(await data.arrayBuffer());
  }
  return null;
}

function resolveWebSocketCtor(explicit?: WebSocketCtor): WebSocketCtor | null {
  const ctor = explicit ?? (globalThis as { WebSocket?: WebSocketCtor }).WebSocket;
  return typeof ctor === 'function' ? ctor : null;
}

/** Detach handlers and close a socket without throwing. */
function teardownSocket(socket: WebSocketLike | null): void {
  if (!socket) return;
  socket.onopen = null;
  socket.onmessage = null;
  socket.onerror = null;
  socket.onclose = null;
  try {
    socket.close();
  } catch {
    /* ignore */
  }
}

// ---------------------------------------------------------------------------
// Remote mode
// ---------------------------------------------------------------------------

function createRemoteClient(options: NodeStatusClientOptions): NodeStatusClient {
  const hub = createHub();
  const url = deriveStatusWsUrl(options.url!);
  const backoff: BackoffOptions = {
    baseMs: options.backoffBaseMs ?? DEFAULT_BACKOFF_BASE_MS,
    capMs: options.backoffCapMs ?? DEFAULT_BACKOFF_CAP_MS,
  };
  const reportError = (error: unknown): void => {
    try {
      options.onError?.(error);
    } catch {
      /* never throw from the error reporter */
    }
  };

  let socket: WebSocketLike | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let attempt = 0;
  let stopped = false;

  const scheduleReconnect = (): void => {
    if (stopped || reconnectTimer) return;
    const delay = computeBackoffDelay(attempt, backoff);
    attempt += 1;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      connect();
    }, delay);
  };

  const handleFrame = async (data: unknown): Promise<void> => {
    try {
      const bytes = await toUint8Array(data);
      if (!bytes || bytes.length === 0) return;
      hub.emit(decodeNodeStatusSet(bytes));
    } catch (error) {
      reportError(error);
    }
  };

  const connect = (): void => {
    if (stopped) return;
    const Ctor = resolveWebSocketCtor(options.WebSocket);
    if (!Ctor) {
      reportError(new Error('createNodeStatusClient: WebSocket is not available'));
      scheduleReconnect();
      return;
    }

    let next: WebSocketLike;
    try {
      next = new Ctor(url);
    } catch (error) {
      reportError(error);
      scheduleReconnect();
      return;
    }

    socket = next;
    try {
      next.binaryType = 'arraybuffer';
    } catch {
      /* some implementations forbid setting binaryType before open */
    }

    next.onopen = () => {
      attempt = 0;
    };
    next.onmessage = (event) => {
      void handleFrame(event?.data);
    };
    next.onerror = (event) => {
      reportError((event as { error?: unknown })?.error ?? event ?? new Error('WebSocket error'));
    };
    next.onclose = () => {
      if (socket === next) socket = null;
      scheduleReconnect();
    };
  };

  connect();

  return {
    subscribe: hub.subscribe,
    current: hub.current,
    stop() {
      if (stopped) return;
      stopped = true;
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
      const closing = socket;
      socket = null;
      teardownSocket(closing);
    },
  };
}

// ---------------------------------------------------------------------------
// Helia mode
// ---------------------------------------------------------------------------

/** A resolved SDN bootstrap node descriptor derived from the edge relays. */
interface BootstrapDescriptor {
  peerId: string;
  multiaddr: string;
  statusUrl: string | null;
}

function extractPeerId(multiaddr: string): string | null {
  return multiaddr.match(/\/p2p\/([^/]+)$/)?.[1] ?? null;
}

/** Derive a `/ws/status` URL from a dialable wss/ws multiaddr, else null. */
function wsStatusUrlFromMultiaddr(multiaddr: string): string | null {
  const withoutPeer = multiaddr.replace(/\/p2p\/[^/]+$/, '');
  const match = withoutPeer.match(/^\/(dns[46]?|ip[46])\/([^/]+)\/tcp\/(\d+)\/(wss?)$/);
  if (!match) return null;
  const [, , host, portStr, transport] = match;
  const port = Number.parseInt(portStr, 10);
  const secure = transport === 'wss';
  const scheme = secure ? 'wss' : 'ws';
  const defaultPort = secure ? 443 : 80;
  const suffix = port === defaultPort ? '' : `:${port}`;
  return `${scheme}://${host}${suffix}/ws/status`;
}

async function loadBootstrapDescriptors(): Promise<BootstrapDescriptor[]> {
  try {
    const mod = (await import('../edge-discovery')) as { DEFAULT_EDGE_RELAYS?: readonly string[] };
    const relays = mod.DEFAULT_EDGE_RELAYS ?? [];
    const byPeer = new Map<string, BootstrapDescriptor>();
    for (const relay of relays) {
      const peerId = extractPeerId(relay);
      if (!peerId) continue;
      const statusUrl = wsStatusUrlFromMultiaddr(relay);
      const existing = byPeer.get(peerId);
      if (!existing) {
        byPeer.set(peerId, { peerId, multiaddr: relay, statusUrl });
      } else if (!existing.statusUrl && statusUrl) {
        existing.statusUrl = statusUrl;
        existing.multiaddr = relay;
      }
    }
    return [...byPeer.values()];
  } catch {
    return [];
  }
}

interface EpmResolverLike {
  setNode?(node: unknown): void;
  resolveByPeerID(peerId: string): Promise<{ dn?: string; legalName?: string } | null>;
}

async function createEpmResolverSafe(
  libp2p: unknown,
  reportError: (error: unknown) => void,
): Promise<EpmResolverLike | null> {
  try {
    const mod = (await import('../epm-resolver')) as {
      createEPMResolver?: () => EpmResolverLike;
    };
    if (typeof mod.createEPMResolver !== 'function') return null;
    const resolver = mod.createEPMResolver();
    if (libp2p && typeof resolver.setNode === 'function') resolver.setNode(libp2p);
    return resolver;
  } catch (error) {
    reportError(error);
    return null;
  }
}

function safeString(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function selfMultiaddrs(libp2p: unknown): string[] {
  try {
    const getMultiaddrs = (libp2p as { getMultiaddrs?: () => unknown[] })?.getMultiaddrs;
    if (typeof getMultiaddrs !== 'function') return [];
    return getMultiaddrs
      .call(libp2p)
      .map((addr) => safeString((addr as { toString?: () => string })?.toString?.()))
      .filter((addr) => addr.length > 0);
  } catch {
    return [];
  }
}

function isConnectedTo(libp2p: unknown, peerId: string): boolean {
  try {
    const getConnections = (libp2p as { getConnections?: (peer?: unknown) => unknown[] })
      ?.getConnections;
    if (typeof getConnections === 'function') {
      return getConnections
        .call(libp2p)
        .some(
          (conn) =>
            safeString((conn as { remotePeer?: { toString?: () => string } })?.remotePeer?.toString?.()) ===
            peerId,
        );
    }
    const getPeers = (libp2p as { getPeers?: () => unknown[] })?.getPeers;
    if (typeof getPeers === 'function') {
      return getPeers
        .call(libp2p)
        .some((peer) => safeString((peer as { toString?: () => string })?.toString?.()) === peerId);
    }
  } catch {
    /* fail-open */
  }
  return false;
}

/**
 * Open a status feed, resolve the first decoded frame, then close. Rejects on
 * timeout, socket error, or close-before-frame. Exported for reuse and testing.
 */
export function fetchNodeStatusOnce(
  url: string,
  webSocketCtor?: WebSocketCtor,
  timeoutMs: number = DEFAULT_ONCE_FETCH_TIMEOUT_MS,
): Promise<NodeStatusSetView> {
  return new Promise<NodeStatusSetView>((resolve, reject) => {
    const Ctor = resolveWebSocketCtor(webSocketCtor);
    if (!Ctor) {
      reject(new Error('fetchNodeStatusOnce: WebSocket is not available'));
      return;
    }

    let target: string;
    try {
      target = deriveStatusWsUrl(url);
    } catch (error) {
      reject(error);
      return;
    }

    let settled = false;
    let socket: WebSocketLike | null = null;

    const timer = setTimeout(() => {
      finish(() => reject(new Error('fetchNodeStatusOnce: timed out')));
    }, timeoutMs);

    const finish = (deliver: () => void): void => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      teardownSocket(socket);
      deliver();
    };

    try {
      socket = new Ctor(target);
    } catch (error) {
      finish(() => reject(error));
      return;
    }
    try {
      socket.binaryType = 'arraybuffer';
    } catch {
      /* ignore */
    }

    socket.onmessage = (event) => {
      void (async () => {
        try {
          const bytes = await toUint8Array(event?.data);
          if (!bytes || bytes.length === 0) return;
          const view = decodeNodeStatusSet(bytes);
          finish(() => resolve(view));
        } catch (error) {
          finish(() => reject(error));
        }
      })();
    };
    socket.onerror = (event) => {
      finish(() => reject((event as { error?: unknown })?.error ?? new Error('WebSocket error')));
    };
    socket.onclose = () => {
      finish(() => reject(new Error('fetchNodeStatusOnce: closed before a frame arrived')));
    };
  });
}

async function fetchRemoteCoords(
  statusUrl: string,
  peerId: string,
  webSocketCtor?: WebSocketCtor,
): Promise<{ lat: number; lon: number; geoLabel: string } | null> {
  const view = await fetchNodeStatusOnce(statusUrl, webSocketCtor, DEFAULT_GEO_FETCH_TIMEOUT_MS).catch(
    () => null,
  );
  if (!view) return null;
  const hasCoords = (node: NodeStatusView): boolean => node.lat !== 0 || node.lon !== 0;
  const match =
    view.nodes.find((node) => node.peerId === peerId && hasCoords(node)) ??
    view.nodes.find((node) => node.isSelf && hasCoords(node));
  if (!match) return null;
  return { lat: match.lat, lon: match.lon, geoLabel: match.geoLabel };
}

interface AssembleContext {
  startedAtMs: number;
  geoEnabled: boolean;
  webSocketCtor?: WebSocketCtor;
  reportError: (error: unknown) => void;
}

async function assembleHeliaView(
  helia: HeliaSDNNode,
  ctx: AssembleContext,
): Promise<NodeStatusSetView> {
  const libp2p = (helia as { libp2p?: unknown })?.libp2p;
  const selfPeerId = safeString(
    (libp2p as { peerId?: { toString?: () => string } })?.peerId?.toString?.(),
  );
  const nowSeconds = Math.floor(Date.now() / 1000);
  const nodes: NodeStatusView[] = [];

  nodes.push({
    peerId: selfPeerId,
    dn: '',
    org: '',
    vcard: '',
    lat: 0,
    lon: 0,
    geoLabel: '',
    online: true,
    isSelf: true,
    agent: `sdn-js/${SUITE_VERSION}`,
    uptimeS: Math.max(0, Math.floor((Date.now() - ctx.startedAtMs) / 1000)),
    lastSeen: nowSeconds,
    addrs: selfMultiaddrs(libp2p),
    trustLevel: '',
    role: 'self',
    latencyMs: 0,
    suiteVersion: SUITE_VERSION,
    standardsVersion: SPACEDATASTANDARDS_VERSION,
    // The self row is not on its own board, so it carries no provenance.
    source: '',
    pinned: false,
    pinNote: '',
  });

  const bootstraps = await loadBootstrapDescriptors();
  const epmResolver = bootstraps.length > 0 ? await createEpmResolverSafe(libp2p, ctx.reportError) : null;

  await Promise.all(
    bootstraps.map(async (boot) => {
      if (!boot.peerId || boot.peerId === selfPeerId) return;
      const online = isConnectedTo(libp2p, boot.peerId);

      let dn = '';
      let org = '';
      if (epmResolver) {
        try {
          const epm = await epmResolver.resolveByPeerID(boot.peerId);
          if (epm) {
            dn = safeString(epm.dn);
            org = safeString(epm.legalName);
          }
        } catch (error) {
          ctx.reportError(error);
        }
      }

      let lat = 0;
      let lon = 0;
      let geoLabel = '';
      if (ctx.geoEnabled && boot.statusUrl) {
        const coords = await fetchRemoteCoords(boot.statusUrl, boot.peerId, ctx.webSocketCtor).catch(
          () => null,
        );
        if (coords) {
          lat = coords.lat;
          lon = coords.lon;
          geoLabel = coords.geoLabel;
        }
      }

      nodes.push({
        peerId: boot.peerId,
        dn,
        org,
        vcard: '',
        lat,
        lon,
        geoLabel,
        online,
        isSelf: false,
        agent: '',
        uptimeS: 0,
        lastSeen: online ? nowSeconds : 0,
        addrs: boot.multiaddr ? [boot.multiaddr] : [],
        trustLevel: '',
        role: 'bootstrap',
        latencyMs: 0,
        suiteVersion: '',
        standardsVersion: '',
        // In-browser Helia mode has no node-side pin store to consult, so a
        // bootstrap row is admitted for the one reason this client can prove:
        // it is connected, or it is not there at all. Claiming 'pinned' here
        // would be inventing provenance the client cannot know.
        source: online ? 'connected' : '',
        pinned: false,
        pinNote: '',
      });
    }),
  );

  return {
    nodes,
    generatedAt: Date.now(),
    sourcePeerId: selfPeerId,
  };
}

function createHeliaClient(options: NodeStatusClientOptions): NodeStatusClient {
  const hub = createHub();
  const helia = options.helia!;
  const intervalMs = Math.max(250, options.refreshIntervalMs ?? DEFAULT_HELIA_REFRESH_MS);
  const reportError = (error: unknown): void => {
    try {
      options.onError?.(error);
    } catch {
      /* never throw from the error reporter */
    }
  };
  const ctx: AssembleContext = {
    startedAtMs: Date.now(),
    geoEnabled: options.geo !== false,
    webSocketCtor: options.WebSocket,
    reportError,
  };

  let stopped = false;
  let running = false;
  let timer: ReturnType<typeof setInterval> | null = null;

  const refresh = async (): Promise<void> => {
    if (stopped || running) return;
    running = true;
    try {
      const view = await assembleHeliaView(helia, ctx);
      if (!stopped) hub.emit(view);
    } catch (error) {
      reportError(error);
    } finally {
      running = false;
    }
  };

  void refresh();
  timer = setInterval(() => {
    void refresh();
  }, intervalMs);

  return {
    subscribe: hub.subscribe,
    current: hub.current,
    stop() {
      if (stopped) return;
      stopped = true;
      if (timer) {
        clearInterval(timer);
        timer = null;
      }
    },
  };
}

/**
 * Create a node status client. Supply `url` for remote mode (a node's
 * `/ws/status` feed) or `helia` for local client-side assembly. When both are
 * given, remote mode wins.
 */
export function createNodeStatusClient(options: NodeStatusClientOptions): NodeStatusClient {
  if (options.url) return createRemoteClient(options);
  if (options.helia) return createHeliaClient(options);
  throw new Error(
    'createNodeStatusClient requires either { url } (remote) or { helia } (local) options.',
  );
}
