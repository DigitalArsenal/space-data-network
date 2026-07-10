/**
 * hd-wallet-wasm browser-loader vendoring shim (loop task U0.3, D1
 * groundwork) — now a documented NO-OP.
 *
 * HISTORY: this shim originally imported the package's `.wasm` binary via a
 * Vite `?url` import (inlined as a `data:` URI by the spaceaware build's
 * `assetsInlineLimit`) and patched `XMLHttpRequest.prototype.open` to
 * redirect the Emscripten glue's expected runtime fetch of
 * `hd-wallet.wasm` to that embedded asset, so wallet unlock/signing would
 * work from the single-file embedded artifact (PACKAGING HARD RULE,
 * SDN_SPACEAWARE_UI_LOOP.md) where no separate files are served.
 *
 * WHY IT IS NOW A NO-OP (U2.2-era dedupe, 2026-07-10): the shipped
 * `hd-wallet-wasm@2.0.20` glue (`dist/hd-wallet.js`, 5.2 MB) is an
 * Emscripten **SINGLE_FILE** build — the wasm binary is ALREADY embedded in
 * the glue itself as an `application/octet-stream` `data:` URI
 * (`wasmBinaryFile` starts as a data URI and the glue's
 * `isDataURI(wasmBinaryFile)` check short-circuits every XHR/fetch path).
 * The runtime fetch this shim guarded against is therefore unreachable, and
 * the extra `?url` import was double-shipping the same ~3.8 MB wasm binary
 * (~5 MB as base64) in the embedded artifact — measured: the artifact
 * carried the identical `\0asm` blob twice, once `application/wasm` (this
 * shim's import) and once `application/octet-stream` (the glue's own).
 * Removing the import halves the artifact; the loader entry points remain
 * so callers (`local-wallet.ts`) and any future non-SINGLE_FILE upgrade
 * path stay source-compatible.
 *
 * IF A FUTURE hd-wallet-wasm UPGRADE STOPS EMBEDDING THE WASM (glue starts
 * XHR-ing `hd-wallet.wasm` again): wallet unlock will fail LOUDLY with a
 * same-origin 404 in the spaceaware artifact — restore the `?url` import +
 * XHR redirect from git history (commit a088bfd7 has the last working
 * copy) and re-measure the artifact for duplication.
 */

let installed = false;

/**
 * Historically patched `XMLHttpRequest.prototype.open` to redirect the
 * package's `hd-wallet.wasm` fetch to a vendored in-bundle asset. The
 * current package embeds its wasm (SINGLE_FILE build — see module doc), so
 * this is a deliberate no-op kept for call-site/source compatibility.
 * Idempotent; safe to call multiple times.
 */
export function vendorHdWalletWasmLoader(): void {
  if (installed) return;
  installed = true;
  // Intentionally nothing: dist/hd-wallet.js never issues the XHR this
  // shim used to intercept. See module doc for the revert recipe if that
  // ever changes.
}

/** Test/dev hook: forget install state (kept for API compatibility). */
export function resetHdWalletWasmVendorForTests(originalOpen?: typeof XMLHttpRequest.prototype.open): void {
  installed = false;
  if (originalOpen && typeof XMLHttpRequest !== 'undefined') {
    XMLHttpRequest.prototype.open = originalOpen;
  }
}
