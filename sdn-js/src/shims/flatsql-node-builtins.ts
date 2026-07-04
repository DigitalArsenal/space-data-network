/**
 * Bundling shims for the bare `fs` / `path` / `url` specifiers inside
 * flatsql's wasm loader (loop D.1: the engine is bundled into the package
 * entries). They are only reached on the Node-only WASM integrity-
 * verification fallback (sdn-js initializes the engine with
 * skipIntegrityCheck); in Node the real builtins are loaded at runtime, in
 * browsers the exports are never used.
 */
const isNode = typeof process !== 'undefined' && Boolean(process.versions?.node);

const nodeFs = isNode ? await import(/* @vite-ignore */ 'node:'.concat('fs')) : null;
const nodePath = isNode ? await import(/* @vite-ignore */ 'node:'.concat('path')) : null;
const nodeUrl = isNode ? await import(/* @vite-ignore */ 'node:'.concat('url')) : null;

export const readFileSync = nodeFs?.readFileSync;
export const existsSync = nodeFs?.existsSync;
export const dirname = nodePath?.dirname;
export const join = nodePath?.join;
export const fileURLToPath = nodeUrl?.fileURLToPath;
export const pathToFileURL = nodeUrl?.pathToFileURL;

export const fs = nodeFs;
export const path = nodePath;
export const url = nodeUrl;
