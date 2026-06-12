/**
 * Thin HTTP client for the SDN node admin/API server.
 *
 * Endpoint paths and request/response shapes mirror the handlers in
 * sdn-server (Go):
 *   - /api/node/info                    cmd/spacedatanetwork/main.go
 *   - /api/v1/data/*                    internal/api/data.go
 *   - /api/v1/catalog                   internal/api/catalog.go
 *   - /api/v1/data/publish/{schema}     internal/api/publish.go
 *   - /api/v1/log/{schema}/*            internal/api/log.go
 *   - /api/peers, /api/peers/sdn        internal/peers/api.go, main.go
 *   - /api/directory/nodes|users        internal/directory/http.go
 */
import type { SdnConfig } from "./config.js";

const SESSION_COOKIE = "sdn_wallet_session";

/** Raised when the SDN node cannot be reached at all (daemon down, wrong URL). */
export class SdnConnectionError extends Error {
  constructor(baseUrl: string, cause: unknown) {
    super(
      `Cannot reach the SDN node at ${baseUrl} (${describeCause(cause)}). ` +
        `Is the SDN daemon running? Start it with: spacedatanetwork daemon ` +
        `— or point SDN_API_URL at a running node's admin API.`
    );
    this.name = "SdnConnectionError";
  }
}

/** Raised when the node responds with a non-2xx status. */
export class SdnHttpError extends Error {
  readonly status: number;
  readonly body: string;
  constructor(method: string, path: string, status: number, body: string) {
    super(`SDN API ${method} ${path} failed with HTTP ${status}: ${truncate(body, 500)}`);
    this.name = "SdnHttpError";
    this.status = status;
    this.body = body;
  }
}

function describeCause(cause: unknown): string {
  if (cause instanceof Error) {
    const inner = (cause as { cause?: unknown }).cause;
    if (inner instanceof Error && inner.message) return inner.message;
    return cause.message || cause.name;
  }
  return String(cause);
}

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + "…" : s;
}

export interface QueryRecordsRequest {
  schema: string;
  provider_id?: string;
  source_name?: string;
  batch_id?: string;
  producer_peer_id?: string;
  producer_public_key?: string;
  peer_id?: string;
  limit?: number;
  offset?: number;
  include_data?: boolean;
}

export interface EpochQueryRequest {
  schema: string;
  day?: string;
  from?: string;
  to?: string;
  at?: string;
  provider_id?: string;
  source_name?: string;
  batch_id?: string;
  entity_id?: string;
  norad_cat_id?: number;
  limit?: number;
  include_data?: boolean;
}

export class SdnClient {
  readonly baseUrl: string;
  readonly token?: string;
  private readonly fetchImpl: typeof fetch;

  constructor(config: SdnConfig, fetchImpl: typeof fetch = fetch) {
    this.baseUrl = config.baseUrl.replace(/\/+$/, "");
    this.token = config.token;
    this.fetchImpl = fetchImpl;
  }

  // ---- low-level helpers -------------------------------------------------

  private headers(extra: Record<string, string> = {}): Record<string, string> {
    const h: Record<string, string> = { Accept: "application/json", ...extra };
    if (this.token) h["Cookie"] = `${SESSION_COOKIE}=${this.token}`;
    return h;
  }

  private async request(method: string, path: string, init: RequestInit): Promise<Response> {
    const url = `${this.baseUrl}${path}`;
    let res: Response;
    try {
      res = await this.fetchImpl(url, { method, ...init });
    } catch (err) {
      throw new SdnConnectionError(this.baseUrl, err);
    }
    if (!res.ok) {
      const body = await res.text().catch(() => "");
      throw new SdnHttpError(method, path, res.status, body);
    }
    return res;
  }

  async getJson<T = unknown>(path: string, query?: Record<string, string | number | boolean | undefined>): Promise<T> {
    let full = path;
    if (query) {
      const qs = new URLSearchParams();
      for (const [k, v] of Object.entries(query)) {
        if (v !== undefined && v !== "") qs.set(k, String(v));
      }
      const encoded = qs.toString();
      if (encoded) full += `?${encoded}`;
    }
    const res = await this.request("GET", full, { headers: this.headers() });
    return (await res.json()) as T;
  }

  async postJson<T = unknown>(path: string, body: unknown): Promise<T> {
    const res = await this.request("POST", path, {
      headers: this.headers({ "Content-Type": "application/json" }),
      body: JSON.stringify(body),
    });
    return (await res.json()) as T;
  }

  // ---- node / health -----------------------------------------------------

  /** GET /api/node/info — peer ID, xpub, version, listen addresses, EPM fields. */
  nodeInfo(): Promise<Record<string, unknown>> {
    return this.getJson("/api/node/info");
  }

  /** GET /api/v1/data/health — data API liveness. */
  dataHealth(): Promise<Record<string, unknown>> {
    return this.getJson("/api/v1/data/health");
  }

  /** GET /api/node/epm/json — this node's Entity Profile Message as JSON. */
  nodeEpmJson(): Promise<Record<string, unknown>> {
    return this.getJson("/api/node/epm/json");
  }

  // ---- peers ---------------------------------------------------------------

  /** GET /api/peers — peers known to the trust registry. */
  listPeers(): Promise<unknown> {
    return this.getJson("/api/peers");
  }

  /** GET /api/peers/sdn — snapshot of observed SDN peers on the network. */
  observedPeers(): Promise<unknown> {
    return this.getJson("/api/peers/sdn");
  }

  /** GET /api/peers/{peerID} — a single registry entry. */
  getPeer(peerId: string): Promise<Record<string, unknown>> {
    return this.getJson(`/api/peers/${encodeURIComponent(peerId)}`);
  }

  // ---- schemas / catalog ---------------------------------------------------

  /** GET /api/v1/catalog — schemas with record counts, capabilities, rate limits. */
  catalog(): Promise<Record<string, unknown>> {
    return this.getJson("/api/v1/catalog");
  }

  /** GET /api/v1/data/summary — total records/bytes by schema and source. */
  dataSummary(): Promise<Record<string, unknown>> {
    return this.getJson("/api/v1/data/summary");
  }

  // ---- records ---------------------------------------------------------------

  /** POST /api/v1/data/query — raw record query by schema + provenance filters. */
  queryRecords(req: QueryRecordsRequest): Promise<Record<string, unknown>> {
    return this.postJson("/api/v1/data/query", req);
  }

  /** GET /api/v1/data/epoch — epoch/time-range query (RFC3339 from/to, day, NORAD id). */
  epochQuery(req: EpochQueryRequest): Promise<Record<string, unknown>> {
    const { schema, ...rest } = req;
    return this.getJson("/api/v1/data/epoch", { schema, ...rest });
  }

  /**
   * POST /api/v1/data/publish/{schema} — publish a single record.
   * The body must be the raw FlatBuffer bytes for the schema; the node
   * validates it server-side before storing. Requires an authenticated
   * session (SDN_API_TOKEN) on nodes with admin.require_auth enabled.
   */
  async publishRecord(schema: string, data: Uint8Array): Promise<Record<string, unknown>> {
    const path = `/api/v1/data/publish/${encodeURIComponent(schema)}`;
    const res = await this.request("POST", path, {
      headers: this.headers({ "Content-Type": "application/octet-stream" }),
      body: data,
    });
    return (await res.json()) as Record<string, unknown>;
  }

  // ---- publication log (network message feed) -------------------------------

  /** GET /api/v1/log/{schema}/heads — publication-log heads for all publishers. */
  logHeads(schema: string): Promise<Record<string, unknown>> {
    return this.getJson(`/api/v1/log/${encodeURIComponent(schema)}/heads`);
  }

  /** GET /api/v1/log/{schema}/entries — log entries for a publisher since a sequence. */
  logEntries(schema: string, publisher: string, since?: number, limit?: number): Promise<Record<string, unknown>> {
    return this.getJson(`/api/v1/log/${encodeURIComponent(schema)}/entries`, {
      publisher,
      since,
      limit,
    });
  }

  // ---- directory / identity -------------------------------------------------

  /** GET /api/directory/nodes?q= — search directory node records. */
  directoryNodes(q: string, limit?: number): Promise<unknown> {
    return this.getJson("/api/directory/nodes", { q, limit });
  }

  /** GET /api/directory/users?q= — search directory user records. */
  directoryUsers(q: string, limit?: number): Promise<unknown> {
    return this.getJson("/api/directory/users", { q, limit });
  }
}
