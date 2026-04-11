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

import {
  encryptBundleBytes,
  seedOrbproModuleCatalog,
} from "../../scripts/seed-orbpro-module-catalog.mjs";

const PASS = "\x1b[32mPASS\x1b[0m";
const FAIL = "\x1b[31mFAIL\x1b[0m";

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

await test("encryptBundleBytes returns decryptable AES-256-GCM payload bytes", async () => {
  const plaintext = new Uint8Array([
    0x00,
    0x61,
    0x73,
    0x6d,
    0x01,
    0x00,
    0x00,
    0x00,
    ...crypto.randomBytes(64),
  ]);
  const contentKey = crypto.randomBytes(32);

  const encrypted = encryptBundleBytes(plaintext, contentKey);

  assert.equal(encrypted.length, plaintext.length + 12 + 16);
  const iv = encrypted.subarray(0, 12);
  const ciphertext = encrypted.subarray(12, encrypted.length - 16);
  const tag = encrypted.subarray(encrypted.length - 16);
  const decipher = crypto.createDecipheriv("aes-256-gcm", contentKey, iv);
  decipher.setAuthTag(tag);
  const decrypted = Buffer.concat([
    decipher.update(ciphertext),
    decipher.final(),
  ]);

  assert.deepEqual(new Uint8Array(decrypted), plaintext);
});

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
  const wasmBytes = new Uint8Array([
    0x00,
    0x61,
    0x73,
    0x6d,
    0x01,
    0x00,
    0x00,
    0x00,
    ...crypto.randomBytes(48),
  ]);
  await fs.writeFile(wasmPath, wasmBytes);

  const summary = await seedOrbproModuleCatalog({
    pluginRoot,
    modules: [
      {
        slug: "fastest-path",
        moduleId: "com.orbpro.fastest-path",
        version: "local-dev",
        wasmPath,
      },
    ],
  });

  assert.equal(summary.seeded.length, 1);
  assert.equal(summary.seeded[0].moduleId, "com.orbpro.fastest-path");
  assert.equal(summary.seeded[0].contentKeyHex.length, 64);

  const encryptedPath = path.join(pluginRoot, "fastest-path.wasm.enc");
  const keyPath = path.join(pluginRoot, "fastest-path.key");
  const [encryptedBytes, keyHex, catalogRaw] = await Promise.all([
    fs.readFile(encryptedPath),
    fs.readFile(keyPath, "utf8"),
    fs.readFile(path.join(pluginRoot, "catalog.json"), "utf8"),
  ]);
  const catalog = JSON.parse(catalogRaw);

  assert.ok(encryptedBytes.length > wasmBytes.length);
  assert.equal(keyHex.trim().length, 64);
  assert.equal(catalog.plugins.length, 2);
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

console.log("\nDone.");
