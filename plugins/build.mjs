#!/usr/bin/env node
/**
 * Build both SDN WASM plugins using system emcc.
 *
 * Prerequisites:
 *   - emcc in PATH (Emscripten 3.x or 4.x)
 *   - Internet access to fetch Crypto++ 8.9.0 (or set CRYPTOPP_SOURCE_DIR)
 *
 * Usage:
 *   node plugins/build.mjs [plugin-delivery|client-decrypt|all]
 *
 * Outputs:
 *   plugins/plugin-delivery/dist/plugin-delivery.wasm
 *   plugins/client-decrypt/dist/client-decrypt.wasm
 *
 * Environment:
 *   CRYPTOPP_SOURCE_DIR   — local Crypto++ source tree (skips git clone)
 *   SDN_SERVER_PRIVATE_KEY_HEX — 64-char hex X25519 private key for plugin-delivery
 */

import { execSync, spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, "..");
const PLUGINS_DIR = __dirname;
const BUILD_DIR = path.join(PLUGINS_DIR, ".build");

// ── Paths ──────────────────────────────────────────────────────────────────────

const MODULE_SDK_ROOT = path.join(
  REPO_ROOT,
  "packages",
  "module-runner",
  "node_modules",
  "space-data-module-sdk",
);
const MODULE_SDK_SCHEMAS = path.join(MODULE_SDK_ROOT, "schemas");

// Prefer the OrbPro plugin-sdk FlatBuffers headers if available
const ORBPRO_PLUGIN_SDK_INCLUDE = path.resolve(
  __dirname,
  "../../../OrbPro/packages/plugin-sdk/include",
);
const ORBPRO_FLATBUFFERS_INCLUDE = path.resolve(
  __dirname,
  "../../../OrbPro/packages/da-flatbuffers/include",
);

// ── Helpers ────────────────────────────────────────────────────────────────────

function run(cmd, opts = {}) {
  console.log(`  $ ${cmd}`);
  execSync(cmd, { stdio: "inherit", ...opts });
}

function runSilent(cmd, opts = {}) {
  return execSync(cmd, { encoding: "utf8", ...opts });
}

function toCByteArray(buf) {
  return Array.from(buf)
    .map((b) => `0x${b.toString(16).padStart(2, "0")}`)
    .join(", ");
}

// ── FlatBuffer C++ header generation ─────────────────────────────────────────

async function generateFlatbufferHeaders(outDir) {
  console.log("  Generating FlatBuffer C++ headers...");
  fs.mkdirSync(outDir, { recursive: true });

  // Use flatc-wasm from the module-runner's node_modules
  const flatcWasmPath = path.join(
    REPO_ROOT,
    "packages",
    "module-runner",
    "node_modules",
    "flatc-wasm",
    "dist",
    "flatc-wasm.js",
  );
  if (!fs.existsSync(flatcWasmPath)) {
    throw new Error(
      `flatc-wasm not found at ${flatcWasmPath}.\n` +
        `Run: cd packages/module-runner && npm install`,
    );
  }

  const { default: createFlatc } = await import(
    `file://${flatcWasmPath}`
  );
  const flatc = await createFlatc();

  const schemaFiles = [
    "PluginInvokeRequest.fbs",
    "PluginInvokeResponse.fbs",
    "TypedArenaBuffer.fbs",
  ];

  const ensureDir = (p) => { try { flatc.FS.mkdir(p); } catch {} };
  ensureDir("/schemas");
  ensureDir("/out_cpp");

  for (const sf of schemaFiles) {
    flatc.FS.writeFile(
      `/schemas/${sf}`,
      fs.readFileSync(path.join(MODULE_SDK_SCHEMAS, sf), "utf8"),
    );
  }

  for (const sf of schemaFiles) {
    const rc = flatc.callMain([
      "--cpp", "--cpp-std", "c++17", "--gen-object-api",
      "-I", "/schemas", "-o", "/out_cpp", `/schemas/${sf}`,
    ]);
    if (rc !== 0) throw new Error(`flatc failed for ${sf}`);
    const hdr = `${path.basename(sf, ".fbs")}_generated.h`;
    fs.writeFileSync(
      path.join(outDir, hdr),
      flatc.FS.readFile(`/out_cpp/${hdr}`, { encoding: "utf8" }),
    );
  }
}

// ── Crypto++ source provisioning ──────────────────────────────────────────────

async function ensureCryptoppSources(dir) {
  const explicit = process.env.CRYPTOPP_SOURCE_DIR;
  if (explicit) {
    if (fs.existsSync(path.join(explicit, "aes.h"))) {
      console.log(`  Using local Crypto++ from ${explicit}`);
      return explicit;
    }
    if (fs.existsSync(path.join(explicit, "cryptopp", "aes.h"))) {
      console.log(`  Using local Crypto++ from ${explicit}/cryptopp`);
      return path.join(explicit, "cryptopp");
    }
    throw new Error(`CRYPTOPP_SOURCE_DIR does not contain aes.h: ${explicit}`);
  }

  if (fs.existsSync(path.join(dir, "aes.h"))) {
    console.log("  Crypto++ already fetched.");
    return dir;
  }

  console.log("  Cloning Crypto++ 8.9.0...");
  fs.mkdirSync(dir, { recursive: true });
  run(
    `git clone --depth=1 --branch CRYPTOPP_8_9_0 ` +
      `https://github.com/weidai11/cryptopp.git ${dir}`,
  );
  return dir;
}

// ── Compile Crypto++ to .a ────────────────────────────────────────────────────

function compileCryptoppLib(srcDir, objDir, archivePath, extraFlags = []) {
  if (fs.existsSync(archivePath)) {
    console.log("  Crypto++ library already compiled, skipping.");
    return;
  }
  console.log("  Compiling Crypto++ (this takes a while)...");
  fs.mkdirSync(objDir, { recursive: true });

  const skip = new Set([
    "adhoc.cpp", "bench1.cpp", "bench2.cpp", "bench3.cpp",
    "cryptest.cpp", "cryptestcwd.cpp", "datatest.cpp", "dlltest.cpp",
    "fipsalgt.cpp", "fipstest.cpp", "regtest1.cpp", "regtest2.cpp",
    "regtest3.cpp", "test.cpp", "validat0.cpp", "validat1.cpp",
    "validat2.cpp", "validat3.cpp", "validat4.cpp", "validat5.cpp",
    "validat6.cpp", "validat7.cpp", "validat8.cpp", "validat9.cpp",
    "validat10.cpp",
  ]);

  const sources = fs.readdirSync(srcDir)
    .filter((f) => f.endsWith(".cpp") && !skip.has(f));

  const objFiles = [];
  for (const src of sources) {
    const obj = path.join(objDir, src.replace(".cpp", ".o"));
    if (!fs.existsSync(obj)) {
      run(
        `emcc -O2 -std=c++17 -fignore-exceptions ` +
          `-DCRYPTOPP_DISABLE_ASM=1 -DCRYPTOPP_DISABLE_SSSE3=1 -DCRYPTOPP_DISABLE_AESNI=1 ` +
          `-I${srcDir} ` +
          extraFlags.join(" ") +
          ` -c ${path.join(srcDir, src)} -o ${obj}`,
      );
    }
    objFiles.push(obj);
  }

  run(`emar rcs ${archivePath} ${objFiles.join(" ")}`);
  console.log(`  Crypto++ archive: ${archivePath}`);
}

// ── Build plugin-delivery ─────────────────────────────────────────────────────

async function buildPluginDelivery(cryptoppSrc, fbbHeadersDir, flatbuffersInclude) {
  console.log("\n=== Building plugin-delivery ===");
  const pluginDir = path.join(PLUGINS_DIR, "plugin-delivery");
  const distDir = path.join(pluginDir, "dist");
  const objDir = path.join(BUILD_DIR, "plugin-delivery", "obj");
  fs.mkdirSync(distDir, { recursive: true });
  fs.mkdirSync(objDir, { recursive: true });

  // Bake server private key
  let privKeyHex = (process.env.SDN_SERVER_PRIVATE_KEY_HEX || "").trim();
  if (privKeyHex.length !== 64) {
    console.log("  Generating random server X25519 private key...");
    privKeyHex = crypto.randomBytes(32).toString("hex");
    // Save for reference (not committed)
    const secretsPath = path.join(pluginDir, ".server-key.hex");
    fs.writeFileSync(secretsPath, privKeyHex, "utf8");
    console.log(`  Saved to ${secretsPath} (gitignored)`);
  }
  const privKeyBytes = Buffer.from(privKeyHex, "hex");
  const bakedKeyLiteral = toCByteArray(privKeyBytes);

  // Apply template
  const srcTemplate = fs.readFileSync(
    path.join(pluginDir, "src", "plugin_delivery.cpp"),
    "utf8",
  );
  const srcFinal = srcTemplate.replace("SDN_BAKED_SERVER_PRIVATE_KEY", bakedKeyLiteral);
  const buildCppPath = path.join(objDir, "plugin_delivery_build.cpp");
  fs.writeFileSync(buildCppPath, srcFinal, "utf8");

  // Compile Crypto++
  const cryptoppObjDir = path.join(BUILD_DIR, "cryptopp-delivery-obj");
  const cryptoppLib = path.join(BUILD_DIR, "libcryptopp-delivery.a");
  compileCryptoppLib(cryptoppSrc, cryptoppObjDir, cryptoppLib);

  // Manifest exports stub
  const manifestExportsPath = path.join(pluginDir, "dist", "manifest-exports.cpp");
  const manifestStub = `
#include <stddef.h>
#include <stdint.h>
#define MODULE_MANIFEST_EXPORT __attribute__((visibility("default")))
// Minimal stub — full manifest generation is handled by manifest.mjs
static const uint8_t g_manifest[] = {0x00};
MODULE_MANIFEST_EXPORT const uint8_t* plugin_get_manifest_flatbuffer() { return g_manifest; }
MODULE_MANIFEST_EXPORT size_t plugin_get_manifest_flatbuffer_size() { return 0; }
`;
  fs.writeFileSync(manifestExportsPath, manifestStub, "utf8");

  const outWasm = path.join(distDir, "plugin-delivery.wasm");

  run(
    `em++ -O2 -std=c++17 -fignore-exceptions ` +
      `-DSDN_WASI_PLUGIN=1 ` +
      `-DCRYPTOPP_DISABLE_ASM=1 -DCRYPTOPP_DISABLE_SSSE3=1 -DCRYPTOPP_DISABLE_AESNI=1 ` +
      `-I${cryptoppSrc} -I${fbbHeadersDir} -I${flatbuffersInclude} ` +
      `${buildCppPath} ${manifestExportsPath} ${cryptoppLib} ` +
      `-sWASM=1 -sSTANDALONE_WASM=1 -sPURE_WASI=1 ` +
      `-sINITIAL_MEMORY=33554432 -sALLOW_MEMORY_GROWTH=1 ` +
      `-sFILESYSTEM=0 -sDISABLE_EXCEPTION_CATCHING=1 ` +
      `-sERROR_ON_UNDEFINED_SYMBOLS=0 ` +
      `-sEXPORTED_FUNCTIONS="['_plugin_invoke_stream','_plugin_alloc','_plugin_free','_plugin_get_manifest_flatbuffer','_plugin_get_manifest_flatbuffer_size','__start']" ` +
      `--no-entry -o ${outWasm}`,
  );

  console.log(`  Output: ${outWasm}`);
}

// ── Build client-decrypt ──────────────────────────────────────────────────────

async function buildClientDecrypt(cryptoppSrc, fbbHeadersDir, flatbuffersInclude) {
  console.log("\n=== Building client-decrypt ===");
  const pluginDir = path.join(PLUGINS_DIR, "client-decrypt");
  const distDir = path.join(pluginDir, "dist");
  const objDir = path.join(BUILD_DIR, "client-decrypt", "obj");
  fs.mkdirSync(distDir, { recursive: true });
  fs.mkdirSync(objDir, { recursive: true });

  // Compile Crypto++
  const cryptoppObjDir = path.join(BUILD_DIR, "cryptopp-decrypt-obj");
  const cryptoppLib = path.join(BUILD_DIR, "libcryptopp-decrypt.a");
  compileCryptoppLib(cryptoppSrc, cryptoppObjDir, cryptoppLib);

  const manifestExportsPath = path.join(distDir, "manifest-exports.cpp");
  const manifestStub = `
#include <stddef.h>
#include <stdint.h>
#define MODULE_MANIFEST_EXPORT __attribute__((visibility("default")))
static const uint8_t g_manifest[] = {0x00};
MODULE_MANIFEST_EXPORT const uint8_t* plugin_get_manifest_flatbuffer() { return g_manifest; }
MODULE_MANIFEST_EXPORT size_t plugin_get_manifest_flatbuffer_size() { return 0; }
`;
  fs.writeFileSync(manifestExportsPath, manifestStub, "utf8");

  const srcPath = path.join(pluginDir, "src", "client_decrypt.cpp");
  const outWasm = path.join(distDir, "client-decrypt.wasm");

  run(
    `em++ -O2 -std=c++17 -fignore-exceptions ` +
      `-DCRYPTOPP_DISABLE_ASM=1 -DCRYPTOPP_DISABLE_SSSE3=1 -DCRYPTOPP_DISABLE_AESNI=1 ` +
      `-I${cryptoppSrc} -I${fbbHeadersDir} -I${flatbuffersInclude} ` +
      `${srcPath} ${manifestExportsPath} ${cryptoppLib} ` +
      `-sWASM=1 -sSTANDALONE_WASM=1 -sPURE_WASI=1 ` +
      `-sINITIAL_MEMORY=16777216 -sALLOW_MEMORY_GROWTH=1 ` +
      `-sFILESYSTEM=0 -sDISABLE_EXCEPTION_CATCHING=1 ` +
      `-sERROR_ON_UNDEFINED_SYMBOLS=0 ` +
      `-sEXPORTED_FUNCTIONS="['_plugin_invoke_stream','_plugin_alloc','_plugin_free','_plugin_get_manifest_flatbuffer','_plugin_get_manifest_flatbuffer_size','__start']" ` +
      `--no-entry -o ${outWasm}`,
  );

  console.log(`  Output: ${outWasm}`);
}

// ── Main ──────────────────────────────────────────────────────────────────────

async function main() {
  const target = process.argv[2] ?? "all";
  console.log(`SDN Plugin Build — target: ${target}`);
  console.log(`Build dir: ${BUILD_DIR}`);
  fs.mkdirSync(BUILD_DIR, { recursive: true });

  // Resolve FlatBuffers C++ include path
  let flatbuffersInclude;
  if (fs.existsSync(path.join(ORBPRO_FLATBUFFERS_INCLUDE, "flatbuffers", "base.h"))) {
    flatbuffersInclude = ORBPRO_FLATBUFFERS_INCLUDE;
    console.log(`Using OrbPro FlatBuffers headers: ${flatbuffersInclude}`);
  } else {
    // Fall back to system flatbuffers (brew install flatbuffers)
    flatbuffersInclude = runSilent("brew --prefix flatbuffers 2>/dev/null || echo /usr/local", {}).trim() + "/include";
    if (!fs.existsSync(path.join(flatbuffersInclude, "flatbuffers", "base.h"))) {
      throw new Error(
        `FlatBuffers C++ headers not found.\n` +
          `Install with: brew install flatbuffers\n` +
          `Or set via OrbPro: ${ORBPRO_FLATBUFFERS_INCLUDE}`,
      );
    }
    console.log(`Using system FlatBuffers headers: ${flatbuffersInclude}`);
  }

  // Generate invoke FlatBuffer headers
  const fbbHeadersDir = path.join(BUILD_DIR, "fbb-headers");
  if (!fs.existsSync(path.join(fbbHeadersDir, "PluginInvokeRequest_generated.h"))) {
    await generateFlatbufferHeaders(fbbHeadersDir);
  } else {
    console.log("  FlatBuffer headers already generated.");
  }

  // Ensure Crypto++ sources
  const cryptoppSrc = await ensureCryptoppSources(
    path.join(BUILD_DIR, "cryptopp-src"),
  );

  if (target === "all" || target === "plugin-delivery") {
    await buildPluginDelivery(cryptoppSrc, fbbHeadersDir, flatbuffersInclude);
  }
  if (target === "all" || target === "client-decrypt") {
    await buildClientDecrypt(cryptoppSrc, fbbHeadersDir, flatbuffersInclude);
  }

  console.log("\n✓ Build complete.");
}

main().catch((err) => {
  console.error("\nBuild failed:", err.message);
  process.exit(1);
});
