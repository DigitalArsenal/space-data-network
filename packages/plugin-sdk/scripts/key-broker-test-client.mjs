#!/usr/bin/env node

import { createLibp2p } from "libp2p";
import { webSockets } from "@libp2p/websockets";
import { noise } from "@chainsafe/libp2p-noise";
import { multiaddr } from "@multiformats/multiaddr";
import {
  KEY_BROKER_PROTOCOL_ID,
  PUBLIC_KEY_PROTOCOL_ID,
  decodeKeyBrokerResponse,
  decodePublicKeyResponse,
  encodeKeyBrokerRequest,
} from "../src/index.js";

const DEFAULT_NODE_INFO_URL = "http://127.0.0.1:5001/api/node/info";
const DEFAULT_TIMEOUT_MS = 15000;

function parseArgs(argv) {
  const out = {
    nodeInfoUrl: DEFAULT_NODE_INFO_URL,
    multiaddr: "",
    timeoutMs: DEFAULT_TIMEOUT_MS,
    requestHex: "",
  };

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const next = argv[i + 1];
    if (arg === "--node-info-url" && next) {
      out.nodeInfoUrl = next;
      i++;
      continue;
    }
    if (arg === "--multiaddr" && next) {
      out.multiaddr = next;
      i++;
      continue;
    }
    if (arg === "--timeout-ms" && next) {
      const parsed = Number(next);
      if (Number.isFinite(parsed) && parsed > 0) {
        out.timeoutMs = parsed;
      }
      i++;
      continue;
    }
    if (arg === "--request-hex" && next) {
      out.requestHex = next;
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
  console.log(`Usage:
  node scripts/key-broker-test-client.mjs [options]

Options:
  --node-info-url <url>   Node-info endpoint (default: ${DEFAULT_NODE_INFO_URL})
  --multiaddr <addr>      Override libp2p multiaddr directly
  --timeout-ms <ms>       Dial/read timeout (default: ${DEFAULT_TIMEOUT_MS})
  --request-hex <hex>     Optional raw protocol packet hex to send to key-broker
  --help                  Show this help
`);
}

function normalizeAddressList(value) {
  if (Array.isArray(value)) {
    return value
      .filter((entry) => typeof entry === "string")
      .map((entry) => entry.trim())
      .filter((entry) => entry.length > 0);
  }
  if (typeof value === "string") {
    return value
      .split(/[\s,]+/)
      .map((entry) => entry.trim())
      .filter((entry) => entry.length > 0);
  }
  return [];
}

function appendPeerId(address, peerId) {
  if (!peerId || address.includes("/p2p/")) {
    return address;
  }
  return `${address.replace(/\/+$/, "")}/p2p/${peerId}`;
}

function selectBestAddress(addresses, peerId) {
  const scored = [...addresses].sort((a, b) => scoreAddress(b) - scoreAddress(a));
  return appendPeerId(scored[0], peerId);
}

function scoreAddress(address) {
  let score = 0;
  if (address.includes("/wss")) {
    score += 200;
  } else if (address.includes("/ws")) {
    score += 100;
  }
  if (!address.includes("/ip4/127.0.0.1/") && !address.includes("/ip6/::1/")) {
    score += 50;
  }
  if (address.includes("/p2p/")) {
    score += 20;
  }
  return score;
}

async function resolveMultiaddrFromNodeInfo(nodeInfoUrl) {
  const response = await fetch(nodeInfoUrl, {
    method: "GET",
    headers: { accept: "application/json" },
    cache: "no-store",
  });
  if (!response.ok) {
    throw new Error(`node-info HTTP ${response.status}`);
  }
  const body = await response.json();
  const root = body && typeof body === "object" ? body : {};
  const data = root.data && typeof root.data === "object" ? root.data : {};
  const node = root.node && typeof root.node === "object" ? root.node : {};

  const peerId =
    root.peer_id ||
    root.peerId ||
    data.peer_id ||
    data.peerId ||
    node.peer_id ||
    node.peerId ||
    "";

  const addresses =
    normalizeAddressList(root.listen_addresses) ||
    normalizeAddressList(root.listenAddresses);
  const allAddresses =
    addresses.length > 0
      ? addresses
      : normalizeAddressList(
          data.listen_addresses ||
            data.listenAddresses ||
            node.listen_addresses ||
            node.listenAddresses ||
            root.addresses ||
            data.addresses ||
            node.addresses,
        );
  if (allAddresses.length === 0) {
    throw new Error("node-info did not expose any listen addresses");
  }

  return selectBestAddress(allAddresses, peerId);
}

function withTimeout(promise, timeoutMs, label) {
  let timer;
  const timeoutPromise = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${label} timeout`)), timeoutMs);
  });
  return Promise.race([promise, timeoutPromise]).finally(() => clearTimeout(timer));
}

function asUint8Array(value) {
  if (value instanceof Uint8Array) {
    return value;
  }
  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value);
  }
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  return new Uint8Array(0);
}

function concatBuffers(chunks) {
  let total = 0;
  for (const chunk of chunks) {
    total += chunk.length;
  }
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

async function readAll(source) {
  const chunks = [];
  for await (const value of source) {
    const chunk = asUint8Array(value);
    if (chunk.length > 0) {
      chunks.push(chunk);
    }
  }
  return concatBuffers(chunks);
}

function hexToBytes(hex) {
  const normalized = hex.trim().replace(/^0x/i, "");
  if (normalized.length === 0 || normalized.length % 2 !== 0) {
    throw new Error("request hex must have an even number of characters");
  }
  if (!/^[0-9a-fA-F]+$/.test(normalized)) {
    throw new Error("request hex must be valid hexadecimal");
  }
  const out = new Uint8Array(normalized.length / 2);
  for (let i = 0; i < normalized.length; i += 2) {
    out[i / 2] = parseInt(normalized.slice(i, i + 2), 16);
  }
  return out;
}

function bytesToHex(bytes) {
  return [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const resolvedMultiaddr =
    args.multiaddr || (await resolveMultiaddrFromNodeInfo(args.nodeInfoUrl));

  const node = await createLibp2p({
    transports: [webSockets()],
    connectionEncrypters: [noise()],
  });

  try {
    const ma = multiaddr(resolvedMultiaddr);
    const publicKeyStream = await withTimeout(
      node.dialProtocol(ma, PUBLIC_KEY_PROTOCOL_ID),
      args.timeoutMs,
      "public-key dial",
    );
    const publicKeyPayload = await withTimeout(
      readAll(publicKeyStream.source),
      args.timeoutMs,
      "public-key read",
    );
    try {
      await publicKeyStream.close();
    } catch {
      // best effort
    }

    const publicKey = decodePublicKeyResponse(publicKeyPayload);
    const result = {
      ok: true,
      multiaddr: resolvedMultiaddr,
      public_key_hex: bytesToHex(publicKey),
      public_key_len: publicKey.length,
    };

    if (args.requestHex) {
      const requestPacket = hexToBytes(args.requestHex);
      const envelope = encodeKeyBrokerRequest(requestPacket);

      const keyBrokerStream = await withTimeout(
        node.dialProtocol(ma, KEY_BROKER_PROTOCOL_ID),
        args.timeoutMs,
        "key-broker dial",
      );
      await withTimeout(
        keyBrokerStream.sink([envelope]),
        args.timeoutMs,
        "key-broker send",
      );
      const responsePayload = await withTimeout(
        readAll(keyBrokerStream.source),
        args.timeoutMs,
        "key-broker read",
      );
      try {
        await keyBrokerStream.close();
      } catch {
        // best effort
      }

      const response = decodeKeyBrokerResponse(responsePayload);
      result.key_broker = {
        status: response.status,
        packet_len: response.packet.length,
        packet_hex: bytesToHex(response.packet),
      };
    }

    console.log(JSON.stringify(result, null, 2));
  } finally {
    await node.stop();
  }
}

main().catch((error) => {
  console.error(`[plugin-sdk test-client] ${error.message}`);
  process.exit(1);
});
