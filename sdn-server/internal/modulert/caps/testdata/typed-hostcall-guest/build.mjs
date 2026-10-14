// Builds the guest modules from guest.cpp with the space-data-module-sdk compiler (which stages typedHostcall.hpp and
// HostOps_aligned.h in every C++ compile):
//
//   node build.mjs <space-data-module-sdk checkout>
//
// - guest.wasm: every capability the e2e test drives (storage_query and p2p_read included);
// - guest-no-query.wasm: the same code without storage_query, so t_denied proves the refusal comes back typed.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const sdk = path.resolve(process.argv[2] || "");
const here = path.dirname(fileURLToPath(import.meta.url));
const { compileModuleFromSource, cleanupCompilation } = await import(path.join(sdk, "src/index.js"));
const methods = ["t_clock", "t_random", "t_config", "t_big_config", "t_ops", "t_ingest", "t_sql", "t_rows", "t_expire", "t_denied", "t_sha", "t_sign", "t_gcm",
  "t_http", "t_peers", "t_pnms", "t_latest", "guest_out_ptr", "guest_out_len"];
const base = ["clock", "random", "storage_ingest", "storage_write", "crypto_hash", "crypto_sign", "crypto_verify", "crypto_encrypt", "crypto_decrypt", "http", "p2p_read"];
const sourceCode = fs.readFileSync(path.join(here, "guest.cpp"), "utf8");

for (const [file, capabilities] of [["guest.wasm", [...base, "storage_query"]], ["guest-no-query.wasm", base]]) {
  const compilation = await compileModuleFromSource({
    manifest: {
      pluginId: "com.digitalarsenal.tests.typed-hostcall-guest",
      name: "Typed hostcall guest",
      version: "1.0.0",
      pluginFamily: "analysis",
      capabilities,
      externalInterfaces: [],
      invokeSurfaces: ["direct"],
      methods: methods.map((methodId) => ({ methodId, displayName: methodId, inputPorts: [], outputPorts: [], maxBatch: 1, drainPolicy: "single-shot" })),
    },
    sourceCode,
    language: "c++",
    allowUndefinedImports: true,
  });
  fs.writeFileSync(path.join(here, file), compilation.wasmBytes);
  await cleanupCompilation(compilation);
  console.log(`${file}: ${compilation.wasmBytes.length} bytes`);
}
process.exit(0);
