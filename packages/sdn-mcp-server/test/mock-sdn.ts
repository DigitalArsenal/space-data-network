/**
 * In-process mock of the SDN node admin HTTP API.
 *
 * Response shapes are synthetic but match the field names produced by the
 * real handlers in sdn-server:
 *   internal/api/data.go, internal/api/catalog.go, internal/api/publish.go,
 *   internal/api/log.go, internal/peers/api.go, internal/directory/http.go,
 *   cmd/spacedatanetwork/main.go (node info / observed peers).
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

export interface MockState {
  requireAuthForPublish: boolean;
  publishes: Array<{ schema: string; bytes: Buffer; cookie?: string }>;
  catalogAvailable: boolean;
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

export const QUERY_RESULT = {
  schema: "OMM.fbs",
  count: 1,
  results: [
    {
      cid: "bafkreimockcid",
      id: "1",
      provider_id: "celestrak",
      source_name: "gp",
      batch_id: "b-1",
      producer_peer_id: NODE_INFO.peer_id,
      producer_public_key: "02abc",
      peer_id: NODE_INFO.peer_id,
      timestamp: "2026-06-09T12:00:00Z",
    },
  ],
};

export const EPOCH_RESULT = {
  schema: "OMM.fbs",
  profile: "day",
  query: { schema: "OMM.fbs", profile: "day", limit: 1000 },
  count: 1,
  total_count: 1,
  results: [
    {
      cid: "bafkreimockepochcid",
      schema: "OMM.fbs",
      epoch: "2026-06-09T00:00:00Z",
      provider_id: "celestrak",
      source_name: "gp",
    },
  ],
};

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

export const DATA_SUMMARY = {
  total_records: 41305,
  total_bytes: 14680064,
  schemas: CATALOG.schemas.map((s) => ({ name: s.name, record_count: s.record_count, total_bytes: s.total_bytes })),
  sources: [{ provider_id: "celestrak", source_name: "gp", record_count: 41305 }],
};

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
  };

  const server: Server = createServer(async (req, res) => {
    const url = new URL(req.url ?? "/", "http://localhost");
    const path = url.pathname;

    try {
      if (req.method === "GET" && path === "/api/node/info") return json(res, 200, NODE_INFO);
      if (req.method === "GET" && path === "/api/v1/data/health") {
        return json(res, 200, { status: "ok", component: "spaceaware-data-api", time: new Date().toISOString() });
      }
      if (req.method === "GET" && path === "/api/v1/data/summary") return json(res, 200, DATA_SUMMARY);
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
        const body = JSON.parse((await readBody(req)).toString() || "{}");
        if (!body.schema && !body.schema_name) return json(res, 400, { error: "schema is required" });
        return json(res, 200, { ...QUERY_RESULT, schema: body.schema ?? body.schema_name });
      }
      if (req.method === "GET" && path === "/api/v1/data/epoch") {
        if (!url.searchParams.get("schema")) {
          return json(res, 400, { error: "missing required query parameter: schema" });
        }
        return json(res, 200, EPOCH_RESULT);
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
