import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import type { CallToolResult, ReadResourceResult } from "@modelcontextprotocol/sdk/types.js";

import { SdnClient } from "../src/client.js";
import { createServer } from "../src/server.js";
import { startMockSdn, NODE_INFO, REGISTRY_PEERS, type MockSdn } from "./mock-sdn.js";

let mock: MockSdn;
let client: Client;

async function connect(config: { baseUrl: string; token?: string }): Promise<Client> {
  const server = createServer(new SdnClient(config));
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  const c = new Client({ name: "test-client", version: "0.0.1" });
  await Promise.all([server.connect(serverTransport), c.connect(clientTransport)]);
  return c;
}

function textOf(result: CallToolResult): string {
  const block = result.content?.[0];
  if (!block || block.type !== "text") throw new Error("expected a text content block");
  return block.text;
}

function parsed(result: CallToolResult): any {
  return JSON.parse(textOf(result));
}

beforeAll(async () => {
  mock = await startMockSdn();
  client = await connect({ baseUrl: mock.url });
});

afterAll(async () => {
  await client.close();
  await mock.close();
});

describe("tool registration", () => {
  it("lists all seven SDN tools", async () => {
    const { tools } = await client.listTools();
    const names = tools.map((t) => t.name).sort();
    expect(names).toEqual([
      "sdn_list_peers",
      "sdn_list_schemas",
      "sdn_node_status",
      "sdn_publish_record",
      "sdn_query_records",
      "sdn_recent_messages",
      "sdn_resolve_identity",
    ]);
  });
});

describe("sdn_node_status", () => {
  it("returns identity, health, and observed peer count", async () => {
    const result = (await client.callTool({ name: "sdn_node_status", arguments: {} })) as CallToolResult;
    expect(result.isError).toBeFalsy();
    const payload = parsed(result);
    expect(payload.node.peer_id).toBe(NODE_INFO.peer_id);
    expect(payload.node.xpub).toBe(NODE_INFO.xpub);
    expect(payload.node.version).toBe(NODE_INFO.version);
    expect(payload.health.status).toBe("ok");
    expect(payload.observed_peer_count).toBe(2);
  });
});

describe("sdn_list_peers", () => {
  it("returns registry peers", async () => {
    const result = (await client.callTool({ name: "sdn_list_peers", arguments: {} })) as CallToolResult;
    expect(result.isError).toBeFalsy();
    const payload = parsed(result);
    expect(payload.registry_peers).toHaveLength(1);
    expect(payload.registry_peers[0].id).toBe(REGISTRY_PEERS[0].id);
    expect(payload.observed_peers).toBeUndefined();
  });

  it("includes observed peers on request", async () => {
    const result = (await client.callTool({
      name: "sdn_list_peers",
      arguments: { include_observed: true },
    })) as CallToolResult;
    const payload = parsed(result);
    expect(payload.observed_peers.peers).toHaveLength(2);
  });
});

describe("sdn_list_schemas", () => {
  it("returns the catalog with schemas and rate limits", async () => {
    const result = (await client.callTool({ name: "sdn_list_schemas", arguments: {} })) as CallToolResult;
    expect(result.isError).toBeFalsy();
    const payload = parsed(result);
    expect(payload.schemas.map((s: any) => s.name)).toEqual(["OMM.fbs", "CAT.fbs"]);
    expect(payload.capabilities).toContain("data_query");
    expect(payload.rate_limits.publish_per_minute).toBe(30);
  });

  it("falls back to the data summary when the catalog is unavailable", async () => {
    mock.state.catalogAvailable = false;
    try {
      const result = (await client.callTool({ name: "sdn_list_schemas", arguments: {} })) as CallToolResult;
      expect(result.isError).toBeFalsy();
      const payload = parsed(result);
      expect(payload.total_records).toBe(41305);
      expect(payload.schemas).toHaveLength(2);
    } finally {
      mock.state.catalogAvailable = true;
    }
  });
});

describe("sdn_query_records", () => {
  it("queries raw records by schema and filters", async () => {
    const result = (await client.callTool({
      name: "sdn_query_records",
      arguments: { schema: "OMM.fbs", provider_id: "celestrak", limit: 5 },
    })) as CallToolResult;
    expect(result.isError).toBeFalsy();
    const payload = parsed(result);
    expect(payload.schema).toBe("OMM.fbs");
    expect(payload.results[0].cid).toBe("bafkreimockcid");
  });

  it("routes time-range filters to the epoch index", async () => {
    const result = (await client.callTool({
      name: "sdn_query_records",
      arguments: { schema: "OMM.fbs", from: "2026-06-01T00:00:00Z", to: "2026-06-10T00:00:00Z" },
    })) as CallToolResult;
    expect(result.isError).toBeFalsy();
    const payload = parsed(result);
    expect(payload.profile).toBe("day");
    expect(payload.results[0].cid).toBe("bafkreimockepochcid");
  });

  it("surfaces server validation errors", async () => {
    const result = (await client.callTool({
      name: "sdn_query_records",
      arguments: { schema: "" },
    })) as CallToolResult;
    expect(result.isError).toBe(true);
    expect(textOf(result)).toContain("400");
  });
});

describe("sdn_publish_record", () => {
  const record = Buffer.from("mock-flatbuffer-record");

  it("publishes a base64 FlatBuffer record and returns the CID", async () => {
    const result = (await client.callTool({
      name: "sdn_publish_record",
      arguments: { schema: "OMM.fbs", data_base64: record.toString("base64") },
    })) as CallToolResult;
    expect(result.isError).toBeFalsy();
    const payload = parsed(result);
    expect(payload.cid).toBe("bafkreipublishedcid");
    expect(payload.schema).toBe("OMM.fbs");
    expect(payload.bytes).toBe(record.length);
    expect(mock.state.publishes.at(-1)?.bytes.equals(record)).toBe(true);
  });

  it("rejects an empty payload before hitting the network", async () => {
    const result = (await client.callTool({
      name: "sdn_publish_record",
      arguments: { schema: "OMM.fbs", data_base64: "" },
    })) as CallToolResult;
    expect(result.isError).toBe(true);
    expect(textOf(result)).toContain("empty");
  });

  it("explains how to authenticate when the node requires auth", async () => {
    mock.state.requireAuthForPublish = true;
    try {
      const result = (await client.callTool({
        name: "sdn_publish_record",
        arguments: { schema: "OMM.fbs", data_base64: record.toString("base64") },
      })) as CallToolResult;
      expect(result.isError).toBe(true);
      expect(textOf(result)).toContain("SDN_API_TOKEN");
    } finally {
      mock.state.requireAuthForPublish = false;
    }
  });

  it("sends the session cookie when SDN_API_TOKEN is configured", async () => {
    mock.state.requireAuthForPublish = true;
    const authed = await connect({ baseUrl: mock.url, token: "secret-session-token" });
    try {
      const result = (await authed.callTool({
        name: "sdn_publish_record",
        arguments: { schema: "OMM.fbs", data_base64: record.toString("base64") },
      })) as CallToolResult;
      expect(result.isError).toBeFalsy();
      expect(mock.state.publishes.at(-1)?.cookie).toContain("sdn_wallet_session=secret-session-token");
    } finally {
      mock.state.requireAuthForPublish = false;
      await authed.close();
    }
  });
});

describe("sdn_recent_messages", () => {
  it("returns publication-log heads when no publisher is given", async () => {
    const result = (await client.callTool({
      name: "sdn_recent_messages",
      arguments: { schema: "OMM.fbs" },
    })) as CallToolResult;
    expect(result.isError).toBeFalsy();
    const payload = parsed(result);
    expect(payload.heads[0].publisher_peer_id).toBe(REGISTRY_PEERS[0].id);
    expect(payload.heads[0].head_sequence).toBe(1042);
  });

  it("returns log entries for a publisher", async () => {
    const result = (await client.callTool({
      name: "sdn_recent_messages",
      arguments: { schema: "OMM.fbs", publisher: REGISTRY_PEERS[0].id, since: 1040, limit: 10 },
    })) as CallToolResult;
    expect(result.isError).toBeFalsy();
    const payload = parsed(result);
    expect(payload.count).toBe(2);
    expect(payload.entries).toHaveLength(2);
  });
});

describe("sdn_resolve_identity", () => {
  it("resolves a peer ID via registry and directory", async () => {
    const result = (await client.callTool({
      name: "sdn_resolve_identity",
      arguments: { identifier: REGISTRY_PEERS[0].id },
    })) as CallToolResult;
    expect(result.isError).toBeFalsy();
    const payload = parsed(result);
    expect(payload.registry_peer.name).toBe("Example Observatory");
    expect(payload.directory_nodes.results[0].organization).toBe("ExampleOrg");
    expect(payload.self).toBeUndefined();
  });

  it("recognizes the local node's own identity", async () => {
    const result = (await client.callTool({
      name: "sdn_resolve_identity",
      arguments: { identifier: NODE_INFO.peer_id },
    })) as CallToolResult;
    const payload = parsed(result);
    expect(payload.self.xpub).toBe(NODE_INFO.xpub);
  });
});

describe("resources", () => {
  it("exposes the schema catalog at sds://schemas", async () => {
    const result: ReadResourceResult = await client.readResource({ uri: "sds://schemas" });
    const schemas = JSON.parse(result.contents[0].text as string);
    expect(schemas.map((s: any) => s.name)).toEqual(["OMM.fbs", "CAT.fbs"]);
  });

  it("exposes individual schemas at sds://schemas/{name}", async () => {
    const result: ReadResourceResult = await client.readResource({ uri: "sds://schemas/OMM.fbs" });
    const schema = JSON.parse(result.contents[0].text as string);
    expect(schema.record_count).toBe(12894);
  });

  it("lists schema resources", async () => {
    const { resources } = await client.listResources();
    const uris = resources.map((r) => r.uri);
    expect(uris).toContain("sds://schemas");
    expect(uris).toContain("sds://schemas/OMM.fbs");
    expect(uris).toContain("sds://schemas/CAT.fbs");
  });
});

describe("daemon down", () => {
  it("every tool returns an actionable error when the SDN node is unreachable", async () => {
    // Reserve a port, then close it so nothing is listening.
    const dead = await startMockSdn();
    await dead.close();
    const offline = await connect({ baseUrl: dead.url });
    try {
      const calls: Array<{ name: string; arguments: Record<string, unknown> }> = [
        { name: "sdn_node_status", arguments: {} },
        { name: "sdn_list_peers", arguments: {} },
        { name: "sdn_list_schemas", arguments: {} },
        { name: "sdn_query_records", arguments: { schema: "OMM.fbs" } },
        { name: "sdn_publish_record", arguments: { schema: "OMM.fbs", data_base64: "aGk=" } },
        { name: "sdn_recent_messages", arguments: { schema: "OMM.fbs" } },
        { name: "sdn_resolve_identity", arguments: { identifier: "anything" } },
      ];
      for (const call of calls) {
        const result = (await offline.callTool(call)) as CallToolResult;
        expect(result.isError, `${call.name} should fail`).toBe(true);
        const text = textOf(result);
        expect(text, `${call.name} message`).toContain("Is the SDN daemon running?");
        expect(text, `${call.name} message`).toContain("spacedatanetwork daemon");
      }
    } finally {
      await offline.close();
    }
  });
});
