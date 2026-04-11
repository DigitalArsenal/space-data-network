#!/usr/bin/env node

import crypto from "node:crypto";
import http from "node:http";
import {
  decodeThirdPartyClientLicenseRequest,
  decodeThirdPartyServerPluginRegistration,
  encodeThirdPartyClientLicenseResponse,
  encodeThirdPartyServerPluginGrant,
} from "../src/third-party-codec.js";

const DEFAULT_HOST = "127.0.0.1";
const DEFAULT_PORT = 8899;
const MAX_BODY_BYTES = 64 * 1024;

function parseArgs(argv) {
  const out = {
    host: DEFAULT_HOST,
    port: DEFAULT_PORT,
  };

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const next = argv[i + 1];

    if (arg === "--host" && next) {
      out.host = next;
      i++;
      continue;
    }
    if (arg === "--port" && next) {
      const parsed = Number(next);
      if (Number.isInteger(parsed) && parsed > 0 && parsed <= 65535) {
        out.port = parsed;
      }
      i++;
      continue;
    }
    if (arg === "--help" || arg === "-h") {
      printUsage();
      process.exit(0);
    }
  }

  return out;
}

function printUsage() {
  console.log(`Usage:\n  node scripts/mock-third-party-broker.mjs [--host 127.0.0.1] [--port 8899]`);
}

function stableBytes(seedText, length) {
  const out = new Uint8Array(length);
  let offset = 0;
  let round = 0;
  while (offset < length) {
    const digest = crypto
      .createHash("sha256")
      .update(seedText)
      .update(String(round))
      .digest();
    const remaining = length - offset;
    const take = Math.min(remaining, digest.length);
    out.set(digest.subarray(0, take), offset);
    offset += take;
    round += 1;
  }
  return out;
}

function readRequestBody(req, maxBodyBytes) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let total = 0;

    req.on("data", (chunk) => {
      total += chunk.length;
      if (total > maxBodyBytes) {
        reject(new Error("payload too large"));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });

    req.on("end", () => {
      resolve(Buffer.concat(chunks));
    });

    req.on("error", reject);
  });
}

function writeJson(res, statusCode, payload) {
  res.statusCode = statusCode;
  res.setHeader("content-type", "application/json; charset=utf-8");
  res.end(`${JSON.stringify(payload)}\n`);
}

function writeBinary(res, statusCode, bytes) {
  res.statusCode = statusCode;
  res.setHeader("content-type", "application/octet-stream");
  res.end(Buffer.from(bytes));
}

async function handleClientLicense(req, res) {
  const body = await readRequestBody(req, MAX_BODY_BYTES);
  const decoded = decodeThirdPartyClientLicenseRequest(body);

  const response = encodeThirdPartyClientLicenseResponse({
    status: 0,
    keyVersion: 1,
    expiresAtMs: Date.now() + 60 * 60 * 1000,
    wrappedKey: stableBytes(
      `${decoded.pluginId}:${decoded.pluginVersion}:${Buffer.from(decoded.accountIdHash).toString("hex")}`,
      32,
    ),
    challengeId: stableBytes(decoded.challengeToken || "no-challenge", 16),
  });

  writeBinary(res, 200, response);
}

async function handleServerRegistration(req, res) {
  const body = await readRequestBody(req, MAX_BODY_BYTES);
  const decoded = decodeThirdPartyServerPluginRegistration(body);

  const now = Date.now();
  const response = encodeThirdPartyServerPluginGrant({
    status: 0,
    grantId: `grant-${decoded.pluginId}-${now}`,
    issuedAtMs: now,
    expiresAtMs: now + 24 * 60 * 60 * 1000,
    allowedAccounts: ["acct-demo-001", "acct-demo-002"],
    policyHash: stableBytes(
      `${decoded.pluginId}:${decoded.pluginVersion}:${decoded.vendorId}`,
      32,
    ),
  });

  writeBinary(res, 200, response);
}

async function main() {
  const args = parseArgs(process.argv.slice(2));

  const server = http.createServer(async (req, res) => {
    try {
      if (req.method === "GET" && req.url === "/healthz") {
        writeJson(res, 200, { ok: true, service: "mock-third-party-broker" });
        return;
      }

      if (req.method === "POST" && req.url === "/v1/third-party/client-license") {
        await handleClientLicense(req, res);
        return;
      }

      if (req.method === "POST" && req.url === "/v1/third-party/server-registration") {
        await handleServerRegistration(req, res);
        return;
      }

      writeJson(res, 404, { error: "not found" });
    } catch (error) {
      writeJson(res, 400, {
        error: error instanceof Error ? error.message : String(error),
      });
    }
  });

  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(args.port, args.host, resolve);
  });

  console.log(
    JSON.stringify(
      {
        ok: true,
        host: args.host,
        port: args.port,
        endpoints: {
          healthz: `http://${args.host}:${args.port}/healthz`,
          clientLicense: `http://${args.host}:${args.port}/v1/third-party/client-license`,
          serverRegistration: `http://${args.host}:${args.port}/v1/third-party/server-registration`,
        },
      },
      null,
      2,
    ),
  );
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
