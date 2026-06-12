# @spacedatanetwork/sdn-mcp-server

A [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server that exposes a
Space Data Network (SDN) node's data fabric to AI agents. Any MCP-capable host —
Claude Desktop, Claude Code, or a custom autonomous agent — can use it to inspect a
node's identity and health, discover SDS schemas, query stored FlatBuffer records,
read the publication log, resolve provider identities, and publish validated records.

It speaks to the node's local admin/API server (the same HTTP surface used by the SDN
admin UI), so it works against any running `spacedatanetwork` daemon with zero extra
node-side configuration for read access.

## Why

Standard-protocol access for autonomous agents (e.g. DIU PROJ00677 "Hydra" explicitly
requests "standard protocol servers such as Model Context Protocol (MCP)") means an AI
agent can participate in the data fabric directly: discover what data a node serves,
pull decoded records with provenance, watch the publication log for new announcements,
and contribute validated SDS records back — all through typed, self-describing tools.

## Install & build

```bash
cd packages/sdn-mcp-server
npm install
npm run build     # emits dist/, including the sdn-mcp CLI (dist/cli.js)
npm test          # offline test suite against a mocked SDN API
npm run smoke     # stdio JSON-RPC initialize + tools/list smoke check
```

Requires Node 18+.

## Running

Default transport is **stdio** (for MCP hosts). Point it at a node with `SDN_API_URL`:

```bash
SDN_API_URL=http://127.0.0.1:5001 node dist/cli.js
```

Streamable HTTP transport (for remote/networked agents):

```bash
node dist/cli.js --http 3000     # serves MCP at http://127.0.0.1:3000/, health at /healthz
```

### Claude Desktop / Claude Code configuration

```json
{
  "mcpServers": {
    "sdn": {
      "command": "node",
      "args": ["<path-to-repo>/packages/sdn-mcp-server/dist/cli.js"],
      "env": {
        "SDN_API_URL": "http://127.0.0.1:5001"
      }
    }
  }
}
```

For Claude Code: `claude mcp add sdn -- node <path>/packages/sdn-mcp-server/dist/cli.js`

## Environment variables

| Variable        | Default                 | Purpose |
|-----------------|-------------------------|---------|
| `SDN_API_URL`   | `http://127.0.0.1:5001` | Base URL of the SDN node's admin/API server (the daemon's default admin listen address). |
| `SDN_API_TOKEN` | _unset_                 | Wallet session token — the value of the `sdn_wallet_session` cookie issued by the node's Ed25519 challenge/response login (`/api/auth/challenge` → `/api/auth/verify`). Only needed for `sdn_publish_record` on nodes with `admin.require_auth: true`; all read tools work unauthenticated. |

CLI flags `--sdn-api-url` and `--sdn-api-token` override the environment.

## Tools

| Tool | What it does | SDN endpoint(s) |
|------|--------------|-----------------|
| `sdn_node_status` | Node identity (peer ID, xpub), version, listen addresses, data-API health, observed-peer count | `GET /api/node/info`, `GET /api/v1/data/health`, `GET /api/peers/sdn` |
| `sdn_list_peers` | Trust-registry peers (name, org, trust level, connection stats); optionally the live observed-peer snapshot | `GET /api/peers`, `GET /api/peers/sdn` |
| `sdn_list_schemas` | SDS schemas served by the node with record counts, byte totals, epoch coverage, capabilities, and rate limits | `GET /api/v1/catalog` (falls back to `GET /api/v1/data/summary`) |
| `sdn_query_records` | Query stored records by schema with provenance filters (`provider_id`, `source_name`, `batch_id`, `producer_peer_id`, `peer_id`), pagination, and optional base64 record payloads. Time filters (`from`/`to`/`at` RFC3339, `day`, `norad_cat_id`, `entity_id`) automatically use the epoch index. | `POST /api/v1/data/query`, `GET /api/v1/data/epoch` |
| `sdn_publish_record` | Publish a single SDS record (base64-encoded FlatBuffer). The node validates it server-side against the schema before storing and returns the content CID. Requires `SDN_API_TOKEN` on auth-enabled nodes. | `POST /api/v1/data/publish/{schema}` |
| `sdn_recent_messages` | Recent network messages for a schema from the publication log: log heads per publisher (no `publisher` arg) or entries since a sequence number (with `publisher`) | `GET /api/v1/log/{schema}/heads`, `GET /api/v1/log/{schema}/entries` |
| `sdn_resolve_identity` | Resolve a peer ID / xpub / name / org / domain to identity info via the directory, the trust registry, and the node's own EPM identity | `GET /api/directory/nodes`, `GET /api/directory/users`, `GET /api/peers/{peerID}`, `GET /api/node/info` |

Schema names are SpaceDataStandards.org FlatBuffer file names, e.g. `OMM.fbs` (orbit
mean-elements), `CAT.fbs` (satellite catalog), `EPM.fbs` (entity profiles).

### A note on publishing

The SDN publish endpoint accepts the **binary FlatBuffer encoding** of a record (it is
validated server-side against the schema), so `sdn_publish_record` takes `data_base64` —
the base64 of those bytes — rather than free-form JSON. Use the SDS libraries
(spacedatastandards.org) or `sdn-js` to build the FlatBuffer for a schema.

## Resources

| URI | Contents |
|-----|----------|
| `sds://schemas` | The full schema catalog as JSON |
| `sds://schemas/{name}` | Catalog entry for one schema (record count, bytes, epoch range); each schema is also listed as a discoverable resource |

## Errors

Every tool returns structured MCP errors. If the daemon is unreachable you get an
actionable message:

> Cannot reach the SDN node at http://127.0.0.1:5001 (…). Is the SDN daemon running?
> Start it with: `spacedatanetwork daemon` — or point SDN_API_URL at a running node's
> admin API.

Publishing without a valid session on an auth-enabled node explains the wallet
challenge/response login flow and the `SDN_API_TOKEN` requirement.

## Development

- `src/client.ts` — typed HTTP client for the node admin API (endpoint paths mirror the
  Go handlers in `sdn-server/internal/api`, `internal/peers`, `internal/directory`).
- `src/server.ts` — MCP server definition (tools + resources).
- `src/cli.ts` — stdio / Streamable HTTP entry point.
- `test/` — vitest suite with an in-process mock of the SDN admin API; covers every
  tool's happy path, auth behavior, and the daemon-down error path. Runs fully offline.
