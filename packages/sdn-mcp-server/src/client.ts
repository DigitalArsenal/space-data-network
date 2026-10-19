/**
 * Thin HTTP client for the SDN node admin/API server.
 *
 * Endpoint paths and request/response shapes mirror the handlers in
 * sdn-server (Go):
 *   - /api/node/info                    cmd/spacedatanetwork/main.go
 *   - /api/v1/data/*                    internal/api/data.go, data_query_qrp.go
 *                                       ($QRP FlatBuffers only: src/qrp.ts)
 *   - /api/v1/catalog                   internal/api/catalog.go
 *   - /api/v1/data/publish/{schema}     internal/api/publish.go
 *   - /api/v1/log/{schema}/*            internal/api/log.go
 *   - /api/peers, /api/peers/sdn        internal/peers/api.go, main.go
 *   - /api/directory/nodes|users        internal/directory/http.go
 */
import type { SdnConfig } from "./config.js";
import {
  FLATBUFFER_STREAM_TYPE,
  decodeQrpErrorText,
  decodeQrpQueryAnswer,
  decodeQrpTable,
  encodeQrpRequest,
  qrpFilterOp,
  qrpStatus,
  qrpTimeProfile,
  type QrpQueryAnswer,
  type QrpRequest,
  type QrpTable,
} from "./qrp.js";

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
  peer_id?: string;
  limit?: number;
  offset?: number;
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
  offset?: number;
}

const DAY_MS = 86_400_000;
/** 9999-12-31T23:59:59.999Z: the open end of a window. */
const OPEN_END_MS = 253_402_300_799_999;

function epochMs(label: string, value: string): number {
  const ms = Date.parse(value);
  if (!Number.isFinite(ms)) throw new Error(`${label} is not an RFC3339 instant: ${value}`);
  return ms;
}

/** PAGE/LIMIT for an offset: the node pages by PAGE (offset = (PAGE-1)*LIMIT). */
function pageOf(limit: number | undefined, offset: number | undefined): Pick<QrpRequest, "page" | "limit"> {
  const size = limit ?? 100;
  const skip = offset ?? 0;
  if (skip % size !== 0) throw new Error(`offset ${skip} must be a multiple of limit ${size}`);
  return { limit: size, page: skip / size + 1 };
}

/** The epoch selection of /data/query: a window, a point in time, or the latest state. */
export function epochQueryRequest(req: EpochQueryRequest): QrpRequest {
  const filters: NonNullable<QrpRequest["filters"]> = [];
  if (req.norad_cat_id !== undefined) filters.push({ field: "NORAD_CAT_ID", op: qrpFilterOp.Eq, value: String(req.norad_cat_id) });
  if (req.entity_id) filters.push({ field: "ENTITY_ID", op: qrpFilterOp.Eq, value: req.entity_id });
  const selection: Partial<QrpRequest> = {};
  if (req.day) {
    const from = epochMs("day", `${req.day}T00:00:00Z`);
    Object.assign(selection, { timeProfile: qrpTimeProfile.All, fromEpochMs: from, toEpochMs: from + DAY_MS - 1 });
  } else if (req.from || req.to) {
    Object.assign(selection, {
      timeProfile: qrpTimeProfile.All,
      fromEpochMs: req.from ? epochMs("from", req.from) : 1,
      toEpochMs: req.to ? epochMs("to", req.to) : OPEN_END_MS,
    });
  } else if (req.at) {
    Object.assign(selection, { timeProfile: qrpTimeProfile.Nearest, profileEpochMs: epochMs("at", req.at) });
  } else {
    Object.assign(selection, { timeProfile: qrpTimeProfile.Latest });
  }
  return {
    schema: req.schema,
    providerId: req.provider_id,
    sourceName: req.source_name,
    batchId: req.batch_id,
    ...selection,
    ...(filters.length ? { filters } : {}),
    ...pageOf(req.limit, req.offset),
  };
}

function text(row: Record<string, unknown>, name: string): string {
  const value = row[name];
  return typeof value === "string" ? value : "";
}

function count(row: Record<string, unknown>, name: string): number {
  const value = row[name];
  return typeof value === "number" ? value : 0;
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

  private streamHeaders(): Record<string, string> {
    const h: Record<string, string> = { Accept: FLATBUFFER_STREAM_TYPE, "Content-Type": FLATBUFFER_STREAM_TYPE };
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
      const bytes = new Uint8Array(await res.arrayBuffer().catch(() => new ArrayBuffer(0)));
      const body = decodeQrpErrorText(bytes) ?? new TextDecoder().decode(bytes);
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

  /** GET of a route that answers one $QRP page of COLUMNS + ROWS. */
  async getQrpTable(path: string): Promise<QrpTable> {
    const res = await this.request("GET", path, { headers: this.streamHeaders() });
    return decodeQrpTable(new Uint8Array(await res.arrayBuffer()));
  }

  /** POST /api/v1/data/query with one $QRP request frame. */
  async postQrpQuery(request: QrpRequest): Promise<QrpQueryAnswer> {
    const res = await this.request("POST", "/api/v1/data/query", {
      headers: this.streamHeaders(),
      body: encodeQrpRequest(request),
    });
    return decodeQrpQueryAnswer(new Uint8Array(await res.arrayBuffer()));
  }

  // ---- node / health -----------------------------------------------------

  /** GET /api/node/info — peer ID, xpub, version, listen addresses, EPM fields. */
  nodeInfo(): Promise<Record<string, unknown>> {
    return this.getJson("/api/node/info");
  }

  /** GET /api/v1/data/health — data API liveness: STATUS plus rows [component, status]. */
  async dataHealth(): Promise<Record<string, unknown>> {
    const table = await this.getQrpTable("/api/v1/data/health");
    const components: Record<string, string> = {};
    for (const row of table.rows) components[text(row, "component")] = text(row, "status");
    return {
      status: table.status === qrpStatus.Ok ? "ok" : table.status === qrpStatus.Cold ? "cold" : "error",
      generated_at_ms: table.generated_at_ms,
      components,
    };
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

  /**
   * GET /api/v1/data/summary — records/bytes by schema and source. Rows are
   * per (schema, lane), then one total per schema (every lane field ""),
   * then the store total (schema_name "" too).
   */
  async dataSummary(): Promise<Record<string, unknown>> {
    const table = await this.getQrpTable("/api/v1/data/summary");
    const laneless = (row: (typeof table.rows)[number]) =>
      ["provider_id", "source_name", "batch_id", "producer_peer_id", "producer_public_key"].every((name) => text(row, name) === "");
    const store = table.rows.find((row) => laneless(row) && text(row, "schema_name") === "");
    const totals = table.rows.filter((row) => laneless(row) && text(row, "schema_name") !== "");
    const lanes = table.rows.filter((row) => !laneless(row));
    const schemas = totals.map((row) => ({ name: text(row, "schema_name"), count: count(row, "count"), total_bytes: count(row, "total_bytes") }));
    const counted = totals.length ? totals : lanes;
    return {
      as_of_ms: table.as_of_ms,
      total_records: store ? count(store, "count") : counted.reduce((sum, row) => sum + count(row, "count"), 0),
      total_bytes: store ? count(store, "total_bytes") : counted.reduce((sum, row) => sum + count(row, "total_bytes"), 0),
      schemas,
      sources: lanes,
    };
  }

  // ---- records ---------------------------------------------------------------

  /** POST /api/v1/data/query — raw record query by schema + provenance filters. */
  queryRecords(req: QueryRecordsRequest): Promise<QrpQueryAnswer> {
    return this.postQrpQuery({
      schema: req.schema,
      providerId: req.provider_id,
      sourceName: req.source_name,
      batchId: req.batch_id,
      producerPeerId: req.producer_peer_id,
      originId: req.peer_id,
      ...pageOf(req.limit, req.offset),
    });
  }

  /** POST /api/v1/data/query by TIME_PROFILE — epoch/time-range query (RFC3339 from/to, day, at, NORAD id). */
  epochQuery(req: EpochQueryRequest): Promise<QrpQueryAnswer> {
    return this.postQrpQuery(epochQueryRequest(req));
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
