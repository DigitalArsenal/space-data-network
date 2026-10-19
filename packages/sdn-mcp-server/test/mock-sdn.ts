/**
 * In-process mock of the SDN node admin HTTP API.
 *
 * Response shapes are synthetic but match the field names produced by the
 * real handlers in sdn-server; /api/v1/data/* answers $QRP FlatBuffers only
 * (internal/api/data_query_qrp.go, wire.go), built here with the SDS builders:
 *   internal/api/data.go, internal/api/catalog.go, internal/api/publish.go,
 *   internal/api/log.go, internal/peers/api.go, internal/directory/http.go,
 *   cmd/spacedatanetwork/main.go (node info / observed peers).
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import * as flatbuffers from "flatbuffers";
import { QRP } from "spacedatastandards.org/lib/js/QRP/QRP.js";
import { QRPColumn } from "spacedatastandards.org/lib/js/QRP/QRPColumn.js";
import { QRPRow } from "spacedatastandards.org/lib/js/QRP/QRPRow.js";
import { QRPValue } from "spacedatastandards.org/lib/js/QRP/QRPValue.js";
import { QRPFilter } from "spacedatastandards.org/lib/js/QRP/QRPFilter.js";
import { qrpKind } from "spacedatastandards.org/lib/js/QRP/qrpKind.js";
import { qrpStatus } from "spacedatastandards.org/lib/js/QRP/qrpStatus.js";
import { qrpTimeProfile } from "spacedatastandards.org/lib/js/QRP/qrpTimeProfile.js";
import { qrpValueKind } from "spacedatastandards.org/lib/js/QRP/qrpValueKind.js";

export interface MockState {
  requireAuthForPublish: boolean;
  publishes: Array<{ schema: string; bytes: Buffer; cookie?: string }>;
  catalogAvailable: boolean;
  /** Every decoded /api/v1/data/query request frame. */
  queries: Array<Record<string, unknown>>;
}

export interface MockSdn {
  url: string;
  state: MockState;
  close(): Promise<void>;
}

export const NODE_INFO = {
  peer_id: "12D3KooWMockPeerExample111111111111111111111111",
  mode: "full",
  version: "v25.2.1",
  agent_version: "sdn/v25.2.1",
  suite_version: "25.2",
  standards_version: "1.0",
  advertisement_flag: "public",
  listen_addresses: ["/ip4/127.0.0.1/tcp/4001"],
  xpub: "xpub6MockXpubExample",
  EMAIL: "ops@example.org",
};

export const CATALOG = {
  peer_id: NODE_INFO.peer_id,
  schemas: [
    {
      name: "OMM.fbs",
      record_count: 12894,
      total_bytes: 5_242_880,
      oldest_epoch: "2025-01-01T00:00:00Z",
      newest_epoch: "2026-06-09T12:00:00Z",
    },
    {
      name: "CAT.fbs",
      record_count: 28411,
      total_bytes: 9_437_184,
      oldest_epoch: "2024-06-01T00:00:00Z",
      newest_epoch: "2026-06-09T12:00:00Z",
    },
  ],
  capabilities: ["data_query", "data_publish", "pubsub"],
  rate_limits: { query_per_minute: 120, publish_per_minute: 30, max_record_bytes: 10485760 },
};

/** Size-prefixed record frames the query lane serves: one stored OMM-like frame per lane run. */
export const QUERY_RECORD = sizePrefixed(Buffer.from("....$OMM-mock-record", "latin1"));
export const EPOCH_RECORD = sizePrefixed(Buffer.from("....$OMM-mock-epoch-record", "latin1"));

export const REGISTRY_PEERS = [
  {
    id: "12D3KooWPeerAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    addrs: ["/ip4/10.0.0.2/tcp/4001"],
    trust_level: "trusted",
    name: "Example Observatory",
    organization: "ExampleOrg",
    groups: ["providers"],
    added_at: "2026-01-15T00:00:00Z",
    last_connected: "2026-06-09T11:00:00Z",
    connection_count: 42,
    messages_received: 1000,
    messages_sent: 900,
    bytes_received: 1048576,
    bytes_sent: 524288,
  },
];

export const OBSERVED_PEERS = {
  peers: [
    { peer_id: REGISTRY_PEERS[0].id, agent: "sdn/v25.2.1" },
    { peer_id: "12D3KooWPeerBbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", agent: "sdn/v25.2.1" },
  ],
};

export const LOG_HEADS = {
  schema_type: "OMM.fbs",
  heads: [
    {
      schema_type: "OMM.fbs",
      publisher_peer_id: REGISTRY_PEERS[0].id,
      head_sequence: 1042,
      head_entry_hash: "abcd1234",
      record_count: 12894,
      oldest_epoch_day: 20250101,
      newest_epoch_day: 20260609,
    },
  ],
};

export const LOG_ENTRIES = {
  schema_type: "OMM.fbs",
  publisher_peer_id: REGISTRY_PEERS[0].id,
  since_sequence: 1040,
  count: 2,
  entries: [
    { data_base64: Buffer.from("entry-1041").toString("base64"), bytes: 10 },
    { data_base64: Buffer.from("entry-1042").toString("base64"), bytes: 10 },
  ],
};

export const DIRECTORY_NODES = {
  kind: "node",
  count: 1,
  results: [
    {
      peer_id: REGISTRY_PEERS[0].id,
      name: "Example Observatory",
      organization: "ExampleOrg",
      xpub: "xpub6PeerExample",
      domains: ["obs.example.org"],
    },
  ],
};

export const DIRECTORY_USERS = { kind: "user", count: 0, results: [] };

/** Summary rows: per-schema totals (source_name "") and per-lane rows. */
export const DATA_SUMMARY_COLUMNS = ["schema_name", "source_name", "provider_id", "batch_id", "count", "total_bytes"];
export const DATA_SUMMARY_ROWS: Array<Array<string | number>> = [
  ...CATALOG.schemas.map((s) => [s.name, "", "", "", s.record_count, s.total_bytes]),
  ["OMM.fbs", "gp", "celestrak", "b-1", 12894, 5_242_880],
];

function sizePrefixed(payload: Buffer): Uint8Array {
  const frame = Buffer.alloc(4 + payload.length);
  frame.writeUInt32LE(payload.length, 0);
  payload.copy(frame, 4);
  return new Uint8Array(frame);
}

function concat(parts: Uint8Array[]): Buffer {
  return Buffer.concat(parts.map((part) => Buffer.from(part.buffer, part.byteOffset, part.byteLength)));
}

function cellOf(b: flatbuffers.Builder, value: string | number): number {
  if (typeof value === "number") return QRPValue.createQRPValue(b, qrpValueKind.Int, false, BigInt(value), 0, 0, 0);
  return QRPValue.createQRPValue(b, qrpValueKind.Text, false, 0n, 0, b.createString(value), 0);
}

/** One $QRP{KIND=Page} of COLUMNS + ROWS. */
function tablePage(columns: string[], rows: Array<Array<string | number>>, status = qrpStatus.Ok): Uint8Array {
  const b = new flatbuffers.Builder(512);
  const cols = QRP.createColumnsVector(
    b,
    columns.map((name, j) => QRPColumn.createQRPColumn(b, b.createString(name), b.createString(typeof rows[0]?.[j] === "number" ? "int64" : "string"), 0, false, false))
  );
  const rowOffsets = QRP.createRowsVector(b, rows.map((cells) => QRPRow.createQRPRow(b, QRPRow.createCellsVector(b, cells.map((c) => cellOf(b, c))))));
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Page);
  QRP.addStatus(b, status);
  QRP.addGeneratedAtMs(b, BigInt(Date.now()));
  QRP.addColumns(b, cols);
  QRP.addRows(b, rowOffsets);
  QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
  return b.asUint8Array();
}

/** One /data/query run: a $QRP page header naming the lane, then its record frames. */
function queryRun(schema: string, page: number, limit: number, lane: { provider: string; source: string; batch: string }, records: Uint8Array[]): Buffer {
  const b = new flatbuffers.Builder(256);
  const schemaOffset = b.createString(schema);
  const provider = b.createString(lane.provider);
  const source = b.createString(lane.source);
  const batch = b.createString(lane.batch);
  const origin = b.createString(NODE_INFO.peer_id);
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Page);
  QRP.addSchemaName(b, schemaOffset);
  QRP.addProviderId(b, provider);
  QRP.addSourceName(b, source);
  QRP.addBatchId(b, batch);
  QRP.addOriginId(b, origin);
  QRP.addPage(b, page);
  QRP.addLimit(b, limit);
  QRP.addTotalCount(b, BigInt(records.length));
  QRP.addRecordCount(b, records.length);
  QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
  return concat([b.asUint8Array(), ...records]);
}

/** One $QRP{KIND=Error} frame. */
function errorFrame(code: string, message: string, httpStatus: number): Uint8Array {
  const b = new flatbuffers.Builder(128);
  const codeOffset = b.createString(code);
  const messageOffset = b.createString(message);
  QRP.startQRP(b);
  QRP.addKind(b, qrpKind.Error);
  QRP.addStatus(b, qrpStatus.Error);
  QRP.addErrorCode(b, codeOffset);
  QRP.addMessage(b, messageOffset);
  QRP.addHttpStatus(b, httpStatus);
  QRP.finishSizePrefixedQRPBuffer(b, QRP.endQRP(b));
  return b.asUint8Array();
}

/** The request frame's fields, as the node reads them. */
function readRequest(body: Buffer): Record<string, unknown> {
  if (body.length < 12 || body.readUInt32LE(0) !== body.length - 4) throw new Error("body is not one size-prefixed frame");
  const payload = new Uint8Array(body.buffer, body.byteOffset + 4, body.length - 4);
  if (Buffer.from(payload.subarray(4, 8)).toString("latin1") !== "$QRP") throw new Error("frame is not a $QRP buffer");
  const q = QRP.getRootAsQRP(new flatbuffers.ByteBuffer(payload));
  if (q.KIND() !== qrpKind.Request) throw new Error("frame is not a $QRP request");
  const filter = new QRPFilter();
  const filters: Array<{ field: string; value: string }> = [];
  for (let i = 0; i < q.filtersLength(); i++) {
    q.FILTERS(i, filter);
    filters.push({ field: filter.FIELD() ?? "", value: filter.VALUE() ?? "" });
  }
  return {
    schema: q.SCHEMA_NAME() ?? "",
    provider_id: q.PROVIDER_ID() ?? "",
    origin_id: q.ORIGIN_ID() ?? "",
    time_profile: q.TIME_PROFILE(),
    from_epoch_ms: Number(q.FROM_EPOCH_MS()),
    to_epoch_ms: Number(q.TO_EPOCH_MS()),
    profile_epoch_ms: Number(q.PROFILE_EPOCH_MS()),
    page: q.PAGE(),
    limit: q.LIMIT(),
    filters,
  };
}

function stream(res: ServerResponse, status: number, body: Uint8Array): void {
  res.writeHead(status, { "Content-Type": "application/vnd.sdn.flatbuffers.stream" });
  res.end(Buffer.from(body.buffer, body.byteOffset, body.byteLength));
}

function json(res: ServerResponse, status: number, payload: unknown): void {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(payload));
}

async function readBody(req: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(chunk as Buffer);
  return Buffer.concat(chunks);
}

export async function startMockSdn(): Promise<MockSdn> {
  const state: MockState = {
    requireAuthForPublish: false,
    publishes: [],
    catalogAvailable: true,
    queries: [],
  };

  const server: Server = createServer(async (req, res) => {
    const url = new URL(req.url ?? "/", "http://localhost");
    const path = url.pathname;

    try {
      if (req.method === "GET" && path === "/api/node/info") return json(res, 200, NODE_INFO);
      if (req.method === "GET" && path === "/api/v1/data/health") {
        return stream(res, 200, tablePage(["component", "status"], [["spaceaware-data-api", "ok"]]));
      }
      if (req.method === "GET" && path === "/api/v1/data/summary") {
        return stream(res, 200, tablePage(DATA_SUMMARY_COLUMNS, DATA_SUMMARY_ROWS));
      }
      if (req.method === "GET" && path === "/api/v1/catalog") {
        if (!state.catalogAvailable) return json(res, 404, { error: "not found" });
        return json(res, 200, CATALOG);
      }
      if (req.method === "GET" && path === "/api/peers/sdn") return json(res, 200, OBSERVED_PEERS);
      if (req.method === "GET" && path === "/api/peers") return json(res, 200, REGISTRY_PEERS);
      if (req.method === "GET" && path.startsWith("/api/peers/")) {
        const id = decodeURIComponent(path.slice("/api/peers/".length));
        const match = REGISTRY_PEERS.find((p) => p.id === id);
        if (!match) return json(res, 404, { error: "peer not found" });
        return json(res, 200, match);
      }
      if (req.method === "POST" && path === "/api/v1/data/query") {
        let request: Record<string, unknown>;
        try {
          request = readRequest(await readBody(req));
        } catch (err) {
          return stream(res, 415, errorFrame("unsupported_media_type", String(err), 415));
        }
        state.queries.push(request);
        const schema = String(request.schema);
        if (!schema) return stream(res, 400, errorFrame("invalid_request", "SCHEMA_NAME is required", 400));
        const page = Number(request.page) || 1;
        const limit = Number(request.limit) || 100;
        const lane = { provider: "celestrak", source: "gp", batch: "b-1" };
        if (request.time_profile === qrpTimeProfile.Unspecified) return stream(res, 200, queryRun(schema, page, limit, lane, [QUERY_RECORD]));
        if (request.time_profile === qrpTimeProfile.All && !(Number(request.from_epoch_ms) && Number(request.to_epoch_ms))) {
          return stream(res, 400, errorFrame("invalid_request", "a window query takes FROM_EPOCH_MS and TO_EPOCH_MS", 400));
        }
        return stream(res, 200, queryRun(schema, page, limit, lane, [EPOCH_RECORD]));
      }
      if (req.method === "POST" && path.startsWith("/api/v1/data/publish/")) {
        const schema = decodeURIComponent(path.slice("/api/v1/data/publish/".length)).replace(/\/$/, "");
        const cookie = req.headers.cookie;
        if (state.requireAuthForPublish && !(cookie ?? "").includes("sdn_wallet_session=")) {
          return json(res, 401, { code: "unauthorized", message: "not authenticated" });
        }
        const bytes = await readBody(req);
        if (bytes.length === 0) return json(res, 400, { error: "empty request body" });
        state.publishes.push({ schema, bytes, cookie });
        return json(res, 201, {
          cid: "bafkreipublishedcid",
          schema,
          stored_at: new Date().toISOString(),
          bytes: bytes.length,
        });
      }
      if (req.method === "GET" && /^\/api\/v1\/log\/[^/]+\/heads$/.test(path)) {
        return json(res, 200, LOG_HEADS);
      }
      if (req.method === "GET" && /^\/api\/v1\/log\/[^/]+\/entries$/.test(path)) {
        if (!url.searchParams.get("publisher")) {
          return json(res, 400, { error: "missing required query parameter: publisher" });
        }
        return json(res, 200, LOG_ENTRIES);
      }
      if (req.method === "GET" && path === "/api/directory/nodes") return json(res, 200, DIRECTORY_NODES);
      if (req.method === "GET" && path === "/api/directory/users") return json(res, 200, DIRECTORY_USERS);

      return json(res, 404, { error: `no mock for ${req.method} ${path}` });
    } catch (err) {
      return json(res, 500, { error: String(err) });
    }
  });

  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;

  return {
    url: `http://127.0.0.1:${port}`,
    state,
    close: () => new Promise<void>((resolve, reject) => server.close((e) => (e ? reject(e) : resolve()))),
  };
}
