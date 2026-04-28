/**
 * Local OrbPro module catalog seeding tests.
 *
 * Run: node tests/isomorphic/seed-orbpro-module-catalog.test.mjs
 */

import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { extractPublicationRecordCollection } from "space-data-module-sdk/transport";

import {
  DEFAULT_ORBPRO_MODULES,
  OPTIONAL_ORBPRO_MODULES,
  seedOrbproModuleCatalog,
} from "../../scripts/seed-orbpro-module-catalog.mjs";

const PASS = "\x1b[32mPASS\x1b[0m";
const FAIL = "\x1b[31mFAIL\x1b[0m";
const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "..", "..");
const workspaceRoot = path.resolve(repoRoot, "..", "..", "..");
const X25519_PKCS8_PREFIX = Buffer.from(
  "302e020100300506032b656e04220420",
  "hex",
);

const localProviderPeerID = "12D3KooWLocalProviderForSeedTests";
const localProviderX25519PubKey = deriveX25519PublicKeyBase64url(
  Buffer.alloc(32, 0x42),
);

async function test(name, fn) {
  try {
    await fn();
    console.log(`${PASS}: ${name}`);
  } catch (error) {
    console.error(`${FAIL}: ${name}`);
    console.error(error);
    process.exitCode = 1;
  }
}

await test("seedOrbproModuleCatalog upserts requested modules and preserves unrelated entries", async () => {
  const tempRoot = await fs.mkdtemp(
    path.join(os.tmpdir(), "sdn-seed-orbpro-module-catalog-"),
  );
  const pluginRoot = path.join(tempRoot, "license", "plugins");
  await fs.mkdir(pluginRoot, { recursive: true });

  await fs.writeFile(
    path.join(pluginRoot, "catalog.json"),
    JSON.stringify(
      {
        plugins: [
          {
            id: "existing.module",
            version: "9.9.9",
            encrypted_path: "existing.wasm.enc",
            key_envelope_path: "existing.key-envelope.json",
            content_type: "application/wasm+encrypted",
          },
        ],
      },
      null,
      2,
    ),
  );

  const wasmPath = path.join(tempRoot, "fastest-path.module.wasm");
  const manifestPath = path.join(tempRoot, "fastest-path.plugin-manifest.json");
  const wasmBytes = new Uint8Array([
    0x00,
    0x61,
    0x73,
    0x6d,
    0x01,
    0x00,
    0x00,
    0x00,
  ]);
  await fs.writeFile(wasmPath, wasmBytes);
  await fs.writeFile(
    manifestPath,
    JSON.stringify(createTestManifest("com.orbpro.fastest-path", "local-dev"), null, 2),
  );

  const summary = await seedOrbproModuleCatalog({
    pluginRoot,
    modules: [
      {
        slug: "fastest-path",
        moduleId: "com.orbpro.fastest-path",
        version: "local-dev",
        wasmPath,
        manifestPath,
      },
    ],
    providerX25519PubKey: localProviderX25519PubKey,
    providerPeerID: localProviderPeerID,
  });

  assert.equal(summary.seeded.length, 1);
  assert.equal(summary.seeded[0].moduleId, "com.orbpro.fastest-path");
  assert.match(summary.seeded[0].keyEnvelopePath, /bundle\.key-envelope\.json$/);
  assert.match(summary.seeded[0].bundleSHA256, /^[0-9a-f]{64}$/);
  assert.equal(summary.seeded[0].hasMbl, true);
  assert.equal(summary.seeded[0].hasEnc, true);
  assert.equal(summary.seeded[0].hasPnm, true);

  const encryptedPath = path.join(
    pluginRoot,
    "com.orbpro.fastest-path",
    "bundle.wasm.enc",
  );
  const keyEnvelopePath = path.join(
    pluginRoot,
    "com.orbpro.fastest-path",
    "bundle.key-envelope.json",
  );
  const [encryptedBytes, catalogRaw] = await Promise.all([
    fs.readFile(encryptedPath),
    fs.readFile(path.join(pluginRoot, "catalog.json"), "utf8"),
  ]);
  const catalog = JSON.parse(catalogRaw);
  const publication = extractPublicationRecordCollection(encryptedBytes);

  assert.ok(encryptedBytes.length > wasmBytes.length);
  assert.equal(catalog.plugins.length, 2);
  assert.equal(Boolean(publication?.mbl), true);
  assert.equal(Boolean(publication?.enc), true);
  assert.equal(Boolean(publication?.pnm), true);
  await assert.doesNotReject(fs.access(keyEnvelopePath));
  await assert.rejects(fs.access(path.join(pluginRoot, "fastest-path.key")));
  await assert.rejects(
    fs.access(path.join(pluginRoot, "com.orbpro.fastest-path", "bundle.key")),
  );
  assert.doesNotMatch(catalogRaw, /content_key_hex|"key_path"|bundle\.key"/u);
  assert.deepEqual(
    catalog.plugins.map((entry) => entry.id).sort(),
    ["com.orbpro.fastest-path", "existing.module"],
  );

  const seededEntry = catalog.plugins.find(
    (entry) => entry.id === "com.orbpro.fastest-path",
  );
  assert.deepEqual(seededEntry, {
    id: "com.orbpro.fastest-path",
    version: "local-dev",
    required_scope: "orbpro:base",
    encrypted_path: "com.orbpro.fastest-path/bundle.wasm.enc",
    key_envelope_path: "com.orbpro.fastest-path/bundle.key-envelope.json",
    content_type: "application/wasm+encrypted",
    cache_control: "public, max-age=300, s-maxage=3600, stale-while-revalidate=86400",
    allowed_domains: [
      "localhost",
      "127.0.0.1",
      "spaceaware.io",
      "www.spaceaware.io",
      "digitalarsenal.io",
      "www.digitalarsenal.io",
    ],
    signer_pubkey_hex: "6c".repeat(32),
  });
});

await test("local OrbPro seeding emits provider-wrapped key envelopes", async () => {
  const tempRoot = await fs.mkdtemp(
    path.join(os.tmpdir(), "sdn-seed-orbpro-provider-envelope-"),
  );
  const pluginRoot = path.join(tempRoot, "license", "plugins");
  await fs.mkdir(pluginRoot, { recursive: true });

  const wasmPath = path.join(tempRoot, "fastest-path.module.wasm");
  const manifestPath = path.join(tempRoot, "fastest-path.plugin-manifest.json");
  await fs.writeFile(
    wasmPath,
    new Uint8Array([0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00]),
  );
  await fs.writeFile(
    manifestPath,
    JSON.stringify(createTestManifest("com.orbpro.fastest-path", "local-dev"), null, 2),
  );

  const summary = await seedOrbproModuleCatalog({
    pluginRoot,
    modules: [
      {
        slug: "fastest-path",
        moduleId: "com.orbpro.fastest-path",
        version: "local-dev",
        wasmPath,
        manifestPath,
      },
    ],
    providerX25519PubKey: localProviderX25519PubKey,
    providerPeerID: localProviderPeerID,
  });

  const envelope = JSON.parse(
    await fs.readFile(summary.seeded[0].keyEnvelopePath, "utf8"),
  );
  const aad = JSON.parse(Buffer.from(envelope.aad, "base64url").toString("utf8"));

  assert.equal(envelope.version, 1);
  assert.equal(envelope.alg, "X25519-HKDF-SHA256-AES-256-GCM");
  assert.equal(envelope.context, "space-data-network/plugin-module/content-key/v1");
  assert.equal(envelope.provider_x25519_pubkey, localProviderX25519PubKey);
  assert.equal(aad.module_id, "com.orbpro.fastest-path");
  assert.equal(aad.version, "local-dev");
  assert.equal(aad.bundle_sha256, summary.seeded[0].bundleSHA256);
  assert.equal(aad.signer_public_key_hex, "6c".repeat(32));
  assert.equal(aad.provider_peer_id, localProviderPeerID);
});

await test("seedOrbproModuleCatalog uses the shipped plugin version for built-in modules by default", async () => {
  const tempRoot = await fs.mkdtemp(
    path.join(os.tmpdir(), "sdn-seed-orbpro-builtins-"),
  );
  const pluginRoot = path.join(tempRoot, "license", "plugins");
  await fs.mkdir(pluginRoot, { recursive: true });

  const summary = await seedOrbproModuleCatalog({
    pluginRoot,
    modules: [
      {
        slug: "sgp4",
        moduleId: "com.orbpro.sgp4",
        wasmPath: "../space-data-network-plugins/packages/sgp4/dist/sgp4.wasm",
      },
    ],
    providerX25519PubKey: localProviderX25519PubKey,
    providerPeerID: localProviderPeerID,
  });

  assert.equal(summary.seeded.length, 1);
  assert.equal(summary.seeded[0].moduleId, "com.orbpro.sgp4");

  const catalog = JSON.parse(
    await fs.readFile(path.join(pluginRoot, "catalog.json"), "utf8"),
  );
  const sgp4Manifest = JSON.parse(
    await fs.readFile(
      path.resolve(
        workspaceRoot,
        "space-data-network-plugins/packages/sgp4/dist/manifest.json",
      ),
      "utf8",
    ),
  );
  const seededEntry = catalog.plugins.find(
    (entry) => entry.id === "com.orbpro.sgp4",
  );

  assert.equal(seededEntry?.version, sgp4Manifest.version);
});

await test("DEFAULT_ORBPRO_MODULES includes the licensing runtime", async () => {
  assert.equal(
    DEFAULT_ORBPRO_MODULES.some((entry) => entry.moduleId === "com.orbpro.licensing"),
    true,
  );
});

await test("OPTIONAL_ORBPRO_MODULES pins conjunction to the current SDN module tag", async () => {
  const conjunctionModule = OPTIONAL_ORBPRO_MODULES.find(
    (entry) => entry.slug === "conjunction-assessment",
  );
  assert.equal(conjunctionModule?.version, "0.1.0-0.8.2");
});

console.log("\nDone.");

function deriveX25519PublicKeyBase64url(rawPrivateKey) {
  const privateKey = crypto.createPrivateKey({
    key: Buffer.concat([X25519_PKCS8_PREFIX, rawPrivateKey]),
    format: "der",
    type: "pkcs8",
  });
  const publicKey = crypto.createPublicKey(privateKey);
  const publicDer = publicKey.export({ format: "der", type: "spki" });
  return Buffer.from(publicDer.subarray(-32)).toString("base64url");
}

function createTestManifest(pluginId, version) {
  return {
    pluginId,
    name: pluginId,
    version,
    pluginFamily: "analysis",
    capabilities: [],
    externalInterfaces: [],
    runtimeTargets: ["browser", "wasmedge"],
    invokeSurfaces: ["command"],
    methods: [
      {
        methodId: "echo",
        displayName: "echo",
        inputPorts: [
          {
            portId: "request",
            acceptedTypeSets: [
              {
                setId: "request-any",
                allowedTypes: [{ acceptsAnyFlatbuffer: true }],
              },
            ],
            minStreams: 1,
            maxStreams: 1,
            required: true,
          },
        ],
        outputPorts: [
          {
            portId: "response",
            acceptedTypeSets: [
              {
                setId: "response-any",
                allowedTypes: [{ acceptsAnyFlatbuffer: true }],
              },
            ],
            minStreams: 0,
            maxStreams: 1,
            required: false,
          },
        ],
        maxBatch: 1,
        drainPolicy: "single-shot",
      },
    ],
  };
}
