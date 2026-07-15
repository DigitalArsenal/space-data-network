// Node-based isomorphic parity harness for the PAGE-side module loader
// (kubo rebase Phase 9). It loads a REAL SDN module artifact through the very
// same self-contained page harness the browser console serves
// (assets/module-harness.js) — WASI shim + space_data_module_host bridge +
// plugin_invoke_stream ABI — and asserts the decoded $PIV result is identical
// to what the node-side runtime (kubo/sdn/modulert) returns for the same
// method + input. That node-side result is pinned by the Go integration test
// modulert/module_invoke_integration_test.go, which drives the SAME licensing
// module through WasmEdge and asserts:
//
//   server_configure_runtime + no input -> $PIV status 400,
//       "Missing required input port: config"
//   no-such-method                       -> $PIV status 404, "Unknown method"
//
// so a green run here proves: same module.wasm, same ABI, same structured
// result — page host == node host (the isomorphic guarantee).
//
// Additionally cross-checks the page harness's hand-authored $PIV codec against
// the SDK's real FlatBuffers codec, and runs the SAME wasm through the SDK's
// own createBrowserModuleHarness (the reused browser-host lineage) for a second
// independent host.
//
// Usage:  node sdn/sdnui/module-harness.parity.mjs
// Exit:   0 pass · 1 fail · 2 skipped (licensing artifact not in this checkout)

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const HARNESS = path.join(here, "assets", "module-harness.js");

const WASM_SUFFIX = ["space-data-network-modules", "licensing", "core", "dist", "isomorphic", "module.wasm"];

function findStackRoot(start) {
  for (let dir = start; ; dir = path.dirname(dir)) {
    if (fs.existsSync(path.join(dir, "repos", "main-packages")) &&
        fs.existsSync(path.join(dir, "docs", "repository-catalog.md"))) {
      return dir;
    }
    const parent = path.dirname(dir);
    if (parent === dir) return null;
  }
}

function findLicensingWasm() {
  const env = process.env.ORBPRO_LICENSING_WASM_PATH;
  if (env && fs.existsSync(env)) return env;
  const root = findStackRoot(here);
  if (root) {
    for (const base of ["main-packages", "ancillary-packages"]) {
      const p = path.join(root, "repos", base, ...WASM_SUFFIX);
      if (fs.existsSync(p)) return p;
    }
  }
  return null;
}

let failures = 0;
function assert(cond, msg) {
  if (cond) { console.log("  ok:", msg); return; }
  console.error("  FAIL:", msg);
  failures += 1;
}

const wasmPath = findLicensingWasm();
if (!wasmPath) {
  console.log("SKIP: unified licensing WASM artifact not available in this checkout.");
  process.exit(2);
}

const harness = await import(HARNESS);
const wasmBytes = new Uint8Array(fs.readFileSync(wasmPath));
const contentHash = await harness.sha256Hex(wasmBytes);
console.log(`module: ${wasmPath}`);
console.log(`CONTENT_HASH: ${contentHash}  (${wasmBytes.length} bytes)\n`);

// ---- Load through the PAGE harness (the browser host) ----
const { instance } = await harness.loadModuleFromBytes(wasmBytes, { expectedHash: contentHash });

console.log("[1] declared method server_configure_runtime (no input) — page host:");
const r1 = instance.invoke("server_configure_runtime", null);
console.log("    ", JSON.stringify({ statusCode: r1.statusCode, status: r1.status, errorCode: r1.errorCode, errorMessage: r1.errorMessage }));
assert(r1.statusCode === 400, `statusCode == 400 (node parity)`);
assert(/Missing required input port/i.test(r1.errorMessage || ""), "errorMessage: 'Missing required input port'");
assert(/config/i.test(r1.errorMessage || ""), "errorMessage names the 'config' port");

console.log("\n[2] undeclared method no-such-method — page host:");
const r2 = instance.invoke("no-such-method", null);
console.log("    ", JSON.stringify({ statusCode: r2.statusCode, status: r2.status, errorCode: r2.errorCode, errorMessage: r2.errorMessage }));
assert(r2.statusCode === 404, `statusCode == 404 (node parity)`);
assert(/Unknown method/i.test((r2.errorMessage || "") + (r2.errorCode || "")), "names 'Unknown method'");

// ---- Cross-check against the SDK's REAL FlatBuffers $PIV codec ----
console.log("\n[3] $PIV codec cross-check vs SDK invoke/codec.js (real FlatBuffers):");
const root = findStackRoot(here);
let sdkCodec = null;
try { sdkCodec = await import(path.join(root, "repos", "ancillary-packages", "space-data-module-sdk", "src", "invoke", "codec.js")); }
catch (e) { console.log("    (SDK codec unavailable:", e.message, ") — skipped"); }
if (sdkCodec) {
  const reqBytes = harness.encodePluginInvokeRequest("server_configure_runtime", null);
  const sdkReq = sdkCodec.decodePluginInvokeRequest(reqBytes);
  assert(sdkReq.methodId === "server_configure_runtime", "SDK decodes our request methodId");
  const sdkResp = sdkCodec.decodePluginInvokeResponse(instance.invokeRaw(reqBytes));
  assert(sdkResp.statusCode === r1.statusCode && (sdkResp.errorMessage || "") === (r1.errorMessage || ""),
    "SDK real-codec decode == page-harness decode");
  const sdkReqBytes = sdkCodec.encodePluginInvokeRequest({ methodId: "server_configure_runtime", inputs: [] });
  const back = harness.decodePluginInvokeResponse(instance.invokeRaw(sdkReqBytes));
  assert(back.statusCode === 400 && (back.errorMessage || "") === (r1.errorMessage || ""),
    "SDK-encoded request through page guest == same result");
}

// ---- Second independent browser host: the SDK's own createBrowserModuleHarness ----
console.log("\n[4] second browser host cross-check vs SDK createBrowserModuleHarness:");
try {
  const { createBrowserModuleHarness } = await import(path.join(root, "repos", "ancillary-packages", "space-data-module-sdk", "src", "testing", "browserModuleHarness.js"));
  const sdkHarness = await createBrowserModuleHarness({ wasmSource: wasmBytes, surface: "direct" });
  const s1 = await sdkHarness.invoke({ methodId: "server_configure_runtime", inputs: [] });
  assert(s1.statusCode === r1.statusCode && (s1.errorMessage || "") === (r1.errorMessage || ""),
    "SDK browser host == page harness (two browser hosts agree)");
} catch (e) {
  console.log("    (SDK browser harness unavailable:", e.message, ") — non-fatal");
}

console.log(failures === 0
  ? "\nPARITY OK — page-harness result == node/modulert result for the same module + method."
  : `\nPARITY FAILED — ${failures} assertion(s) failed.`);
process.exit(failures === 0 ? 0 : 1);
