/**
 * Integration test: client-decrypt WASM module
 *
 * Loads the compiled plugins/client-decrypt/dist/client-decrypt.wasm via
 * the space-data-module-sdk browser harness and verifies it can decrypt
 * envelopes produced by the JS encryptArtifact implementation.
 *
 * This validates the C++ Crypto++ ECIES decryption matches the WebCrypto
 * implementation end-to-end.
 *
 * Run: node tests/isomorphic/client-decrypt-wasm.test.mjs
 */

import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import {
  encryptArtifact,
  generateX25519KeyPair,
} from "../../packages/module-runner/src/artifact-crypto.js";

// Import the browser harness directly — works in Node.js via built-in WebAssembly
import {
  createBrowserModuleHarness,
} from "../../packages/module-runner/node_modules/space-data-module-sdk/src/testing/browserModuleHarness.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const WASM_PATH = path.resolve(__dirname, "../../plugins/client-decrypt/dist/client-decrypt.wasm");

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

// ── Check WASM exists ─────────────────────────────────────────────────────────

if (!fs.existsSync(WASM_PATH)) {
  console.error(`\nWASM not found: ${WASM_PATH}`);
  console.error("Build it first: node plugins/build.mjs client-decrypt");
  process.exit(1);
}

const wasmBytes = fs.readFileSync(WASM_PATH);
console.log(`Loaded ${path.basename(WASM_PATH)} (${wasmBytes.length} bytes)`);

// ── Load module once, reuse across tests ─────────────────────────────────────

let harness;

await test("load client-decrypt.wasm via browser harness", async () => {
  harness = await createBrowserModuleHarness({
    wasmSource: wasmBytes,
    surface: "direct",
  });
  assert.ok(harness, "harness should be created");
  assert.ok(typeof harness.invoke === "function", "harness should expose invoke()");
});

// ── Decryption tests ──────────────────────────────────────────────────────────

await test("decrypt_artifact: basic round-trip with orbpro info string", async () => {
  const { publicKey, privateKey } = await generateX25519KeyPair();
  const plaintext = new TextEncoder().encode("hello from wasm decryption test");

  const envelope = await encryptArtifact(
    plaintext,
    publicKey,
    "orbpro-key-server-artifact-wrap-v1",
  );
  const envelopeBytes = new TextEncoder().encode(JSON.stringify(envelope));

  const result = await harness.invoke({
    methodId: "decrypt_artifact",
    inputs: [
      { payload: envelopeBytes },
      { payload: privateKey },
    ],
  });

  assert.ok(result.outputs?.length >= 1, "should have at least one output");
  const decrypted = result.outputs[0].payload;
  assert.ok(decrypted instanceof Uint8Array, "output should be Uint8Array");
  assert.deepEqual(decrypted, plaintext, "decrypted bytes should match original plaintext");
});

await test("decrypt_artifact: round-trip with plugin info string", async () => {
  const { publicKey, privateKey } = await generateX25519KeyPair();
  const plaintext = new TextEncoder().encode("plugin-key-server test payload");

  const envelope = await encryptArtifact(
    plaintext,
    publicKey,
    "plugin-key-server-artifact-wrap-v1",
  );
  const envelopeBytes = new TextEncoder().encode(JSON.stringify(envelope));

  const result = await harness.invoke({
    methodId: "decrypt_artifact",
    inputs: [
      { payload: envelopeBytes },
      { payload: privateKey },
    ],
  });

  const decrypted = result.outputs[0].payload;
  assert.deepEqual(decrypted, plaintext);
});

await test("decrypt_artifact: decrypts binary WASM-like payload", async () => {
  const { publicKey, privateKey } = await generateX25519KeyPair();
  // Fake WASM magic + random bytes
  const plaintext = new Uint8Array([
    0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00,
    ...crypto.getRandomValues(new Uint8Array(256)),
  ]);

  const envelope = await encryptArtifact(plaintext, publicKey);
  const envelopeBytes = new TextEncoder().encode(JSON.stringify(envelope));

  const result = await harness.invoke({
    methodId: "decrypt_artifact",
    inputs: [
      { payload: envelopeBytes },
      { payload: privateKey },
    ],
  });

  const decrypted = result.outputs[0].payload;
  assert.deepEqual(decrypted, plaintext, "binary payload should survive ECIES round-trip");
  assert.equal(decrypted[0], 0x00);
  assert.equal(decrypted[1], 0x61);
  assert.equal(decrypted[2], 0x73);
  assert.equal(decrypted[3], 0x6D);
});

await test("decrypt_artifact: wrong private key returns error", async () => {
  const { publicKey } = await generateX25519KeyPair();
  const { privateKey: wrongPrivKey } = await generateX25519KeyPair();
  const plaintext = new TextEncoder().encode("secret");

  const envelope = await encryptArtifact(plaintext, publicKey);
  const envelopeBytes = new TextEncoder().encode(JSON.stringify(envelope));

  const result = await harness.invoke({
    methodId: "decrypt_artifact",
    inputs: [
      { payload: envelopeBytes },
      { payload: wrongPrivKey },
    ],
  });

  // The C++ module should return an error response (non-zero status or empty output)
  const hasError = result.status !== 0 || result.errorMessage ||
    !result.outputs?.[0]?.payload?.length;
  assert.ok(hasError, "wrong key should produce an error or empty output");
});

// ── Cleanup ───────────────────────────────────────────────────────────────────

if (harness?.destroy) {
  await harness.destroy();
}

console.log("\nDone.");
if (failures > 0) {
  process.exit(1);
}
