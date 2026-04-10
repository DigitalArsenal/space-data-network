/**
 * createModuleRunner — high-level module lifecycle manager.
 *
 * Loads a WASM module via the module-sdk isomorphic harness and optionally
 * registers libp2p stream handlers for each protocol the module's manifest
 * declares.  The returned runner mirrors the modulert.Module interface used
 * by the Go server, so both sides speak the same concepts.
 *
 * Usage (browser or Node.js):
 *
 *   const runner = await createModuleRunner({
 *     wasmSource: wasmBytes,          // Uint8Array, ArrayBuffer, URL, or path
 *     libp2p: heliaNode.libp2p,       // optional — registers inbound module protocols
 *     capabilities: ["http", "crypto_hash"],
 *   });
 *
 *   const result = await runner.invoke("myMethod", [{ payload: inputBytes }]);
 *   const manifest = runner.manifest;
 *   await runner.destroy();
 */

import {
  loadModule,
  inspectModule,
} from "space-data-module-sdk/host/isomorphic";
import { createBrowserHost } from "space-data-module-sdk/host/browser";
import { decodePluginManifest } from "space-data-module-sdk/manifest";

/**
 * Create a module runner backed by the module-sdk isomorphic harness.
 *
 * @param {object} options
 * @param {Uint8Array|ArrayBuffer|Response|string} options.wasmSource
 * @param {object}  [options.libp2p]      — libp2p / Helia instance used to register inbound module protocols
 * @param {string[]}[options.capabilities]— capabilities to grant the module
 * @param {string[]}[options.args]         — WASI args
 * @param {object}  [options.env]          — WASI env vars
 * @param {string}  [options.surface]      — "direct" | "command" (auto-detected)
 * @param {boolean} [options.logOutput]    — pipe WASM stdout/stderr to console
 * @returns {Promise<ModuleRunner>}
 */
export async function createModuleRunner(options = {}) {
  const {
    wasmSource,
    libp2p = null,
    capabilities = [],
    args = [],
    env = {},
    surface,
    logOutput = false,
  } = options;

  if (!wasmSource) throw new TypeError("wasmSource is required");

  // Build a browser host granting the requested capabilities
  const host = createBrowserHost({
    capabilities: capabilities.length > 0 ? capabilities : undefined,
  });

  // Load the module via the isomorphic harness
  const harness = await loadModule({
    wasmSource,
    host,
    args,
    env,
    surface,
    logOutput,
  });

  // Decode the embedded manifest FlatBuffer (if present)
  let manifest = null;
  const rawManifest = harness.readManifest?.();
  if (rawManifest?.length) {
    try {
      manifest = decodePluginManifest(rawManifest);
    } catch {
      // manifest not present or not parseable — non-fatal
    }
  }

  // Register libp2p stream handlers for each protocol in the manifest
  const registeredProtocols = [];
  if (libp2p && manifest?.protocols?.length) {
    for (const proto of manifest.protocols) {
      if (!proto.protocolId || !proto.methodId) continue;
      libp2p.handle(proto.protocolId, async ({ stream }) => {
        const chunks = [];
        for await (const chunk of stream.source) {
          chunks.push(chunk instanceof Uint8Array ? chunk : new Uint8Array(chunk.subarray()));
        }
        const reqBytes = chunks.length === 1
          ? chunks[0]
          : new Uint8Array(chunks.reduce((a, b) => a + b.length, 0));
        if (chunks.length > 1) {
          let off = 0;
          for (const c of chunks) { reqBytes.set(c, off); off += c.length; }
        }
        const response = await harness.invoke({
          methodId: proto.methodId,
          inputs: [{ payload: reqBytes }],
        });
        const outBytes = response.outputs?.[0]?.payload ?? new Uint8Array();
        await stream.sink([outBytes]);
      });
      registeredProtocols.push(proto.protocolId);
    }
  }

  return {
    /** Decoded plugin manifest (null if unavailable). */
    manifest,
    /** The raw module-sdk harness. */
    harness,
    /** Registered libp2p protocol IDs. */
    protocols: registeredProtocols,

    /**
     * Invoke a module method.
     *
     * @param {string} methodId
     * @param {Array}  [inputs]  — array of InvokeFrame objects
     * @returns {Promise<PluginInvokeResponseEnvelope>}
     */
    invoke(methodId, inputs = []) {
      return harness.invoke({ methodId, inputs });
    },

    /**
     * Tear down the module, unregister libp2p handlers.
     */
    async destroy() {
      if (libp2p) {
        for (const pid of registeredProtocols) {
          try { libp2p.unhandle(pid); } catch { /* ignore */ }
        }
      }
      await harness.destroy?.();
    },
  };
}
