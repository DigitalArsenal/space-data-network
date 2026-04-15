/**
 * Module invoke codec integration test.
 *
 * Verifies that encodePluginInvokeRequest / decodePluginInvokeResponse
 * work correctly — which is the wire format the C++ WASM modules will
 * speak on plugin_invoke_stream.
 *
 * This test runs purely in JS (no WASM binary needed) to validate the
 * codec before doing a full C++ build.
 *
 * Run: node tests/isomorphic/module-invoke.test.mjs
 */

import assert from "node:assert/strict";
import {
  encodePluginInvokeRequest,
  decodePluginInvokeRequest,
  encodePluginInvokeResponse,
  decodePluginInvokeResponse,
} from "space-data-module-sdk/invoke";

const PASS = "\x1b[32mPASS\x1b[0m";
const FAIL = "\x1b[31mFAIL\x1b[0m";

async function test(name, fn) {
  try {
    await fn();
    console.log(`${PASS}: ${name}`);
  } catch (err) {
    console.error(`${FAIL}: ${name}`);
    console.error(err);
    process.exitCode = 1;
  }
}

// ── Tests ────────────────────────────────────────────────────────────────────

await test("encodePluginInvokeRequest round-trips method ID", () => {
  const bytes = encodePluginInvokeRequest({ methodId: "deliver_plugin" });
  assert.ok(bytes instanceof Uint8Array, "should return Uint8Array");
  assert.ok(bytes.length > 0, "should have bytes");

  const decoded = decodePluginInvokeRequest(bytes);
  assert.equal(decoded.methodId, "deliver_plugin");
});

await test("encodePluginInvokeRequest with two input frames", () => {
  const clientPub = new Uint8Array(32).fill(0x42);
  const cid = new TextEncoder().encode("Qm1234567890abcdef");

  const bytes = encodePluginInvokeRequest({
    methodId: "deliver_plugin",
    inputs: [
      { payload: clientPub },
      { payload: cid },
    ],
  });

  const decoded = decodePluginInvokeRequest(bytes);
  assert.equal(decoded.methodId, "deliver_plugin");
  assert.equal(decoded.inputs.length, 2);
  assert.equal(decoded.inputs[0].payload.length, 32);
  assert.ok(decoded.inputs[0].payload.every((b) => b === 0x42));
  assert.deepEqual(decoded.inputs[1].payload, cid);
});

await test("encodePluginInvokeResponse with output frame", () => {
  const payload = new TextEncoder().encode('{"foo":"bar"}');

  const bytes = encodePluginInvokeResponse({
    statusCode: 0,
    outputs: [{ payload }],
  });

  const decoded = decodePluginInvokeResponse(bytes);
  assert.equal(decoded.statusCode, 0);
  assert.equal(decoded.outputs.length, 1);
  assert.deepEqual(decoded.outputs[0].payload, payload);
});

await test("encodePluginInvokeResponse with error", () => {
  const bytes = encodePluginInvokeResponse({
    statusCode: 1,
    errorMessage: "something went wrong",
  });

  const decoded = decodePluginInvokeResponse(bytes);
  assert.equal(decoded.statusCode, 1);
  assert.equal(decoded.errorMessage, "something went wrong");
});

await test("deliver_plugin invoke envelope is structurally valid", () => {
  // Simulate what the client sends to plugin-delivery module
  const clientPubKey = new Uint8Array(32).fill(0xab);
  const cid = new TextEncoder().encode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi");

  const reqBytes = encodePluginInvokeRequest({
    methodId: "deliver_plugin",
    inputs: [
      { payload: clientPubKey },
      { payload: cid },
    ],
  });

  assert.ok(reqBytes.length > 40, "request should be non-trivial");

  const decoded = decodePluginInvokeRequest(reqBytes);
  assert.equal(decoded.methodId, "deliver_plugin");
  assert.equal(decoded.inputs[0].payload.length, 32);
  assert.equal(decoded.inputs[1].payload.length, cid.length);
});

await test("decrypt_artifact invoke envelope is structurally valid", () => {
  // Simulate what the client sends to client-decrypt module
  const envelope = JSON.stringify({
    keyEncryption: {
      scheme: "ecies-x25519-hkdf-sha256-aes-256-gcm",
      ephemeralPublicKeyHex: "a".repeat(64),
      hkdfSaltB64: "dGVzdA==",
      wrapIvB64: "dGVzdA==",
      wrappedKeyB64: "dGVzdA==",
      wrappedKeyTagB64: "dGVzdA==",
    },
    contentEncryption: {
      algorithm: "aes-256-gcm",
      ivB64: "dGVzdA==",
      tagB64: "dGVzdA==",
      ciphertextB64: "dGVzdA==",
    },
  });
  const envelopeBytes = new TextEncoder().encode(envelope);
  const privKey = new Uint8Array(32).fill(0xcd);

  const reqBytes = encodePluginInvokeRequest({
    methodId: "decrypt_artifact",
    inputs: [
      { payload: envelopeBytes },
      { payload: privKey },
    ],
  });

  const decoded = decodePluginInvokeRequest(reqBytes);
  assert.equal(decoded.methodId, "decrypt_artifact");
  assert.equal(decoded.inputs.length, 2);
  assert.equal(decoded.inputs[1].payload.length, 32);
});

console.log("\nDone.");
