import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import {
  decodeThirdPartyClientLicenseRequest,
  decodeThirdPartyClientLicenseResponse,
} from "../../src/third-party-codec.js";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, "../..");

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

async function runFixtureValidation() {
  const fixturesDir = path.join(packageRoot, "fixtures/third-party/v1");
  const requestHex = (
    await fs.readFile(
      path.join(fixturesDir, "third_party_client_license_request.hex"),
      "utf8",
    )
  ).trim();
  const responseHex = (
    await fs.readFile(
      path.join(fixturesDir, "third_party_client_license_response.hex"),
      "utf8",
    )
  ).trim();
  const invalidHex = (
    await fs.readFile(
      path.join(
        fixturesDir,
        "third_party_client_license_request_invalid_identifier.hex",
      ),
      "utf8",
    )
  ).trim();

  const request = decodeThirdPartyClientLicenseRequest(hexToBytes(requestHex));
  const response = decodeThirdPartyClientLicenseResponse(hexToBytes(responseHex));

  assert.equal(request.schemaVersion, 1, "schema version mismatch");
  assert.equal(request.pluginId, "com.example.echo-client", "pluginId mismatch");
  assert.equal(response.status, 0, "expected success status");
  assert.ok(response.keyVersion > 0, "key version must be > 0");

  let failed = false;
  try {
    decodeThirdPartyClientLicenseRequest(hexToBytes(invalidHex));
  } catch {
    failed = true;
  }
  assert.equal(failed, true, "invalid identifier fixture must fail");
}

async function runScaffoldValidation() {
  const tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), "sdn-client-scaffold-"));
  const scaffoldScript = path.join(
    packageRoot,
    "scripts/generate-third-party-scaffold.mjs",
  );
  const result = spawnSync(
    process.execPath,
    [
      scaffoldScript,
      "--type",
      "client",
      "--name",
      "Conformance Client",
      "--vendor-id",
      "conformance",
      "--out-dir",
      tempRoot,
    ],
    {
      cwd: packageRoot,
      encoding: "utf8",
    },
  );
  assert.equal(result.status, 0, `scaffold command failed: ${result.stderr}`);

  const outputDir = path.join(tempRoot, "conformance-client");
  const manifestPath = path.join(outputDir, "plugin-manifest.json");
  const manifest = JSON.parse(await fs.readFile(manifestPath, "utf8"));

  assert.equal(manifest.pluginType, "client", "pluginType mismatch");
  assert.ok(manifest.pluginId.includes("conformance"), "pluginId should include vendor id");

  const indexPath = path.join(outputDir, "src/index.js");
  await fs.access(indexPath);
}

export async function runClientPluginConformance() {
  await runFixtureValidation();
  await runScaffoldValidation();
  return {
    ok: true,
    suite: "client-plugin-conformance",
  };
}
