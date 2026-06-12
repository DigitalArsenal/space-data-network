#!/usr/bin/env node
/**
 * Stdio smoke check: spawns dist/cli.js and speaks raw JSON-RPC
 * (newline-delimited, per the MCP stdio transport) to verify the server
 * initializes and lists its tools. Exits 0 on success.
 *
 * Run after `npm run build`:  node scripts/smoke.mjs
 */
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const __dirname = dirname(fileURLToPath(import.meta.url));
const cliPath = join(__dirname, "..", "dist", "cli.js");

const child = spawn(process.execPath, [cliPath], {
  stdio: ["pipe", "pipe", "pipe"],
  env: { ...process.env, SDN_API_URL: process.env.SDN_API_URL ?? "http://127.0.0.1:5001" },
});

const timeout = setTimeout(() => {
  console.error("SMOKE FAIL: timed out waiting for responses");
  child.kill();
  process.exit(1);
}, 10_000);

let buffer = "";
const responses = new Map();

child.stdout.on("data", (chunk) => {
  buffer += chunk.toString();
  let idx;
  while ((idx = buffer.indexOf("\n")) !== -1) {
    const line = buffer.slice(0, idx).trim();
    buffer = buffer.slice(idx + 1);
    if (!line) continue;
    const msg = JSON.parse(line);
    if (msg.id !== undefined) responses.set(msg.id, msg);
    checkDone();
  }
});

child.stderr.on("data", (chunk) => process.stderr.write(`[server] ${chunk}`));

function send(msg) {
  child.stdin.write(JSON.stringify(msg) + "\n");
}

send({
  jsonrpc: "2.0",
  id: 1,
  method: "initialize",
  params: {
    protocolVersion: "2025-03-26",
    capabilities: {},
    clientInfo: { name: "smoke-check", version: "0.0.1" },
  },
});

let listSent = false;

function checkDone() {
  if (responses.has(1) && !listSent) {
    listSent = true;
    const init = responses.get(1);
    if (!init.result?.serverInfo?.name) {
      fail("initialize response missing serverInfo");
    }
    console.error(`initialized: ${init.result.serverInfo.name} v${init.result.serverInfo.version}`);
    send({ jsonrpc: "2.0", method: "notifications/initialized" });
    send({ jsonrpc: "2.0", id: 2, method: "tools/list", params: {} });
  }
  if (responses.has(2)) {
    const list = responses.get(2);
    const tools = list.result?.tools ?? [];
    const names = tools.map((t) => t.name).sort();
    console.error(`tools: ${names.join(", ")}`);
    const expected = [
      "sdn_list_peers",
      "sdn_list_schemas",
      "sdn_node_status",
      "sdn_publish_record",
      "sdn_query_records",
      "sdn_recent_messages",
      "sdn_resolve_identity",
    ];
    const missing = expected.filter((n) => !names.includes(n));
    if (missing.length > 0) fail(`missing tools: ${missing.join(", ")}`);
    console.error("SMOKE OK");
    clearTimeout(timeout);
    child.kill();
    process.exit(0);
  }
}

function fail(reason) {
  console.error(`SMOKE FAIL: ${reason}`);
  clearTimeout(timeout);
  child.kill();
  process.exit(1);
}

child.on("exit", (code) => {
  if (!responses.has(2)) {
    console.error(`SMOKE FAIL: server exited early (code ${code})`);
    clearTimeout(timeout);
    process.exit(1);
  }
});
