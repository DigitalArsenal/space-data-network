#!/usr/bin/env node

import { createHash } from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import * as flatbuffers from "flatbuffers";
import { ModuleDeliveryMessage } from "../src/generated/space-data-network/module-delivery/v1/module-delivery-message.js";
import {
  decodeErrorResponse,
  decodeGrantChallenge,
  decodeGrantProof,
  decodeGrantRequest,
  decodeGrantResponse,
  decodeModuleDeliveryMessage,
  encodeErrorResponse,
  encodeGrantChallenge,
  encodeGrantProof,
  encodeGrantRequest,
  encodeGrantResponse,
  encodeModuleDeliveryMessage,
} from "../src/module-delivery-codec.js";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, "..");
const fixturesDir = path.join(packageRoot, "fixtures/module-delivery/v1");
const generatedAt = "2026-04-10T00:00:00.000Z";

function hexToBytes(hex) {
  const normalized = String(hex || "")
    .trim()
    .toLowerCase()
    .replace(/^0x/, "");
  if (!/^[0-9a-f]+$/.test(normalized) || normalized.length % 2 !== 0) {
    throw new Error(`invalid hex: ${hex}`);
  }
  const out = new Uint8Array(normalized.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = Number.parseInt(normalized.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

function bytesToHex(bytes) {
  return [...bytes].map((value) => value.toString(16).padStart(2, "0")).join("");
}

function sha256(bytes) {
  return createHash("sha256").update(bytes).digest("hex");
}

function writeInvalidMessageTypeFixture() {
  const builder = new flatbuffers.Builder(64);
  ModuleDeliveryMessage.startModuleDeliveryMessage(builder);
  ModuleDeliveryMessage.addSchemaVersion(builder, 1);
  ModuleDeliveryMessage.addMessageType(builder, 127);
  const root = ModuleDeliveryMessage.endModuleDeliveryMessage(builder);
  ModuleDeliveryMessage.finishModuleDeliveryMessageBuffer(builder, root);
  return builder.asUint8Array();
}

function assertRoundTrip(name, encoded, decoder, expectedReqId) {
  const decoded = decoder(encoded);
  if (expectedReqId && decoded.reqId !== expectedReqId) {
    throw new Error(`${name} reqId mismatch`);
  }
}

async function main() {
  await fs.rm(fixturesDir, { recursive: true, force: true });
  await fs.mkdir(fixturesDir, { recursive: true });

  const grantRequestPayload = {
    schemaVersion: 1,
    reqId: "req-module-delivery-001",
    moduleId: "com.example.echo-module",
    moduleVersion: "1.2.3",
    moduleVariant: "darwin-arm64",
    requesterPeerId: "12D3KooWReqNode123456789ABCDEFGHJKLMNPQRSTUV",
    requesterXpub: "xpub6CUGRUonZSQ4TWtTMmzXdrXDtypWKiKpT2R3N4bKp9t6r5GaJ8V4aHcRUW2YCiMzFxLpyR4UxF3RJTUs63HgNbUsVTNff7kwh28ykVfoCEN",
    requesterDomain: "app.example.com",
    requesterSigningPublicKey: hexToBytes(
      "11aa22bb33cc44dd55ee66ff77889900aabbccddeeff00112233445566778899",
    ),
    requesterEncryptionPublicKey: hexToBytes(
      "99aa88bb77cc66dd55ee44ff33002211ffeeddccbbaa99887766554433221100",
    ),
    requestedTimeoutMs: 300_000,
    requestedAtMs: 1830000000123,
  };

  const grantChallengePayload = {
    schemaVersion: 1,
    reqId: grantRequestPayload.reqId,
    challenge: hexToBytes(
      "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
    ),
    expiresAtMs: 1830000005123,
    providerPeerId: "12D3KooWProvider123456789ABCDEFGHJKLMNPQRSTUV",
    providerPublicKey: hexToBytes(
      "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd",
    ),
  };

  const grantProofPayload = {
    schemaVersion: 1,
    reqId: grantRequestPayload.reqId,
    moduleId: grantRequestPayload.moduleId,
    moduleVersion: grantRequestPayload.moduleVersion,
    requesterPeerId: grantRequestPayload.requesterPeerId,
    requesterDomain: grantRequestPayload.requesterDomain,
    requesterSigningPublicKey: grantRequestPayload.requesterSigningPublicKey,
    requesterEncryptionPublicKey: grantRequestPayload.requesterEncryptionPublicKey,
    requestedTimeoutMs: grantRequestPayload.requestedTimeoutMs,
    challenge: grantChallengePayload.challenge,
    signature: hexToBytes(
      "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeffffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100",
    ),
    provedAtMs: 1830000001123,
  };

  const bundleDescriptor = {
    schemaVersion: 1,
    contentCid: "bafybeigdyrzt5sfp7udm7hu76p4rkymz3x4xj4h6f6h7l6t5m5b5o5l5au",
    contentHash: hexToBytes(
      "7f6e5d4c3b2a19081716151413121110ffeeddccbbaa99887766554433221100",
    ),
    sizeBytes: 4096,
    moduleId: grantRequestPayload.moduleId,
    moduleVersion: grantRequestPayload.moduleVersion,
    runtime: "wasi_snapshot_preview1",
    abi: "sdn-module/1",
    entrypoint: "plugin.wasm",
    publicationCid: "bafybeicx4v76y7m4wsez4m6dru3z2e4q5mkn4zt6g44jdfq2a3n4r3u3fa",
    contentCodec: "application/wasm",
    encryptionCodec: "xchacha20poly1305",
  };

  const wrappedContentKey = {
    schemaVersion: 1,
    wrappingAlgorithm: "x25519-xsalsa20-poly1305",
    recipientKeyId: "requester-key-01",
    recipientPublicKey: grantRequestPayload.requesterEncryptionPublicKey,
    ephemeralPublicKey: hexToBytes(
      "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
    ),
    nonce: hexToBytes("00112233445566778899aabbccddeeff0011223344556677"),
    ciphertext: hexToBytes(
      "deafbeef00112233445566778899aabbccddeeff00112233445566778899aabb",
    ),
    tag: hexToBytes("11223344556677889900aabbccddeeff"),
  };

  const grantResponsePayload = {
    schemaVersion: 1,
    reqId: grantRequestPayload.reqId,
    entitlementStatus: "active",
    capabilityToken: "grant-token-v1",
    expiresAtMs: 1830003600123,
    grantedDomain: grantRequestPayload.requesterDomain,
    grantedTimeoutMs: grantRequestPayload.requestedTimeoutMs,
    grantSignature: hexToBytes(
      "f0e1d2c3b4a5968778695a4b3c2d1e0ff0e1d2c3b4a5968778695a4b3c2d1e0f0011aa22bb33cc44dd55ee66ff7788990011aa22bb33cc44dd55ee66ff778899",
    ),
    grantVerifierPublicKey: hexToBytes(
      "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd",
    ),
    bundleDescriptor,
    wrappedContentKey,
  };

  const errorResponsePayload = {
    schemaVersion: 1,
    reqId: grantRequestPayload.reqId,
    code: "grant_denied",
    message: "module delivery policy denied the request",
    retryable: false,
  };

  const fixtures = [
    {
      name: "grant_request",
      encoded: encodeGrantRequest(grantRequestPayload),
      decode: decodeGrantRequest,
      expectedReqId: grantRequestPayload.reqId,
    },
    {
      name: "grant_challenge",
      encoded: encodeGrantChallenge(grantChallengePayload),
      decode: decodeGrantChallenge,
      expectedReqId: grantRequestPayload.reqId,
    },
    {
      name: "grant_proof",
      encoded: encodeGrantProof(grantProofPayload),
      decode: decodeGrantProof,
      expectedReqId: grantRequestPayload.reqId,
    },
    {
      name: "grant_response",
      encoded: encodeGrantResponse(grantResponsePayload),
      decode: decodeGrantResponse,
      expectedReqId: grantRequestPayload.reqId,
    },
    {
      name: "error_response",
      encoded: encodeErrorResponse(errorResponsePayload),
      decode: decodeErrorResponse,
      expectedReqId: grantRequestPayload.reqId,
    },
  ];

  const manifest = {
    schemaVersion: 1,
    generatedAt,
    fixtures: [],
  };

  for (const fixture of fixtures) {
    assertRoundTrip(
      fixture.name,
      fixture.encoded,
      fixture.decode,
      fixture.expectedReqId,
    );
    await fs.writeFile(
      path.join(fixturesDir, `${fixture.name}.hex`),
      `${bytesToHex(fixture.encoded)}\n`,
      "utf8",
    );
    manifest.fixtures.push({
      name: fixture.name,
      file: `${fixture.name}.hex`,
      bytes: fixture.encoded.length,
      sha256: sha256(fixture.encoded),
    });
  }

  const envelope = encodeModuleDeliveryMessage({
    type: "grant_response",
    payload: grantResponsePayload,
  });
  const decodedEnvelope = decodeModuleDeliveryMessage(envelope);
  if (decodedEnvelope.type !== "grant_response") {
    throw new Error("module_delivery_message_grant_response type mismatch");
  }
  await fs.writeFile(
    path.join(fixturesDir, "module_delivery_message_grant_response.hex"),
    `${bytesToHex(envelope)}\n`,
    "utf8",
  );
  manifest.fixtures.push({
    name: "module_delivery_message_grant_response",
    file: "module_delivery_message_grant_response.hex",
    bytes: envelope.length,
    sha256: sha256(envelope),
  });

  const invalidGrantRequest = new Uint8Array(fixtures[0].encoded);
  invalidGrantRequest[7] = 0xff;
  await fs.writeFile(
    path.join(fixturesDir, "grant_request_invalid_identifier.hex"),
    `${bytesToHex(invalidGrantRequest)}\n`,
    "utf8",
  );
  manifest.fixtures.push({
    name: "grant_request_invalid_identifier",
    file: "grant_request_invalid_identifier.hex",
    bytes: invalidGrantRequest.length,
    sha256: sha256(invalidGrantRequest),
    expectedFailure: "invalid grant request identifier",
  });

  const invalidMessage = writeInvalidMessageTypeFixture();
  await fs.writeFile(
    path.join(fixturesDir, "module_delivery_message_invalid_type.hex"),
    `${bytesToHex(invalidMessage)}\n`,
    "utf8",
  );
  manifest.fixtures.push({
    name: "module_delivery_message_invalid_type",
    file: "module_delivery_message_invalid_type.hex",
    bytes: invalidMessage.length,
    sha256: sha256(invalidMessage),
    expectedFailure: "unsupported module-delivery message type: 127",
  });

  await fs.writeFile(
    path.join(fixturesDir, "fixture-manifest.json"),
    `${JSON.stringify(manifest, null, 2)}\n`,
    "utf8",
  );

  console.log(
    `Generated ${manifest.fixtures.length} module-delivery fixtures -> ${path.relative(packageRoot, fixturesDir)}`,
  );
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
