/**
 * MCP server definition for the Space Data Network.
 *
 * Exposes an SDN node's data fabric (SDS schemas, stored records, the
 * publication log, peer registry, and EPM identity) as MCP tools and
 * resources so autonomous agents can query and publish space data.
 */
import { McpServer, ResourceTemplate } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";

import { SdnClient, SdnConnectionError, SdnHttpError } from "./client.js";

export const SERVER_NAME = "sdn-mcp-server";
export const SERVER_VERSION = "0.1.0";

function ok(payload: unknown): CallToolResult {
  return { content: [{ type: "text", text: JSON.stringify(payload, null, 2) }] };
}

function fail(err: unknown): CallToolResult {
  let message: string;
  if (err instanceof SdnConnectionError || err instanceof SdnHttpError) {
    message = err.message;
  } else if (err instanceof Error) {
    message = err.message;
  } else {
    message = String(err);
  }
  return { isError: true, content: [{ type: "text", text: message }] };
}

async function run(fn: () => Promise<unknown>): Promise<CallToolResult> {
  try {
    return ok(await fn());
  } catch (err) {
    return fail(err);
  }
}

/** Best-effort call: returns the value, or an `{ error }` marker on failure. */
async function tryCall<T>(fn: () => Promise<T>): Promise<T | { error: string }> {
  try {
    return await fn();
  } catch (err) {
    if (err instanceof SdnConnectionError) throw err; // daemon down — surface to the caller
    return { error: err instanceof Error ? err.message : String(err) };
  }
}

export function createServer(client: SdnClient): McpServer {
  const server = new McpServer(
    { name: SERVER_NAME, version: SERVER_VERSION },
    {
      instructions:
        "Tools for interacting with a Space Data Network (SDN) node: inspect node identity " +
        "and health, list peers and SDS schemas, query stored FlatBuffer records, read the " +
        "publication log, resolve provider identities, and publish validated SDS records. " +
        "Schema names are SpaceDataStandards.org file names like OMM.fbs, CAT.fbs, EPM.fbs.",
    }
  );

  // ---- tools ---------------------------------------------------------------

  server.registerTool(
    "sdn_node_status",
    {
      title: "SDN node status",
      description:
        "Get the local SDN node's identity and health: peer ID, xpub, version, listen " +
        "addresses, data-API health, and observed-peer count. Use this first to confirm " +
        "the daemon is reachable.",
      inputSchema: {},
    },
    async () =>
      run(async () => {
        const info = await client.nodeInfo();
        const health = await tryCall(() => client.dataHealth());
        const observed = await tryCall(() => client.observedPeers());
        let observedPeerCount: number | undefined;
        if (Array.isArray(observed)) observedPeerCount = observed.length;
        else if (observed && typeof observed === "object") {
          const o = observed as Record<string, unknown>;
          if (Array.isArray(o.peers)) observedPeerCount = o.peers.length;
          else if (typeof o.count === "number") observedPeerCount = o.count;
        }
        return { node: info, health, observed_peer_count: observedPeerCount };
      })
  );

  server.registerTool(
    "sdn_list_peers",
    {
      title: "List SDN peers",
      description:
        "List peers known to this SDN node. Returns the trust-registry peers (name, " +
        "organization, trust level, connection stats). Set include_observed to also " +
        "return the snapshot of peers currently observed on the network.",
      inputSchema: {
        include_observed: z
          .boolean()
          .optional()
          .describe("Also include the live observed-peer snapshot from /api/peers/sdn"),
      },
    },
    async ({ include_observed }) =>
      run(async () => {
        const registry = await client.listPeers();
        if (!include_observed) return { registry_peers: registry };
        const observed = await tryCall(() => client.observedPeers());
        return { registry_peers: registry, observed_peers: observed };
      })
  );

  server.registerTool(
    "sdn_list_schemas",
    {
      title: "List SDS schemas",
      description:
        "List the SpaceDataStandards (SDS) schemas this node serves, with record counts, " +
        "byte totals, epoch coverage, node capabilities, and rate limits. Schema names " +
        "(e.g. OMM.fbs for orbit mean-elements, CAT.fbs for the satellite catalog, " +
        "EPM.fbs for entity profiles) are used by the query/publish tools.",
      inputSchema: {},
    },
    async () =>
      run(async () => {
        try {
          return await client.catalog();
        } catch (err) {
          // The catalog handler is only mounted when publishing is configured;
          // fall back to the always-available data summary.
          if (err instanceof SdnHttpError && err.status === 404) {
            return await client.dataSummary();
          }
          throw err;
        }
      })
  );

  server.registerTool(
    "sdn_query_records",
    {
      title: "Query stored SDS records",
      description:
        "Query records stored on this SDN node for a schema. Supports provenance filters " +
        "(provider_id, source_name, batch_id, producer_peer_id, peer_id) plus pagination " +
        "(limit/offset). Providing any time filter (from/to as RFC3339, day as YYYY-MM-DD, " +
        "at, norad_cat_id, entity_id) switches to the epoch index for time-range queries. " +
        "Set include_data to true to get each record's raw FlatBuffer payload base64-encoded.",
      inputSchema: {
        schema: z.string().describe("SDS schema name, e.g. OMM.fbs"),
        provider_id: z.string().optional().describe("Filter by provider identifier"),
        source_name: z.string().optional().describe("Filter by source name"),
        batch_id: z.string().optional().describe("Filter by batch identifier"),
        producer_peer_id: z.string().optional().describe("Filter by producing peer ID"),
        peer_id: z.string().optional().describe("Filter by storing peer ID"),
        limit: z.number().int().positive().optional().describe("Max records (server default 100)"),
        offset: z.number().int().nonnegative().optional().describe("Pagination offset"),
        include_data: z
          .boolean()
          .optional()
          .describe("Include base64-encoded FlatBuffer record bytes"),
        from: z.string().optional().describe("Epoch range start, RFC3339 (switches to epoch query)"),
        to: z.string().optional().describe("Epoch range end, RFC3339 (switches to epoch query)"),
        at: z.string().optional().describe("Point-in-time epoch lookup, RFC3339"),
        day: z.string().optional().describe("Epoch day bucket, YYYY-MM-DD"),
        norad_cat_id: z.number().int().optional().describe("Filter by NORAD catalog ID (epoch query)"),
        entity_id: z.string().optional().describe("Filter by entity ID (epoch query)"),
      },
    },
    async (args) =>
      run(async () => {
        const usesEpochIndex =
          args.from !== undefined ||
          args.to !== undefined ||
          args.at !== undefined ||
          args.day !== undefined ||
          args.norad_cat_id !== undefined ||
          args.entity_id !== undefined;
        if (usesEpochIndex) {
          return await client.epochQuery({
            schema: args.schema,
            from: args.from,
            to: args.to,
            at: args.at,
            day: args.day,
            norad_cat_id: args.norad_cat_id,
            entity_id: args.entity_id,
            provider_id: args.provider_id,
            source_name: args.source_name,
            batch_id: args.batch_id,
            limit: args.limit,
            include_data: args.include_data,
          });
        }
        return await client.queryRecords({
          schema: args.schema,
          provider_id: args.provider_id,
          source_name: args.source_name,
          batch_id: args.batch_id,
          producer_peer_id: args.producer_peer_id,
          peer_id: args.peer_id,
          limit: args.limit,
          offset: args.offset,
          include_data: args.include_data,
        });
      })
  );

  server.registerTool(
    "sdn_publish_record",
    {
      title: "Publish an SDS record",
      description:
        "Publish a single SDS record to this node. The record must be the binary FlatBuffer " +
        "encoding of the named schema, base64-encoded; the node validates it server-side " +
        "against the schema before storing and returns the content CID. Requires an " +
        "authenticated session: set SDN_API_TOKEN to a wallet session token on nodes with " +
        "admin.require_auth enabled.",
      inputSchema: {
        schema: z.string().describe("SDS schema name the record conforms to, e.g. OMM.fbs"),
        data_base64: z
          .string()
          .describe("Base64-encoded FlatBuffer bytes of the record"),
      },
    },
    async ({ schema, data_base64 }) =>
      run(async () => {
        let bytes: Uint8Array;
        try {
          bytes = Uint8Array.from(Buffer.from(data_base64, "base64"));
        } catch {
          throw new Error("data_base64 is not valid base64");
        }
        if (bytes.length === 0) throw new Error("data_base64 decoded to an empty payload");
        try {
          return await client.publishRecord(schema, bytes);
        } catch (err) {
          if (err instanceof SdnHttpError && (err.status === 401 || err.status === 403)) {
            throw new Error(
              `${err.message}\nPublishing requires an authenticated wallet session. ` +
                `Log in to the node (wallet challenge/response at /api/auth/challenge + ` +
                `/api/auth/verify) and set SDN_API_TOKEN to the sdn_wallet_session cookie value.`
            );
          }
          throw err;
        }
      })
  );

  server.registerTool(
    "sdn_recent_messages",
    {
      title: "Recent publication-log messages",
      description:
        "Read recent network messages for a schema from the node's publication log (the " +
        "durable record of pubsub announcements). Without a publisher, returns the log " +
        "heads for every publisher of the schema (head sequence, record counts, epoch " +
        "range) — use those peer IDs as `publisher` to fetch the actual entries since a " +
        "sequence number.",
      inputSchema: {
        schema: z.string().describe("SDS schema name, e.g. OMM.fbs"),
        publisher: z
          .string()
          .optional()
          .describe("Publisher peer ID; omit to list heads for all publishers"),
        since: z
          .number()
          .int()
          .nonnegative()
          .optional()
          .describe("Return entries after this sequence number (default 0)"),
        limit: z.number().int().positive().optional().describe("Max entries (server max 1000)"),
      },
    },
    async ({ schema, publisher, since, limit }) =>
      run(async () => {
        if (!publisher) return await client.logHeads(schema);
        return await client.logEntries(schema, publisher, since, limit);
      })
  );

  server.registerTool(
    "sdn_resolve_identity",
    {
      title: "Resolve a provider identity",
      description:
        "Resolve a provider identifier (peer ID, xpub, name, organization, or domain) to " +
        "identity information. Searches the node's directory (nodes and users), checks the " +
        "trust registry for an exact peer-ID match, and reports if the identifier is this " +
        "node itself (EPM identity).",
      inputSchema: {
        identifier: z.string().describe("Peer ID, xpub, name, organization, or domain to resolve"),
        limit: z.number().int().positive().optional().describe("Max directory matches (default 10)"),
      },
    },
    async ({ identifier, limit }) =>
      run(async () => {
        const max = limit ?? 10;
        const [nodes, users, self] = await Promise.all([
          tryCall(() => client.directoryNodes(identifier, max)),
          tryCall(() => client.directoryUsers(identifier, max)),
          tryCall(() => client.nodeInfo()),
        ]);

        let selfMatch: Record<string, unknown> | undefined;
        if (self && typeof self === "object" && !("error" in (self as object))) {
          const s = self as Record<string, unknown>;
          if (s.peer_id === identifier || s.xpub === identifier) selfMatch = s;
        }

        const registryPeer = await tryCall(() => client.getPeer(identifier));
        const registryMatch =
          registryPeer && typeof registryPeer === "object" && !("error" in registryPeer)
            ? registryPeer
            : undefined;

        return {
          identifier,
          self: selfMatch,
          registry_peer: registryMatch,
          directory_nodes: nodes,
          directory_users: users,
        };
      })
  );

  // ---- resources -----------------------------------------------------------

  async function schemaList(): Promise<Array<Record<string, unknown>>> {
    try {
      const cat = (await client.catalog()) as { schemas?: Array<Record<string, unknown>> };
      return cat.schemas ?? [];
    } catch {
      const summary = (await client.dataSummary()) as { schemas?: Array<Record<string, unknown>> };
      return summary.schemas ?? [];
    }
  }

  server.registerResource(
    "sds-schemas",
    "sds://schemas",
    {
      title: "SDS schema catalog",
      description: "All SDS schemas served by this SDN node, with record counts and coverage",
      mimeType: "application/json",
    },
    async (uri) => ({
      contents: [
        {
          uri: uri.href,
          mimeType: "application/json",
          text: JSON.stringify(await schemaList(), null, 2),
        },
      ],
    })
  );

  server.registerResource(
    "sds-schema",
    new ResourceTemplate("sds://schemas/{name}", {
      list: async () => {
        const schemas = await schemaList();
        return {
          resources: schemas
            .filter((s) => typeof s.name === "string")
            .map((s) => ({
              uri: `sds://schemas/${s.name as string}`,
              name: s.name as string,
              mimeType: "application/json",
            })),
        };
      },
    }),
    {
      title: "SDS schema details",
      description: "Catalog entry for a single SDS schema (record count, bytes, epoch range)",
      mimeType: "application/json",
    },
    async (uri, variables) => {
      const name = String(variables.name);
      const schemas = await schemaList();
      const match = schemas.find((s) => s.name === name);
      return {
        contents: [
          {
            uri: uri.href,
            mimeType: "application/json",
            text: JSON.stringify(match ?? { error: `schema not found: ${name}` }, null, 2),
          },
        ],
      };
    }
  );

  return server;
}
