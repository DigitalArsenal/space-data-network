import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import {
  decodeThirdPartyServerPluginGrant,
  decodeThirdPartyServerPluginRegistration,
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
      path.join(fixturesDir, "third_party_server_plugin_registration.hex"),
      "utf8",
    )
  ).trim();
  const responseHex = (
    await fs.readFile(
      path.join(fixturesDir, "third_party_server_plugin_grant.hex"),
      "utf8",
    )
  ).trim();

  const request = decodeThirdPartyServerPluginRegistration(hexToBytes(requestHex));
  const response = decodeThirdPartyServerPluginGrant(hexToBytes(responseHex));

  assert.equal(request.schemaVersion, 1, "schema version mismatch");
  assert.equal(request.pluginId, "com.example.echo-server", "pluginId mismatch");
  assert.equal(response.status, 0, "expected success status");
  assert.ok(response.grantId.length > 0, "grant id expected");
  assert.ok(response.allowedAccounts.length > 0, "allowed accounts expected");
}

async function runScaffoldValidation() {
  const tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), "sdn-server-scaffold-"));
  const scaffoldScript = path.join(
    packageRoot,
    "scripts/generate-third-party-scaffold.mjs",
  );
  const result = spawnSync(
    process.execPath,
    [
      scaffoldScript,
      "--type",
      "server",
      "--name",
      "Conformance Server",
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

  const outputDir = path.join(tempRoot, "conformance-server");
  const manifestPath = path.join(outputDir, "plugin-manifest.json");
  const manifest = JSON.parse(await fs.readFile(manifestPath, "utf8"));

  assert.equal(manifest.pluginType, "server", "pluginType mismatch");
  assert.ok(manifest.capabilities.includes("license.issue"), "expected issue capability");

  const indexPath = path.join(outputDir, "src/index.js");
  await fs.access(indexPath);
}

export async function runServerPluginConformance() {
  await runFixtureValidation();
  await runScaffoldValidation();
  return {
    ok: true,
    suite: "server-plugin-conformance",
  };
}
