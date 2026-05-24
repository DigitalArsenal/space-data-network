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
import {
  decryptProtectedBytes,
  extractPublicationRecordCollection,
} from "space-data-module-sdk/transport";

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
            key_path: "existing.key",
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
  });

  assert.equal(summary.seeded.length, 1);
  assert.equal(summary.seeded[0].moduleId, "com.orbpro.fastest-path");
  assert.equal(summary.seeded[0].contentKeyHex.length, 64);
  assert.equal(summary.seeded[0].hasMbl, true);
  assert.equal(summary.seeded[0].hasEnc, true);
  assert.equal(summary.seeded[0].hasPnm, true);

  const encryptedPath = path.join(pluginRoot, "fastest-path.wasm.enc");
  const keyPath = path.join(pluginRoot, "fastest-path.key");
  const [encryptedBytes, keyHex, catalogRaw] = await Promise.all([
    fs.readFile(encryptedPath),
    fs.readFile(keyPath, "utf8"),
    fs.readFile(path.join(pluginRoot, "catalog.json"), "utf8"),
  ]);
  const catalog = JSON.parse(catalogRaw);
  const publication = extractPublicationRecordCollection(encryptedBytes);

  assert.ok(encryptedBytes.length > wasmBytes.length);
  assert.equal(keyHex.trim().length, 64);
  assert.equal(catalog.plugins.length, 2);
  assert.equal(Boolean(publication?.mbl), true);
  assert.equal(Boolean(publication?.enc), true);
  assert.equal(Boolean(publication?.pnm), true);
  const decrypted = await decryptProtectedBytes({
    protectedBytes: new Uint8Array(encryptedBytes),
    recipientPrivateKey: Uint8Array.from(Buffer.from(keyHex.trim(), "hex")),
  });
  assert.deepEqual(decrypted, wasmBytes);
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
    encrypted_path: "fastest-path.wasm.enc",
    key_path: "fastest-path.key",
    content_type: "application/wasm+encrypted",
    cache_control: "public, max-age=300, s-maxage=3600, stale-while-revalidate=86400",
  });
});

await test("seedOrbproModuleCatalog uses the shipped plugin version for built-in modules by default", async () => {
  const tempRoot = await fs.mkdtemp(
    path.join(os.tmpdir(), "sdn-seed-orbpro-builtins-"),
  );
  const pluginRoot = path.join(tempRoot, "license", "plugins");
  await fs.mkdir(pluginRoot, { recursive: true });
  const builtInModule = DEFAULT_ORBPRO_MODULES.find(
    (entry) => entry.moduleId === "com.orbpro.access",
  );
  assert.ok(builtInModule);

  const summary = await seedOrbproModuleCatalog({
    pluginRoot,
    modules: [builtInModule],
  });

  assert.equal(summary.seeded.length, 1);
  assert.equal(summary.seeded[0].moduleId, "com.orbpro.access");

  const catalog = JSON.parse(
    await fs.readFile(path.join(pluginRoot, "catalog.json"), "utf8"),
  );
  const manifest = JSON.parse(
    await fs.readFile(
      path.resolve(
        repoRoot,
        "..",
        "OrbPro/packages/space-data-network-modules/analysis/access/plugin-manifest.json",
      ),
      "utf8",
    ),
  );
  const seededEntry = catalog.plugins.find(
    (entry) => entry.id === "com.orbpro.access",
  );

  assert.equal(seededEntry?.version, manifest.version);
});

await test("DEFAULT_ORBPRO_MODULES includes the licensing runtime", async () => {
  assert.equal(
    DEFAULT_ORBPRO_MODULES.some((entry) => entry.moduleId === "licensing"),
    true,
  );
});

await test("DEFAULT_ORBPRO_MODULES includes the protected wasm-engine runtime artifact", async () => {
  const wasmEngineRuntime = DEFAULT_ORBPRO_MODULES.find(
    (entry) => entry.slug === "wasm-engine",
  );

  assert.equal(
    DEFAULT_ORBPRO_MODULES.some(
      (entry) =>
        entry.slug === "wasm-engine-sdk" ||
        entry.moduleId === "com.orbpro.wasm-engine-sdk",
    ),
    false,
  );
  assert.equal(
    wasmEngineRuntime?.protectedModulePath,
    "packages/wasm-engine/dist/wasm-engine-sdn-encrypted.js",
  );
  assert.equal(wasmEngineRuntime?.protectedExports?.[0]?.exportName, "encryptedData");
});

await test("seedOrbproModuleCatalog resolves relative artifacts from ORBPRO_ROOT first", async () => {
  const tempRoot = await fs.mkdtemp(
    path.join(os.tmpdir(), "sdn-seed-orbpro-root-"),
  );
  const pluginRoot = path.join(tempRoot, "license", "plugins");
  const orbproRoot = path.join(tempRoot, "active-orbpro");
  const fixtureModule = path.join(
    orbproRoot,
    "packages",
    "fixture",
    "dist",
    "fixture-encrypted.js",
  );
  await fs.mkdir(path.dirname(fixtureModule), { recursive: true });
  await fs.mkdir(pluginRoot, { recursive: true });
  await fs.writeFile(
    fixtureModule,
    [
      `export const encryptedData = ${JSON.stringify(
        Buffer.from("fixture-from-active-orbpro-root").toString("base64"),
      )};`,
      'export const recipientPrivateKeyHex = "00".repeat(32);',
      "",
    ].join("\n"),
  );

  const previousOrbproRoot = process.env.ORBPRO_ROOT;
  process.env.ORBPRO_ROOT = orbproRoot;
  try {
    const summary = await seedOrbproModuleCatalog({
      pluginRoot,
      modules: [
        {
          slug: "fixture-root",
          moduleId: "com.orbpro.fixture-root",
          version: "root-test",
          protectedModulePath: "packages/fixture/dist/fixture-encrypted.js",
          protectedExports: [
            { exportName: "encryptedData", slug: "fixture-root" },
          ],
          keyExport: "recipientPrivateKeyHex",
        },
      ],
    });

    assert.equal(summary.seeded.length, 1);
    assert.equal(summary.seeded[0].protectedModulePath, fixtureModule);
    assert.equal(
      (await fs.readFile(path.join(pluginRoot, "fixture-root.wasm.enc"))).toString(
        "utf8",
      ),
      "fixture-from-active-orbpro-root",
    );
  } finally {
    if (previousOrbproRoot === undefined) {
      delete process.env.ORBPRO_ROOT;
    } else {
      process.env.ORBPRO_ROOT = previousOrbproRoot;
    }
  }
});

await test("seedOrbproModuleCatalog removes the stale wasm-engine-sdk catalog entry", async () => {
  const tempRoot = await fs.mkdtemp(
    path.join(os.tmpdir(), "sdn-seed-orbpro-stale-wasm-engine-sdk-"),
  );
  const pluginRoot = path.join(tempRoot, "license", "plugins");
  await fs.mkdir(pluginRoot, { recursive: true });

  await fs.writeFile(
    path.join(pluginRoot, "catalog.json"),
    JSON.stringify(
      {
        plugins: [
          {
            id: "com.orbpro.wasm-engine-sdk",
            version: "1.0.0",
            encrypted_path: "wasm-engine-sdk.wasm.enc",
            key_path: "wasm-engine-sdk.key",
            content_type: "application/wasm+encrypted",
          },
          {
            id: "existing.module",
            version: "9.9.9",
            encrypted_path: "existing.wasm.enc",
            key_path: "existing.key",
            content_type: "application/wasm+encrypted",
          },
        ],
      },
      null,
      2,
    ),
  );

  await seedOrbproModuleCatalog({
    pluginRoot,
    modules: [],
  });

  const catalog = JSON.parse(
    await fs.readFile(path.join(pluginRoot, "catalog.json"), "utf8"),
  );
  assert.deepEqual(
    catalog.plugins.map((entry) => entry.id),
    ["existing.module"],
  );
});

await test("OPTIONAL_ORBPRO_MODULES pins conjunction to the current SDN module tag", async () => {
  const conjunctionModule = OPTIONAL_ORBPRO_MODULES.find(
    (entry) => entry.slug === "conjunction-assessment",
  );
  assert.equal(conjunctionModule?.version, "0.1.0-0.8.2");
});

console.log("\nDone.");

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
