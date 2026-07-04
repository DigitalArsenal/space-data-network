/**
 * Compatibility shim (loop D.1): the WASM engine store was promoted from
 * this webUI runtime module into core `src/local-flatsql.ts`, where it is
 * THE SDNNode store. All exports are preserved here so existing webUI
 * imports keep working unchanged.
 */
export * from '../../local-flatsql';
