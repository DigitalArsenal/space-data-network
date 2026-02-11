import { getFlatSQLWASIURL, loadFlatSQLWASI } from 'flatsql/wasi';

let cachedWASIModule: Promise<Uint8Array> | null = null;

/**
 * Preload the packaged FlatSQL WASI module for runtimes that need direct WASM access.
 */
export async function preloadFlatSQLWASI(): Promise<Uint8Array> {
  if (!cachedWASIModule) {
    cachedWASIModule = loadFlatSQLWASI({ as: 'uint8array' }).then((bytes) => {
      return bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
    });
  }

  return cachedWASIModule;
}

/**
 * Return the packaged FlatSQL WASI URL for diagnostics/bootstrapping.
 */
export function getFlatSQLWASIPath(): string {
  return getFlatSQLWASIURL().toString();
}
