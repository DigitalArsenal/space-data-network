// Browser-bundle shim: satellite.js's optional WASM runtime pulls in Node
// builtins (node:module, node:worker_threads). The SDK only uses the pure-JS
// SGP4 API, so the wasm entry is replaced with an empty module when bundling.
export {};
