import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  MODULE_DELIVERY_PROTOCOL_ID,
  MODULE_DELIVERY_MESSAGE_TYPES,
  decodeModuleDeliveryMessage,
  encodeModuleDeliveryMessage,
} from "../../src/index.js";
import {
  decodeErrorResponse,
  decodeGrantChallenge,
  decodeGrantProof,
  decodeGrantRequest,
  decodeGrantResponse,
} from "../../src/module-delivery-codec.js";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, "../..");
const fixturesDir = path.join(packageRoot, "fixtures/module-delivery/v1");

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
  const fixturePath = path.join(fixturesDir, `${name}.hex`);
  return hexToBytes(await fs.readFile(fixturePath, "utf8"));
}

export async function runModuleDeliveryConformance() {
  const publicApi = await import("../../src/index.js");

  assert.equal(
    MODULE_DELIVERY_PROTOCOL_ID,
    "/space-data-network/module-delivery/1.0.0",
    "module-delivery protocol id mismatch",
  );
  assert.equal(
    MODULE_DELIVERY_MESSAGE_TYPES.GRANT_RESPONSE,
    "grant_response",
    "grant response message type mismatch",
  );
  assert.ok(
    !("KEY_BROKER_PROTOCOL_ID" in publicApi),
    "module-sdk root export must not expose legacy key-broker protocol ids",
  );
  assert.ok(
    !("PUBLIC_KEY_PROTOCOL_ID" in publicApi),
    "module-sdk root export must not expose legacy public-key protocol ids",
  );
  assert.ok(
    !("decodeKeyBrokerResponse" in publicApi),
    "module-sdk root export must not expose legacy key-broker codecs",
  );
  assert.ok(
    !("encodeThirdPartyClientLicenseRequest" in publicApi),
    "module-sdk root export must not expose legacy third-party codecs",
  );

  const manifest = JSON.parse(
    await fs.readFile(path.join(fixturesDir, "fixture-manifest.json"), "utf8"),
  );
  const fixtureNames = manifest.fixtures.map((fixture) => fixture.name);
  assert.deepEqual(
    fixtureNames,
    [
      "grant_request",
      "grant_challenge",
      "grant_proof",
      "grant_response",
      "error_response",
      "module_delivery_message_grant_response",
      "grant_request_invalid_identifier",
      "module_delivery_message_invalid_type",
    ],
    "fixture manifest should describe the full module-delivery conformance set",
  );

  const grantRequestBytes = await readFixture("grant_request");
  const grantChallengeBytes = await readFixture("grant_challenge");
  const grantProofBytes = await readFixture("grant_proof");
  const grantResponseBytes = await readFixture("grant_response");
  const errorResponseBytes = await readFixture("error_response");
  const envelopeBytes = await readFixture("module_delivery_message_grant_response");
  const invalidGrantRequestBytes = await readFixture("grant_request_invalid_identifier");
  const invalidMessageBytes = await readFixture("module_delivery_message_invalid_type");

  const grantRequest = decodeGrantRequest(grantRequestBytes);
  const grantChallenge = decodeGrantChallenge(grantChallengeBytes);
  const grantProof = decodeGrantProof(grantProofBytes);
  const grantResponse = decodeGrantResponse(grantResponseBytes);
  const errorResponse = decodeErrorResponse(errorResponseBytes);

  assert.equal(grantRequest.moduleId, "com.example.echo-module");
  assert.equal(grantRequest.requesterDomain, "app.example.com");
  assert.equal(grantRequest.requestedTimeoutMs, 300_000);
  assert.equal(grantChallenge.reqId, grantRequest.reqId);
  assert.equal(grantProof.reqId, grantRequest.reqId);
  assert.equal(grantProof.requesterDomain, grantRequest.requesterDomain);
  assert.equal(grantResponse.reqId, grantRequest.reqId);
  assert.equal(errorResponse.code, "grant_denied");
  assert.equal(grantResponse.bundleDescriptor.moduleId, grantRequest.moduleId);
  assert.ok(grantResponse.bundleDescriptor.cid.startsWith("bafy"));
  assert.equal(grantResponse.grantedDomain, grantRequest.requesterDomain);
  assert.equal(grantResponse.grantedTimeoutMs, grantRequest.requestedTimeoutMs);
  assert.equal(grantResponse.grantVerifierPublicKey.length, 32);

  const envelope = decodeModuleDeliveryMessage(
    encodeModuleDeliveryMessage({
      type: "grant_response",
      payload: grantResponse,
    }),
  );
  assert.equal(envelope.type, "grant_response");
  assert.equal(envelope.payload.reqId, grantRequest.reqId);
  assert.equal(
    decodeModuleDeliveryMessage(envelopeBytes).payload.bundleDescriptor.cid,
    grantResponse.bundleDescriptor.cid,
  );

  assert.throws(
    () => decodeGrantRequest(invalidGrantRequestBytes),
    /invalid grant request identifier/,
  );
  assert.throws(
    () => decodeModuleDeliveryMessage(invalidMessageBytes),
    /unsupported module-delivery message type/,
  );

  const clientResult = spawnSync(
    process.execPath,
    [
      path.join(packageRoot, "scripts/module-delivery-test-client.mjs"),
      "--fixture",
      "grant_response",
    ],
    {
      cwd: packageRoot,
      encoding: "utf8",
    },
  );
  assert.equal(
    clientResult.status,
    0,
    `module-delivery test client failed: ${clientResult.stderr}`,
  );
  const clientPayload = JSON.parse(clientResult.stdout);
  assert.equal(clientPayload.protocolId, MODULE_DELIVERY_PROTOCOL_ID);
  assert.equal(clientPayload.type, "grant_response");

  return {
    ok: true,
    suite: "module-delivery-conformance",
  };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  runModuleDeliveryConformance()
    .then((result) => {
      console.log(JSON.stringify(result, null, 2));
    })
    .catch((error) => {
      console.error(`[module-delivery conformance] ${error.message}`);
      process.exit(1);
    });
}
