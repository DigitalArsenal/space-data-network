/**
 * Bundling shim for `node:crypto` inside flatsql's wasm loader (loop D.1).
 * Only reached on the Node-only WASM integrity-verification path (guarded by
 * hasNodeProcess(); sdn-js initializes the engine with skipIntegrityCheck).
 * In Node the real builtin is loaded at runtime; browsers never execute it.
 */
const nodeCrypto =
  typeof process !== 'undefined' && process.versions?.node
    ? await import(/* @vite-ignore */ 'node:'.concat('crypto'))
    : null;

export const createHash = (nodeCrypto as { createHash?: unknown } | null)?.createHash;
export default nodeCrypto;
