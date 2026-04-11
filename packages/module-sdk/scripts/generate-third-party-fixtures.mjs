#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  decodeThirdPartyClientLicenseRequest,
  decodeThirdPartyClientLicenseResponse,
  decodeThirdPartyServerPluginGrant,
  decodeThirdPartyServerPluginRegistration,
  encodeThirdPartyClientLicenseRequest,
  encodeThirdPartyClientLicenseResponse,
  encodeThirdPartyServerPluginGrant,
  encodeThirdPartyServerPluginRegistration,
} from "../src/third-party-codec.js";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, "..");
const fixturesDir = path.join(packageRoot, "fixtures/third-party/v1");

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
  return [...bytes].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function assertRoundTrip(name, encoded, decoder, expected) {
  const decoded = decoder(encoded);
  if (expected.pluginId && decoded.pluginId !== expected.pluginId) {
    throw new Error(`${name} pluginId mismatch`);
  }
  if (expected.pluginVersion && decoded.pluginVersion !== expected.pluginVersion) {
    throw new Error(`${name} pluginVersion mismatch`);
  }
  if (expected.vendorId && decoded.vendorId !== expected.vendorId) {
    throw new Error(`${name} vendorId mismatch`);
  }
  if (expected.status !== undefined && decoded.status !== expected.status) {
    throw new Error(`${name} status mismatch`);
  }
}

async function main() {
  await fs.mkdir(fixturesDir, { recursive: true });

  const clientRequestPayload = {
    schemaVersion: 1,
    pluginId: "com.example.echo-client",
    pluginVersion: "1.2.3",
    accountIdHash: hexToBytes(
      "8f9d6f9dd5f44b969531b4ef5fdf6f18f5e6558cfbd19f7117fd2dc0138f8f8a",
    ),
    requestNonce: hexToBytes("00112233445566778899aabbccddeeff"),
    ephemeralPublicKey: hexToBytes(
      "04aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff66778899",
    ),
    challengeToken: "challenge-token-v1",
  };

  const clientResponsePayload = {
    status: 0,
    keyVersion: 17,
    expiresAtMs: 1830000000000,
    wrappedKey: hexToBytes(
      "9a9a9a9a111122223333444455556666777788889999aaaabbbbccccddddeeee",
    ),
    challengeId: hexToBytes("11111111222222223333333344444444"),
  };

  const serverRegistrationPayload = {
    schemaVersion: 1,
    pluginId: "com.example.echo-server",
    pluginVersion: "3.4.5",
    vendorId: "example-vendor",
    signingPublicKey: hexToBytes(
      "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899",
    ),
    capabilities: ["license.issue", "license.audit"],
    manifestHash: hexToBytes(
      "c3c3c3c3111122223333444455556666777788889999aaaabbbbccccddddeeee",
    ),
  };

  const serverGrantPayload = {
    status: 0,
    grantId: "grant-001",
    issuedAtMs: 1820000000000,
    expiresAtMs: 1820003600000,
    allowedAccounts: ["acct-001", "acct-002"],
    policyHash: hexToBytes(
      "77aa77aa11bb11bb22cc22cc33dd33dd44ee44ee55ff55ff66aa66aa77bb77bb",
    ),
  };

  const fixtures = [
    {
      name: "third_party_client_license_request",
      encoded: encodeThirdPartyClientLicenseRequest(clientRequestPayload),
      decode: decodeThirdPartyClientLicenseRequest,
      expected: clientRequestPayload,
    },
    {
      name: "third_party_client_license_response",
      encoded: encodeThirdPartyClientLicenseResponse(clientResponsePayload),
      decode: decodeThirdPartyClientLicenseResponse,
      expected: clientResponsePayload,
    },
    {
      name: "third_party_server_plugin_registration",
      encoded: encodeThirdPartyServerPluginRegistration(serverRegistrationPayload),
      decode: decodeThirdPartyServerPluginRegistration,
      expected: serverRegistrationPayload,
    },
    {
      name: "third_party_server_plugin_grant",
      encoded: encodeThirdPartyServerPluginGrant(serverGrantPayload),
      decode: decodeThirdPartyServerPluginGrant,
      expected: serverGrantPayload,
    },
  ];

  const manifest = {
    schemaVersion: 1,
    generatedAt: new Date().toISOString(),
    fixtures: [],
  };

  for (const fixture of fixtures) {
    assertRoundTrip(fixture.name, fixture.encoded, fixture.decode, fixture.expected);
    const hex = bytesToHex(fixture.encoded);
    await fs.writeFile(path.join(fixturesDir, `${fixture.name}.hex`), `${hex}\n`, "utf8");
    manifest.fixtures.push({
      name: fixture.name,
      file: `${fixture.name}.hex`,
      bytes: fixture.encoded.length,
      sha256: await cryptoHash(fixture.encoded),
    });
  }

  const corruptFixture = new Uint8Array(fixtures[0].encoded);
  corruptFixture[7] = 0xff;
  const corruptHex = bytesToHex(corruptFixture);
  const corruptName = "third_party_client_license_request_invalid_identifier";
  await fs.writeFile(path.join(fixturesDir, `${corruptName}.hex`), `${corruptHex}\n`, "utf8");
  manifest.fixtures.push({
    name: corruptName,
    file: `${corruptName}.hex`,
    bytes: corruptFixture.length,
    sha256: await cryptoHash(corruptFixture),
    expectedFailure: "invalid third-party client license request identifier",
  });

  await fs.writeFile(
    path.join(fixturesDir, "fixture-manifest.json"),
    `${JSON.stringify(manifest, null, 2)}\n`,
    "utf8",
  );

  console.log(
    `Generated ${manifest.fixtures.length} third-party fixtures -> ${path.relative(packageRoot, fixturesDir)}`,
  );
}

async function cryptoHash(bytes) {
  const { createHash } = await import("node:crypto");
  return createHash("sha256").update(bytes).digest("hex");
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
