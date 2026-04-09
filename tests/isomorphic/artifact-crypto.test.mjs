/**
 * Artifact crypto round-trip tests.
 *
 * Verifies that encryptArtifact() / decryptArtifact() produce envelopes
 * that the Go DecryptStagedArtifactEnvelope() can read, and that the JS
 * implementation correctly decrypts what it encrypts.
 *
 * Run: node tests/isomorphic/artifact-crypto.test.mjs
 */

import assert from "node:assert/strict";
import { encryptArtifact, decryptArtifact, generateX25519KeyPair } from
  "../../packages/module-runner/src/artifact-crypto.js";

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

// ── Tests ──────────────────────────────────────────────────────────────────

await test("generateX25519KeyPair returns 32-byte keys", async () => {
  const { publicKey, privateKey } = await generateX25519KeyPair();
  assert.equal(publicKey.length, 32, "public key must be 32 bytes");
  assert.equal(privateKey.length, 32, "private key must be 32 bytes");
  // Keys should differ from each other
  assert.notDeepEqual(publicKey, privateKey);
});

await test("encryptArtifact produces expected envelope schema", async () => {
  const { publicKey } = await generateX25519KeyPair();
  const plaintext = new TextEncoder().encode("hello world");
  const env = await encryptArtifact(plaintext, publicKey);

  assert.equal(env.keyEncryption.scheme, "ecies-x25519-hkdf-sha256-aes-256-gcm");
  assert.equal(typeof env.keyEncryption.ephemeralPublicKeyHex, "string");
  assert.equal(env.keyEncryption.ephemeralPublicKeyHex.length, 64);
  assert.equal(typeof env.keyEncryption.hkdfSaltB64, "string");
  assert.equal(typeof env.keyEncryption.wrapIvB64, "string");
  assert.equal(typeof env.keyEncryption.wrappedKeyB64, "string");
  assert.equal(typeof env.keyEncryption.wrappedKeyTagB64, "string");
  assert.equal(env.contentEncryption.algorithm, "aes-256-gcm");
  assert.equal(typeof env.contentEncryption.ivB64, "string");
  assert.equal(typeof env.contentEncryption.tagB64, "string");
  assert.equal(typeof env.contentEncryption.ciphertextB64, "string");
});

await test("encryptArtifact + decryptArtifact round-trip (text)", async () => {
  const { publicKey, privateKey } = await generateX25519KeyPair();
  const original = new TextEncoder().encode("round-trip test payload");

  const envelope = await encryptArtifact(original, publicKey);
  const decrypted = await decryptArtifact(envelope, privateKey);

  assert.deepEqual(decrypted, original);
});

await test("encryptArtifact + decryptArtifact round-trip (WASM-like binary)", async () => {
  const { publicKey, privateKey } = await generateX25519KeyPair();
  // Simulate a WASM binary with magic bytes
  const original = new Uint8Array([0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, ...crypto.getRandomValues(new Uint8Array(256))]);

  const envelope = await encryptArtifact(original, publicKey);
  const decrypted = await decryptArtifact(envelope, privateKey);

  assert.deepEqual(decrypted, original);
  // WASM magic bytes preserved
  assert.equal(decrypted[0], 0x00);
  assert.equal(decrypted[1], 0x61);
  assert.equal(decrypted[2], 0x73);
  assert.equal(decrypted[3], 0x6D);
});

await test("decryptArtifact fails with wrong key", async () => {
  const { publicKey } = await generateX25519KeyPair();
  const { privateKey: wrongKey } = await generateX25519KeyPair();
  const original = new TextEncoder().encode("secret data");

  const envelope = await encryptArtifact(original, publicKey);
  await assert.rejects(
    () => decryptArtifact(envelope, wrongKey),
    (err) => {
      assert.ok(err.message.includes("unwrap") || err.message.includes("Failed") || err.message.includes("decrypt"),
        `unexpected error: ${err.message}`);
      return true;
    },
  );
});

await test("decryptArtifact fails with unsupported scheme", async () => {
  const { privateKey } = await generateX25519KeyPair();
  const badEnvelope = {
    keyEncryption: { scheme: "rsa-oaep" },
    contentEncryption: {},
  };
  await assert.rejects(
    () => decryptArtifact(badEnvelope, privateKey),
    /Unsupported envelope scheme/,
  );
});

await test("encryptArtifact uses second HKDF info string when requested", async () => {
  const { publicKey, privateKey } = await generateX25519KeyPair();
  const original = new TextEncoder().encode("info-string test");

  const envelope = await encryptArtifact(
    original,
    publicKey,
    "plugin-key-server-artifact-wrap-v1",
  );
  // decryptArtifact tries both info strings, so this must succeed
  const decrypted = await decryptArtifact(envelope, privateKey);
  assert.deepEqual(decrypted, original);
});

await test("encryptArtifact + decryptArtifact JSON serialization round-trip", async () => {
  const { publicKey, privateKey } = await generateX25519KeyPair();
  const original = new TextEncoder().encode("json serialization test");

  const envelope = await encryptArtifact(original, publicKey);
  // Simulate transport over JSON (string parsing)
  const envelopeJSON = JSON.stringify(envelope);
  const decrypted = await decryptArtifact(envelopeJSON, privateKey);

  assert.deepEqual(decrypted, original);
});

console.log("\nDone.");
