/**
 * hd-wallet-wasm browser-loader vendoring shim (loop task U0.3, D1
 * groundwork).
 *
 * PROBLEM: the `hd-wallet-wasm` npm package's browser loader
 * (`hd-wallet-wasm/dist/hd-wallet.js`, an Emscripten-generated glue file)
 * fetches its `.wasm` binary at runtime via `XMLHttpRequest` against a URL
 * resolved relative to the executing script's own location
 * (`import.meta.url` / `document.currentScript.src`). That is fine for the
 * NORMAL sdn-ui build (`ui/vite.config.mts`), which ships a real `dist/`
 * folder of separate files on disk. It is NOT fine for the SpaceAware build
 * (`ui/vite.spaceaware.config.mts`): the PACKAGING HARD RULE
 * (SDN_SPACEAWARE_UI_LOOP.md GROUND TRUTH) inlines the entire app into ONE
 * `<script>` tag with no separate files served at all, so the glue file's
 * relative fetch resolves to a same-origin path that serves nothing (e.g.
 * `https://sdn.example.com/hd-wallet.wasm` from an inline `<script>` on
 * `/login`) and 404s — wallet unlock/signing would silently fail in the
 * shipped artifact despite working in every other environment (Node tests,
 * the non-spaceaware sdn-ui dist build).
 *
 * FIX: vendor the wasm binary as an in-bundle asset (a Vite `?url` import,
 * which the spaceaware build inlines as a `data:` URI under its
 * `assetsInlineLimit`) and intercept `XMLHttpRequest.prototype.open` calls
 * that target the package's hard-coded `hd-wallet.wasm` filename, redirecting
 * them to the embedded `data:` URI instead. `data:` URIs are same-document,
 * not network requests — this preserves the "zero external network
 * requests" hard rule while keeping `hd-wallet-wasm` itself unmodified (this
 * module lives entirely under `sdn-js/ui/`; the shared `sdn-js/src/crypto`
 * wrapper that every other consumer relies on is untouched).
 *
 * In Vite DEV mode (`npm run dev:spaceaware`) the `?url` import instead
 * resolves to the real Vite dev-server URL for the file, so the same
 * interceptor also fixes the otherwise-mismatched relative path there —
 * `vendorHdWalletWasmLoader()` is safe (and required) to call unconditionally
 * before the first `initHDWallet()`.
 */

// Vite `?url` import: emitted as a `data:` URI (spaceaware build,
// assetsInlineLimit far above this file's size) or a dev-server URL (Vite
// dev mode). A RELATIVE path is used deliberately, not the `hd-wallet-wasm`
// package specifier: the package's `package.json` "exports" map does not
// list `./dist/hd-wallet.wasm` as an importable subpath (only `./wasi.wasm`
// is exported — almost certainly an upstream oversight), so a bare-specifier
// deep import is rejected by both Vite and plain Node ESM resolution.
// Reaching the file by relative path bypasses "exports" enforcement (which
// only applies to package-specifier resolution) without patching
// node_modules or any config outside sdn-js/ui/.
import hdWalletWasmUrl from '../../../../node_modules/hd-wallet-wasm/dist/hd-wallet.wasm?url';

let installed = false;

/**
 * Patch `XMLHttpRequest.prototype.open` so any request for the package's
 * `hd-wallet.wasm` filename is redirected to the vendored in-bundle asset.
 * Idempotent; safe to call multiple times (only patches once).
 */
export function vendorHdWalletWasmLoader(): void {
  if (installed) return;
  if (typeof XMLHttpRequest === 'undefined') return; // non-browser (e.g. vitest node env)
  installed = true;

  const originalOpen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function patchedOpen(
    this: XMLHttpRequest,
    method: string,
    url: string | URL,
    ...rest: unknown[]
  ): void {
    const target = typeof url === 'string' ? url : url.toString();
    const redirected = target.endsWith('hd-wallet.wasm') ? hdWalletWasmUrl : target;
    (originalOpen as unknown as (...args: unknown[]) => void).call(this, method, redirected, ...rest);
  } as typeof XMLHttpRequest.prototype.open;
}

/** Test/dev hook: undo the patch and forget install state. */
export function resetHdWalletWasmVendorForTests(originalOpen?: typeof XMLHttpRequest.prototype.open): void {
  installed = false;
  if (originalOpen && typeof XMLHttpRequest !== 'undefined') {
    XMLHttpRequest.prototype.open = originalOpen;
  }
}
