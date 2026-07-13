/**
 * Empty `hd-wallet-wasm` stub for the CONJUNCTION-only ship build
 * (loop SDN_SPACEAWARE_UI_LOOP.md Phase C, task C1).
 *
 * Aliased in `ui/vite.conjunction.config.mts` so the ~5 MB SINGLE_FILE wasm
 * glue (`hd-wallet-wasm/dist/hd-wallet.js`, which also carries the BIP-39
 * wordlists) never enters the conjunction bundle. This ship keeps no session
 * flow, so nothing at runtime ever reaches wallet crypto — the only path to
 * `hd-wallet-wasm` is a dead static import chain
 * (`lib/console.ts` → `lib/login.ts` → `lib/auth/local-wallet.ts` →
 * `src/crypto/hd-wallet.ts`) whose used functions are all tree-shaken.
 *
 * The exports below only need to satisfy `src/crypto/hd-wallet.ts`'s import
 * bindings so the module still evaluates if it survives tree-shaking:
 * `initHDWalletWasm` (default) and the `Curve` / `Language` enums are all
 * referenced solely inside function bodies there, never at module top level,
 * so these placeholders are never actually consumed. The default export
 * throws if invoked — a loud signal that a wallet flow leaked into this ship.
 */

export const Curve = {
  ED25519: 0,
  X25519: 1,
  SECP256K1: 2,
} as const;

export const Language = {
  English: 0,
} as const;

export type HDWalletModule = unknown;

export default async function initHDWalletWasm(): Promise<never> {
  throw new Error(
    'hd-wallet-wasm is stubbed out of the conjunction-only ship: no session flow is bundled',
  );
}
