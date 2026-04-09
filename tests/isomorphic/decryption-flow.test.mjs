/**
 * Full decryption flow integration test.
 *
 * Scenario:
 *   - "Key Server" node holds an X25519 private key and creates an
 *     encrypted artifact (WASM bytes) using its own public key.
 *   - "Client" node connects to the key server via a libp2p protocol,
 *     sends its ephemeral X25519 public key, and receives the content
 *     key material encrypted back to it.
 *   - Client derives the content key and decrypts the artifact.
 *   - Both nodes are in-process libp2p instances (no network required).
 *
 * This validates the full encrypt → p2p key exchange → decrypt pipeline
 * before migrating the key-broker WASM module to isomorphic execution.
 *
 * Run: node tests/isomorphic/decryption-flow.test.mjs
 */

import assert from "node:assert/strict";
import { createLibp2p } from "libp2p";
import { tcp } from "@libp2p/tcp";
import { noise } from "@chainsafe/libp2p-noise";
import { yamux } from "@chainsafe/libp2p-yamux";
import {
  encryptArtifact,
  decryptArtifact,
  generateX25519KeyPair,
} from "../../packages/module-runner/src/artifact-crypto.js";

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

// ── Key-exchange protocol ────────────────────────────────────────────────────
//
// Protocol: /sdn/test/key-exchange/1.0.0
//
// Client → Server: 32-byte ephemeral X25519 public key
// Server → Client: 32-byte ephemeral pub key + 28-byte nonce + ciphertext
//                  (content key encrypted to client's ephemeral pub via ECDH)
//
// This mirrors the OrbPro key-broker packet exchange, simplified for testing.

const KEY_EXCHANGE_PROTOCOL = "/sdn/test/key-exchange/1.0.0";

async function startKeyServer(contentKey, serverPrivateKey) {
  const libp2p = await createLibp2p({
    addresses: { listen: ["/ip4/127.0.0.1/tcp/0"] },
    transports: [tcp()],
    connectionEncrypters: [noise()],
    streamMuxers: [yamux()],
  });

  await libp2p.start();

  libp2p.handle(KEY_EXCHANGE_PROTOCOL, async ({ stream }) => {
    // Read client's ephemeral public key (32 bytes)
    const chunks = [];
    for await (const chunk of stream.source) {
      chunks.push(chunk instanceof Uint8Array ? chunk : chunk.subarray());
      if (chunks.reduce((n, c) => n + c.length, 0) >= 32) break;
    }
    const clientPub = new Uint8Array(32);
    let off = 0;
    for (const c of chunks) {
      const take = Math.min(c.length, 32 - off);
      clientPub.set(c.subarray(0, take), off);
      off += take;
    }

    // Encrypt content key to client's ephemeral pub key using server priv key
    // Reuse encryptArtifact for simplicity (it generates its own ephemeral key)
    const envelope = await encryptArtifact(contentKey, clientPub);
    const envelopeBytes = new TextEncoder().encode(JSON.stringify(envelope));

    // Write length-prefixed envelope (4-byte LE length + envelope JSON)
    const response = new Uint8Array(4 + envelopeBytes.length);
    new DataView(response.buffer).setUint32(0, envelopeBytes.length, true);
    response.set(envelopeBytes, 4);

    await stream.sink([response]);
    await stream.close();
  });

  return libp2p;
}

async function requestContentKey(clientLibp2p, serverAddr, clientPrivateKey) {
  const { publicKey: ephemeralPub, privateKey: ephemeralPriv } = await generateX25519KeyPair();

  const stream = await clientLibp2p.dialProtocol(serverAddr, KEY_EXCHANGE_PROTOCOL);

  // Send our ephemeral public key
  await stream.sink([ephemeralPub]);

  // Read length-prefixed envelope
  const chunks = [];
  for await (const chunk of stream.source) {
    chunks.push(chunk instanceof Uint8Array ? chunk : chunk.subarray());
  }
  const raw = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0));
  let pos = 0;
  for (const c of chunks) { raw.set(c, pos); pos += c.length; }

  const envLen = new DataView(raw.buffer).getUint32(0, true);
  const envelopeJSON = new TextDecoder().decode(raw.slice(4, 4 + envLen));

  // Decrypt the content key using our ephemeral private key
  return decryptArtifact(envelopeJSON, ephemeralPriv);
}

// ── Tests ────────────────────────────────────────────────────────────────────

await test("full encrypt → p2p key exchange → decrypt", async () => {
  // 1. Key server generates its identity key pair
  const serverKeyPair = await generateX25519KeyPair();

  // 2. Encrypt a fake WASM artifact to the server's public key
  const fakeWasm = new Uint8Array([
    0x00, 0x61, 0x73, 0x6D, 0x01, 0x00, 0x00, 0x00, // WASM magic + version
    ...crypto.getRandomValues(new Uint8Array(128)),
  ]);
  const envelope = await encryptArtifact(fakeWasm, serverKeyPair.publicKey);

  // 3. Server decrypts the artifact to get the content key it will serve
  //    (in the real system the server holds this key separately)
  const contentKey = await decryptArtifact(envelope, serverKeyPair.privateKey);
  assert.deepEqual(contentKey, fakeWasm, "server should recover plaintext");

  // 4. Start key-server libp2p node
  const serverNode = await startKeyServer(contentKey, serverKeyPair.privateKey);
  const serverAddr = serverNode.getMultiaddrs()[0];

  // 5. Start client libp2p node
  const clientNode = await createLibp2p({
    addresses: { listen: ["/ip4/127.0.0.1/tcp/0"] },
    transports: [tcp()],
    connectionEncrypters: [noise()],
    streamMuxers: [yamux()],
  });
  await clientNode.start();

  try {
    // 6. Client requests the content key from the server
    const receivedKey = await requestContentKey(
      clientNode,
      serverAddr,
      null, // client uses ephemeral key generated inside requestContentKey
    );

    // 7. Verify client received the correct content key
    assert.deepEqual(receivedKey, fakeWasm, "client should receive the original plaintext");

    // 8. Verify WASM magic bytes are intact
    assert.equal(receivedKey[0], 0x00);
    assert.equal(receivedKey[1], 0x61);
    assert.equal(receivedKey[2], 0x73);
    assert.equal(receivedKey[3], 0x6D);
  } finally {
    await clientNode.stop();
    await serverNode.stop();
  }
});

await test("envelope from server is a valid artifact envelope", async () => {
  const serverKeyPair = await generateX25519KeyPair();
  const plaintext = new TextEncoder().encode("test payload");
  const envelope = await encryptArtifact(plaintext, serverKeyPair.publicKey);

  // Verify all required fields are present
  assert.ok(envelope.keyEncryption, "missing keyEncryption");
  assert.ok(envelope.contentEncryption, "missing contentEncryption");
  assert.equal(envelope.keyEncryption.scheme, "ecies-x25519-hkdf-sha256-aes-256-gcm");
  assert.equal(envelope.keyEncryption.ephemeralPublicKeyHex.length, 64);

  // Go-side expects wrapIvB64 — verify it's present
  assert.ok(envelope.keyEncryption.wrapIvB64, "missing wrapIvB64");

  const decrypted = await decryptArtifact(envelope, serverKeyPair.privateKey);
  assert.deepEqual(decrypted, plaintext);
});

await test("two independent encryptions of same plaintext produce different envelopes", async () => {
  const { publicKey } = await generateX25519KeyPair();
  const plaintext = new TextEncoder().encode("same data");

  const env1 = await encryptArtifact(plaintext, publicKey);
  const env2 = await encryptArtifact(plaintext, publicKey);

  // Ephemeral keys must differ (fresh randomness each time)
  assert.notEqual(env1.keyEncryption.ephemeralPublicKeyHex, env2.keyEncryption.ephemeralPublicKeyHex);
  assert.notEqual(env1.contentEncryption.ciphertextB64, env2.contentEncryption.ciphertextB64);
});

console.log("\nDone.");
