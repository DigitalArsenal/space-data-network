#!/usr/bin/env node
/**
 * sdn-mcp — Model Context Protocol server for a Space Data Network node.
 *
 * Default transport is stdio (for Claude Desktop / Claude Code / any MCP host).
 * Pass --http [port] to serve the MCP Streamable HTTP transport instead.
 *
 * Environment:
 *   SDN_API_URL   base URL of the SDN node admin API (default http://127.0.0.1:5001)
 *   SDN_API_TOKEN wallet session token (sdn_wallet_session cookie value), needed
 *                 only for sdn_publish_record on auth-enabled nodes
 */
import { createServer as createHttpServer } from "node:http";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";

import { loadConfig, DEFAULT_SDN_API_URL } from "./config.js";
import { SdnClient } from "./client.js";
import { createServer, SERVER_NAME, SERVER_VERSION } from "./server.js";

interface CliOptions {
  http: boolean;
  port: number;
  apiUrl?: string;
  token?: string;
  help: boolean;
}

export function parseArgs(argv: string[]): CliOptions {
  const opts: CliOptions = { http: false, port: 3000, help: false };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    switch (arg) {
      case "--http": {
        opts.http = true;
        const next = argv[i + 1];
        if (next && /^\d+$/.test(next)) {
          opts.port = Number(next);
          i++;
        }
        break;
      }
      case "--port":
        opts.port = Number(argv[++i] ?? opts.port);
        break;
      case "--sdn-api-url":
        opts.apiUrl = argv[++i];
        break;
      case "--sdn-api-token":
        opts.token = argv[++i];
        break;
      case "--help":
      case "-h":
        opts.help = true;
        break;
      default:
        break;
    }
  }
  return opts;
}

const USAGE = `${SERVER_NAME} v${SERVER_VERSION}

Usage: sdn-mcp [options]

Options:
  --http [port]          Serve MCP over Streamable HTTP instead of stdio (default port 3000)
  --port <port>          Port for --http mode
  --sdn-api-url <url>    SDN node admin API base URL (overrides SDN_API_URL)
  --sdn-api-token <tok>  Wallet session token (overrides SDN_API_TOKEN)
  -h, --help             Show this help

Environment:
  SDN_API_URL    default ${DEFAULT_SDN_API_URL}
  SDN_API_TOKEN  sdn_wallet_session cookie value (needed for publishing on auth-enabled nodes)
`;

async function main(): Promise<void> {
  const opts = parseArgs(process.argv.slice(2));
  if (opts.help) {
    process.stdout.write(USAGE);
    return;
  }

  const config = loadConfig();
  if (opts.apiUrl) config.baseUrl = opts.apiUrl.replace(/\/+$/, "");
  if (opts.token) config.token = opts.token;

  if (!opts.http) {
    const client = new SdnClient(config);
    const server = createServer(client);
    await server.connect(new StdioServerTransport());
    // Keep logs off stdout — it carries the JSON-RPC stream.
    console.error(`${SERVER_NAME} v${SERVER_VERSION} on stdio (SDN node: ${config.baseUrl})`);
    return;
  }

  // Stateless Streamable HTTP: a fresh server+transport per request.
  const httpServer = createHttpServer(async (req, res) => {
    if (req.url === "/healthz") {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ status: "ok", server: SERVER_NAME, version: SERVER_VERSION }));
      return;
    }
    try {
      const client = new SdnClient(config);
      const server = createServer(client);
      const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
      res.on("close", () => {
        void transport.close();
        void server.close();
      });
      await server.connect(transport);
      await transport.handleRequest(req, res);
    } catch (err) {
      console.error("request handling error:", err);
      if (!res.headersSent) {
        res.writeHead(500, { "Content-Type": "application/json" });
        res.end(
          JSON.stringify({
            jsonrpc: "2.0",
            error: { code: -32603, message: "Internal server error" },
            id: null,
          })
        );
      }
    }
  });

  httpServer.listen(opts.port, () => {
    console.error(
      `${SERVER_NAME} v${SERVER_VERSION} on http://127.0.0.1:${opts.port} (SDN node: ${config.baseUrl})`
    );
  });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
