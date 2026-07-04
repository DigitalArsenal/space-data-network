/**
 * Bundling shim for the bare `module` specifier inside flatsql's emscripten
 * glue (loop D.1: the engine is bundled into the package entries). The glue
 * only touches `createRequire` on its ENVIRONMENT_IS_NODE path, so in Node
 * the real builtin is loaded at runtime (the computed specifier keeps
 * esbuild from trying to resolve it at build time); in browsers the export
 * is never used.
 */
const nodeModule =
  typeof process !== 'undefined' && process.versions?.node
    ? await import(/* @vite-ignore */ 'node:'.concat('module'))
    : null;

export const createRequire = nodeModule?.createRequire;
export default nodeModule;
