/**
 * The node dashboard's stand-in for the bundled `hd-wallet-wasm` package.
 *
 * The dashboard loads the wallet module natively from /wallet-wasm/ for
 * sign-in and hands that same instance to sdn-js (useHDWalletModule), so the
 * package's own WASM (megabytes) must not also be inlined into the dashboard's
 * single-file build. dashboard/vite.config.mjs aliases the bare import here.
 * The enums are the package's own values (hd-wallet-wasm dist/runtime).
 */
export const Curve = Object.freeze({ SECP256K1: 0, ED25519: 1, P256: 2, P384: 3, X25519: 4 });
export const Language = Object.freeze({
  ENGLISH: 0, JAPANESE: 1, KOREAN: 2, SPANISH: 3, CHINESE_SIMPLIFIED: 4,
  CHINESE_TRADITIONAL: 5, FRENCH: 6, ITALIAN: 7, CZECH: 8, PORTUGUESE: 9,
});
export type HDWalletModule = any;

export default async function init(): Promise<never> {
  throw new Error('In the node dashboard the wallet module comes from /wallet-wasm/; pass it to useHDWalletModule.');
}
