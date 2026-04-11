#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  decodeErrorResponse,
  decodeGrantChallenge,
  decodeGrantProof,
  decodeGrantRequest,
  decodeGrantResponse,
  decodeModuleDeliveryMessage,
  encodeModuleDeliveryMessage,
} from "../src/module-delivery-codec.js";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, "..");
const fixturesDir = path.join(packageRoot, "fixtures/module-delivery/v1");

function parseArgs(argv) {
  const out = {
    fixture: "grant_response",
    type: "",
    wrap: false,
  };

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const next = argv[i + 1];
    if (arg === "--fixture" && next) {
      out.fixture = next;
      i++;
      continue;
    }
    if (arg === "--type" && next) {
      out.type = next;
      i++;
      continue;
    }
    if (arg === "--wrap") {
      out.wrap = true;
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
  console.log(`Usage:
  node scripts/module-delivery-test-client.mjs [options]

Options:
  --fixture <name>   Fixture base name from fixtures/module-delivery/v1 (default: grant_response)
  --type <type>      Explicit message type when using --wrap
  --wrap             Wrap the decoded payload in a ModuleDeliveryMessage and decode it again
  --help             Show this help
`);
}

function hexToBytes(hex) {
  const normalized = String(hex || "").trim();
  if (!/^[0-9a-f]+$/i.test(normalized) || normalized.length % 2 !== 0) {
    throw new Error(`invalid fixture hex: ${hex}`);
  }
  const out = new Uint8Array(normalized.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = Number.parseInt(normalized.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

async function readFixture(name) {
  return hexToBytes(
    await fs.readFile(path.join(fixturesDir, `${name}.hex`), "utf8"),
  );
}

function inferDecoder(name) {
  switch (name) {
    case "grant_request":
      return { type: "grant_request", decode: decodeGrantRequest };
    case "grant_challenge":
      return { type: "grant_challenge", decode: decodeGrantChallenge };
    case "grant_proof":
      return { type: "grant_proof", decode: decodeGrantProof };
    case "grant_response":
      return { type: "grant_response", decode: decodeGrantResponse };
    case "error_response":
      return { type: "error_response", decode: decodeErrorResponse };
    default:
      throw new Error(`unsupported fixture for module-delivery test client: ${name}`);
  }
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const bytes = await readFixture(args.fixture);
  const { type: inferredType, decode } = inferDecoder(args.fixture);
  const type = args.type || inferredType;
  const payload = decode(bytes);

  if (!args.wrap) {
    console.log(
      JSON.stringify(
        {
          protocolId: "/space-data-network/module-delivery/1.0.0",
          type,
          payload,
        },
        null,
        2,
      ),
    );
    return;
  }

  const wrapped = encodeModuleDeliveryMessage({ type, payload });
  const decoded = decodeModuleDeliveryMessage(wrapped);
  console.log(
    JSON.stringify(
      {
        protocolId: "/space-data-network/module-delivery/1.0.0",
        wrappedBytes: wrapped.length,
        message: decoded,
      },
      null,
      2,
    ),
  );
}

main().catch((error) => {
  console.error(`[module-delivery test client] ${error.message}`);
  process.exit(1);
});
