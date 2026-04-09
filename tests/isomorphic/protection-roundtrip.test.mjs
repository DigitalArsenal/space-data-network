#!/usr/bin/env node
/**
 * Full protection round-trip integration test.
 *
 * End-to-end test using both plugin-delivery and client-decrypt WASMs:
 *
 *   1. Start an in-process Helia node and add a test WASM artifact to IPFS
 *   2. Load plugin-delivery WASM with a custom host that backs ipfs.cat via Helia
 *   3. Client generates X25519 key pair and requests the plugin via its CID
 *   4. plugin-delivery encrypts the artifact for the client's public key
 *   5. Client feeds the encrypted envelope into client-decrypt WASM
 *   6. Verify decrypted bytes match the original artifact
 *
 * This validates the full IPFS-backed protection pipeline: server encryption
 * (C++ Crypto++) + client decryption (C++ Crypto++) with Helia as the
 * content-addressed storage layer.
 *
 * Run: node tests/isomorphic/protection-roundtrip.test.mjs
 * Prereqs:
 *   - npm install in tests/isomorphic/
 *   - Pre-built WASMs at:
 *       ../../plugins/plugin-delivery/dist/plugin-delivery.wasm
 *       ../../plugins/client-decrypt/dist/client-decrypt.wasm
 *     OR standalone repos:
 *       PLUGIN_DELIVERY_WASM=path/to/plugin-delivery.wasm
 *       CLIENT_DECRYPT_WASM=path/to/client-decrypt.wasm
 */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createHelia } from "helia";
import { unixfs } from "@helia/unixfs";
import { MemoryBlockstore } from "blockstore-core/memory";
import { MemoryDatastore } from "datastore-core/memory";

import {
  createBrowserModuleHarness,
} from "../../packages/module-runner/node_modules/space-data-module-sdk/src/testing/browserModuleHarness.js";

import {
  createBrowserWasiShim,
} from "../../packages/module-runner/node_modules/space-data-module-sdk/src/host/wasiShim.js";

import {
  createJsonHostcallBridge,
  DEFAULT_HOSTCALL_IMPORT_MODULE,
} from "../../packages/module-runner/node_modules/space-data-module-sdk/src/host/abi.js";

import {
  encodePluginInvokeRequest,
  decodePluginInvokeResponse,
} from "../../packages/module-runner/node_modules/space-data-module-sdk/src/invoke/codec.js";

import {
  encryptArtifact,
  decryptArtifact,
  generateX25519KeyPair,
} from "../../packages/module-runner/src/artifact-crypto.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// ── Resolve WASM paths ──────────────────────────────────────────────────────

const DELIVERY_WASM_PATH =
  process.env.PLUGIN_DELIVERY_WASM ??
  path.resolve(__dirname, "../../plugins/plugin-delivery/dist/plugin-delivery.wasm");

const DECRYPT_WASM_PATH =
  process.env.CLIENT_DECRYPT_WASM ??
  path.resolve(__dirname, "../../plugins/client-decrypt/dist/client-decrypt.wasm");

// Also try standalone repo paths
function resolveWasm(primary, fallbackName) {
  if (fs.existsSync(primary)) return primary;
  const standaloneRepo = path.resolve(
    __dirname,
    `../../../space-data-network-${fallbackName}/dist/${fallbackName}.wasm`,
  );
  if (fs.existsSync(standaloneRepo)) return standaloneRepo;
  console.error(`WASM not found: ${primary}`);
  console.error(`Also tried: ${standaloneRepo}`);
  process.exit(1);
}

const deliveryWasmPath = resolveWasm(DELIVERY_WASM_PATH, "plugin-delivery");
const decryptWasmPath = resolveWasm(DECRYPT_WASM_PATH, "client-decrypt");

// ── Test runner ─────────────────────────────────────────────────────────────

const PASS = "\x1b[32mPASS\x1b[0m";
const FAIL = "\x1b[31mFAIL\x1b[0m";
let failures = 0;

async function test(name, fn) {
  try {
    await fn();
    console.log(`${PASS}: ${name}`);
  } catch (err) {
    console.error(`${FAIL}: ${name}`);
    console.error(err);
    failures++;
  }
}

// ── Custom host with Helia-backed ipfs.cat ──────────────────────────────────

function createIpfsHost(heliaFs) {
  return {
    runtimeTarget: "node",
    capabilities: new Set(["ipfs"]),
    listCapabilities() { return ["ipfs"]; },
    listSupportedCapabilities() { return ["ipfs"]; },
    hasCapability(cap) { return cap === "ipfs"; },
    listOperations() { return ["ipfs.cat"]; },

    // The dispatch function is called synchronously by the WASM bridge,
    // so we need to handle ipfs.cat by returning pre-fetched content.
    // We'll use a sync content store pre-populated from Helia.
    _contentStore: new Map(),

    dispatch(operation, params) {
      if (operation === "ipfs.cat") {
        // Accept both {"cid":"..."} and {"path":"/ipfs/..."} formats
        let cid = params?.cid ?? "";
        if (!cid && params?.path) {
          cid = params.path.replace(/^\/ipfs\//, "");
        }
        const data = this._contentStore.get(cid);
        if (!data) {
          throw new Error(`CID not found in test store: ${cid}`);
        }
        // Return as Uint8Array — the bridge's encodeHostcallValue will
        // wrap it as {"__type":"bytes","base64":"..."}
        return new Uint8Array(data);
      }
      // Delegate to basic host ops
      if (operation === "host.runtimeTarget") return "node";
      if (operation === "host.listCapabilities") return ["ipfs"];
      if (operation === "host.hasCapability") return params === "ipfs";
      if (operation === "clock.now") return Date.now();
      if (operation === "random.bytes") {
        const n = params?.count ?? 32;
        const buf = new Uint8Array(n);
        crypto.getRandomValues(buf);
        let b64 = "";
        for (let i = 0; i < buf.length; i++) b64 += String.fromCharCode(buf[i]);
        return btoa(b64);
      }
      throw new Error(`Unsupported operation: ${operation}`);
    },
  };
}

// ── Main test ───────────────────────────────────────────────────────────────

console.log(`plugin-delivery WASM: ${deliveryWasmPath}`);
console.log(`client-decrypt WASM:  ${decryptWasmPath}`);

const deliveryWasm = fs.readFileSync(deliveryWasmPath);
const decryptWasm = fs.readFileSync(decryptWasmPath);
console.log(`  delivery: ${deliveryWasm.length} bytes`);
console.log(`  decrypt:  ${decryptWasm.length} bytes\n`);

// Create Helia node
let helia;
let heliaUfs;

await test("start Helia node (in-memory, no network)", async () => {
  helia = await createHelia({
    blockstore: new MemoryBlockstore(),
    datastore: new MemoryDatastore(),
    start: false,
  });
  heliaUfs = unixfs(helia);
  assert.ok(helia, "Helia node should start");
});

// Add a test artifact to IPFS
let testArtifact;
let testCid;

await test("add test artifact to IPFS via Helia", async () => {
  // Create a fake WASM module (magic + version + random payload)
  testArtifact = new Uint8Array([
    0x00, 0x61, 0x73, 0x6d, // WASM magic
    0x01, 0x00, 0x00, 0x00, // WASM version 1
    ...crypto.getRandomValues(new Uint8Array(512)),
  ]);

  testCid = await heliaUfs.addBytes(testArtifact);
  assert.ok(testCid, "CID should be returned");
  console.log(`    CID: ${testCid.toString()}`);

  // Verify we can read it back
  const chunks = [];
  for await (const chunk of heliaUfs.cat(testCid)) {
    chunks.push(chunk);
  }
  const readBack = new Uint8Array(
    chunks.reduce((n, c) => n + c.length, 0),
  );
  let off = 0;
  for (const c of chunks) {
    readBack.set(c, off);
    off += c.length;
  }
  assert.deepEqual(readBack, testArtifact, "Helia should store and retrieve artifact");
});

// Pre-fetch content from Helia into the sync store for the host bridge
let ipfsHost;

await test("create IPFS host with Helia content", async () => {
  ipfsHost = createIpfsHost(heliaUfs);

  // Read the artifact from Helia and store in the sync content map
  const chunks = [];
  for await (const chunk of heliaUfs.cat(testCid)) {
    chunks.push(chunk);
  }
  const data = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0));
  let off = 0;
  for (const c of chunks) { data.set(c, off); off += c.length; }

  ipfsHost._contentStore.set(testCid.toString(), data);
  assert.ok(ipfsHost._contentStore.has(testCid.toString()));
});

// Load plugin-delivery WASM with custom IPFS dispatch
let deliveryHarness;

await test("load plugin-delivery WASM with IPFS host", async () => {
  // Custom dispatch that handles ipfs.cat + basic host ops
  function dispatch(operation, params) {
    if (operation === "ipfs.cat") {
      let cid = params?.cid ?? "";
      if (!cid && params?.path) {
        cid = params.path.replace(/^\/ipfs\//, "");
      }
      const data = ipfsHost._contentStore.get(cid);
      if (!data) {
        throw new Error(`CID not found in test store: ${cid}`);
      }
      return new Uint8Array(data);
    }
    if (operation === "host.runtimeTarget") return "node";
    if (operation === "host.listCapabilities") return ["ipfs"];
    if (operation === "host.hasCapability") return params?.capability === "ipfs";
    if (operation === "host.listOperations") return ["ipfs.cat"];
    if (operation === "clock.now") return Date.now();
    if (operation === "random.bytes") {
      const n = params?.length ?? 32;
      return crypto.getRandomValues(new Uint8Array(n));
    }
    throw new Error(`Unsupported operation: ${operation}`);
  }

  const wasmModule = await WebAssembly.compile(deliveryWasm);
  const wasi = createBrowserWasiShim({ args: [], env: {} });
  const importObject = { ...wasi.imports };

  let instance = null;
  const bridge = createJsonHostcallBridge({
    dispatch,
    getMemory: () => instance.exports.memory,
  });
  Object.assign(importObject, bridge.imports);

  instance = await WebAssembly.instantiate(wasmModule, importObject);
  if (instance.exports.memory) wasi.setMemory(instance.exports.memory);
  if (instance.exports._initialize) instance.exports._initialize();

  // Build invoke wrapper matching harness API
  deliveryHarness = {
    invoke({ methodId, inputs }) {
      const reqBytes = encodePluginInvokeRequest({ methodId, inputs: (inputs || []).map(i => ({ payload: i.payload })) });

      const alloc = instance.exports.plugin_alloc;
      const free = instance.exports.plugin_free;
      const invoke = instance.exports.plugin_invoke_stream;

      const inPtr = alloc(reqBytes.length);
      new Uint8Array(instance.exports.memory.buffer, inPtr, reqBytes.length).set(reqBytes);

      const outLenPtr = alloc(4);
      const outPtr = invoke(inPtr, reqBytes.length, outLenPtr);
      free(inPtr, reqBytes.length);

      const outLen = new DataView(instance.exports.memory.buffer).getUint32(outLenPtr, true);
      free(outLenPtr, 4);

      if (!outPtr || outLen === 0) {
        return { statusCode: 1, errorMessage: "invoke returned null", outputs: [] };
      }

      const outBytes = new Uint8Array(instance.exports.memory.buffer, outPtr, outLen).slice();
      free(outPtr, outLen);

      return decodePluginInvokeResponse(outBytes);
    },
    destroy() {},
  };

  assert.ok(deliveryHarness, "delivery harness should be created");
});

// Get server's public key
let serverPubKey;

await test("get_public_key from plugin-delivery", async () => {
  const result = await deliveryHarness.invoke({
    methodId: "get_public_key",
    inputs: [],
  });

  assert.ok(result.outputs?.length >= 1);
  serverPubKey = result.outputs[0].payload;
  assert.equal(serverPubKey.length, 32, "server public key must be 32 bytes");
  assert.ok(!serverPubKey.every((b) => b === 0), "public key should not be all zeros");
});

// Full round-trip: deliver_plugin → client-decrypt
await test("full IPFS round-trip: deliver_plugin → decrypt_artifact", async () => {
  // 1. Client generates X25519 key pair
  const { publicKey: clientPub, privateKey: clientPriv } = await generateX25519KeyPair();

  // 2. Client requests plugin delivery (sends pub key + CID)
  const cidBytes = new TextEncoder().encode(testCid.toString());
  const deliverResult = await deliveryHarness.invoke({
    methodId: "deliver_plugin",
    inputs: [
      { payload: clientPub },
      { payload: cidBytes },
    ],
  });

  if (deliverResult.statusCode !== 0 || deliverResult.errorMessage) {
    console.log(`    status: ${deliverResult.statusCode}, error: ${deliverResult.errorMessage}`);
  }
  assert.ok(deliverResult.outputs?.length >= 1, "deliver_plugin should return output");
  const envelopeBytes = deliverResult.outputs[0].payload;
  assert.ok(envelopeBytes.length > 0, "envelope should be non-empty");

  // 3. Parse envelope JSON to verify structure
  const envelopeStr = new TextDecoder().decode(envelopeBytes);
  const envelope = JSON.parse(envelopeStr);
  assert.equal(
    envelope.keyEncryption.scheme,
    "ecies-x25519-hkdf-sha256-aes-256-gcm",
    "envelope should use correct scheme",
  );
  assert.ok(envelope.keyEncryption.ephemeralPublicKeyHex, "should have ephemeral key");
  assert.ok(envelope.contentEncryption.ciphertextB64, "should have ciphertext");

  // 4. Load client-decrypt WASM and decrypt
  const decryptHarness = await createBrowserModuleHarness({
    wasmSource: decryptWasm,
    surface: "direct",
  });

  const decryptResult = await decryptHarness.invoke({
    methodId: "decrypt_artifact",
    inputs: [
      { payload: envelopeBytes },
      { payload: clientPriv },
    ],
  });

  assert.ok(decryptResult.outputs?.length >= 1, "decrypt should return output");
  const decrypted = decryptResult.outputs[0].payload;
  assert.ok(decrypted instanceof Uint8Array, "decrypted output should be Uint8Array");
  assert.deepEqual(
    decrypted,
    testArtifact,
    "decrypted bytes must match original artifact from IPFS",
  );

  // 5. Verify WASM magic bytes
  assert.equal(decrypted[0], 0x00);
  assert.equal(decrypted[1], 0x61);
  assert.equal(decrypted[2], 0x73);
  assert.equal(decrypted[3], 0x6d);
  console.log(`    Decrypted ${decrypted.length} bytes successfully`);

  if (decryptHarness?.destroy) await decryptHarness.destroy();
});

// Test with JS WebCrypto decryption (cross-validate C++ and JS impls)
await test("cross-validate: JS WebCrypto decrypts C++ encrypted envelope", async () => {
  const { publicKey: clientPub, privateKey: clientPriv } = await generateX25519KeyPair();
  const cidBytes = new TextEncoder().encode(testCid.toString());

  const deliverResult = await deliveryHarness.invoke({
    methodId: "deliver_plugin",
    inputs: [
      { payload: clientPub },
      { payload: cidBytes },
    ],
  });

  const envelopeStr = new TextDecoder().decode(deliverResult.outputs[0].payload);

  // Decrypt using JS WebCrypto (artifact-crypto.js)
  const decrypted = await decryptArtifact(envelopeStr, clientPriv);
  assert.deepEqual(
    decrypted,
    testArtifact,
    "JS WebCrypto should decrypt C++ Crypto++ envelope",
  );
});

// Test: JS WebCrypto encrypts → C++ WASM decrypts
await test("cross-validate: C++ WASM decrypts JS WebCrypto encrypted envelope", async () => {
  const { publicKey: clientPub, privateKey: clientPriv } = await generateX25519KeyPair();

  const envelope = await encryptArtifact(testArtifact, clientPub);
  const envelopeBytes = new TextEncoder().encode(JSON.stringify(envelope));

  const decryptHarness = await createBrowserModuleHarness({
    wasmSource: decryptWasm,
    surface: "direct",
  });

  const result = await decryptHarness.invoke({
    methodId: "decrypt_artifact",
    inputs: [
      { payload: envelopeBytes },
      { payload: clientPriv },
    ],
  });

  assert.deepEqual(
    result.outputs[0].payload,
    testArtifact,
    "C++ WASM should decrypt JS WebCrypto envelope",
  );

  if (decryptHarness?.destroy) await decryptHarness.destroy();
});

// ── Cleanup ─────────────────────────────────────────────────────────────────

if (deliveryHarness?.destroy) await deliveryHarness.destroy();
if (helia) await helia.stop();

console.log(`\nDone. ${failures === 0 ? "All tests passed." : `${failures} failure(s).`}`);
if (failures > 0) process.exit(1);
