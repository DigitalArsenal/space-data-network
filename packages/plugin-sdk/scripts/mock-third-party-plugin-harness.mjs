#!/usr/bin/env node

import {
  encodeThirdPartyClientLicenseRequest,
  decodeThirdPartyClientLicenseResponse,
  encodeThirdPartyServerPluginRegistration,
  decodeThirdPartyServerPluginGrant,
} from "../src/third-party-codec.js";

const DEFAULT_BASE_URL = "http://127.0.0.1:8899";

function parseArgs(argv) {
  const out = {
    baseUrl: DEFAULT_BASE_URL,
  };

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const next = argv[i + 1];

    if (arg === "--base-url" && next) {
      out.baseUrl = next;
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
  console.log(`Usage:\n  node scripts/mock-third-party-plugin-harness.mjs [--base-url http://127.0.0.1:8899]`);
}

function hexToBytes(hex) {
  const normalized = String(hex || "")
    .trim()
    .replace(/^0x/i, "")
    .toLowerCase();
  if (!/^[0-9a-f]+$/.test(normalized) || normalized.length % 2 !== 0) {
    throw new Error(`invalid hex value: ${hex}`);
  }

  const out = new Uint8Array(normalized.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = Number.parseInt(normalized.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

async function postBinary(url, bodyBytes) {
  const response = await fetch(url, {
    method: "POST",
    headers: {
      "content-type": "application/octet-stream",
    },
    body: bodyBytes,
  });
  const payload = new Uint8Array(await response.arrayBuffer());
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}: ${new TextDecoder().decode(payload)}`);
  }
  return payload;
}

async function runClientFlow(baseUrl) {
  const request = encodeThirdPartyClientLicenseRequest({
    schemaVersion: 1,
    pluginId: "com.example.harness-client",
    pluginVersion: "0.0.1",
    accountIdHash: hexToBytes(
      "8f9d6f9dd5f44b969531b4ef5fdf6f18f5e6558cfbd19f7117fd2dc0138f8f8a",
    ),
    requestNonce: hexToBytes("00112233445566778899aabbccddeeff"),
    ephemeralPublicKey: hexToBytes(
      "04aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff66778899",
    ),
    challengeToken: "harness-token",
  });

  const responseBytes = await postBinary(
    `${baseUrl}/v1/third-party/client-license`,
    request,
  );
  return decodeThirdPartyClientLicenseResponse(responseBytes);
}

async function runServerFlow(baseUrl) {
  const request = encodeThirdPartyServerPluginRegistration({
    schemaVersion: 1,
    pluginId: "com.example.harness-server",
    pluginVersion: "0.0.1",
    vendorId: "example-vendor",
    signingPublicKey: hexToBytes(
      "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
    ),
    capabilities: ["license.issue", "license.audit"],
    manifestHash: hexToBytes(
      "c3c3c3c3111122223333444455556666777788889999aaaabbbbccccddddeeee",
    ),
  });

  const responseBytes = await postBinary(
    `${baseUrl}/v1/third-party/server-registration`,
    request,
  );
  return decodeThirdPartyServerPluginGrant(responseBytes);
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const [client, server] = await Promise.all([
    runClientFlow(args.baseUrl),
    runServerFlow(args.baseUrl),
  ]);

  if (client.status !== 0) {
    throw new Error(`client flow returned non-zero status: ${client.status}`);
  }
  if (server.status !== 0) {
    throw new Error(`server flow returned non-zero status: ${server.status}`);
  }

  console.log(
    JSON.stringify(
      {
        ok: true,
        baseUrl: args.baseUrl,
        client,
        server,
      },
      null,
      2,
    ),
  );
}

main().catch((error) => {
  console.error(`[plugin-sdk harness] ${error.message}`);
  process.exit(1);
});
