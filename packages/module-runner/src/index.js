/**
 * @spacedatanetwork/module-runner
 *
 * Isomorphic WASM module runner built on the space-data-module-sdk 0.5.22
 * browser harness. Wraps loadModule() / createBrowserModuleHarness() and adds
 * libp2p protocol registration so a module's declared inbound protocols are
 * live the moment the runner starts. Async client-side transport flows such as
 * the license exchange belong in sdn-js, not in guest-side protocol hostcalls.
 *
 * Browser:  uses createBrowserModuleHarness (WASI shim + optional space_data_module_host)
 * Node/CI:  uses WasmEdge via the isomorphic loader
 */

export { createModuleRunner } from "./runner.js";
export { decryptArtifact, encryptArtifact } from "./artifact-crypto.js";
export {
  loadModule,
  inspectModule,
} from "space-data-module-sdk/host/isomorphic";
export {
  createBrowserModuleHarness,
  detectArtifactProfile,
} from "space-data-module-sdk/testing/browser";
export { createBrowserHost } from "space-data-module-sdk/host/browser";
