const DEFAULT_WASI_URL = new URL('../wasm/flatsql-wasi.wasm', import.meta.url);

let cachedWASIModule: Promise<Uint8Array> | null = null;

/**
 * Preload the packaged FlatSQL WASI module for runtimes that need direct WASM access.
 */
export async function preloadFlatSQLWASI(): Promise<Uint8Array> {
  if (!cachedWASIModule) {
    cachedWASIModule = loadFlatSQLWASI(DEFAULT_WASI_URL);
  }

  return cachedWASIModule;
}

/**
 * Return the packaged FlatSQL WASI URL for diagnostics/bootstrapping.
 */
export function getFlatSQLWASIPath(): string {
  return DEFAULT_WASI_URL.toString();
}

async function loadFlatSQLWASI(url: URL): Promise<Uint8Array> {
  if (url.protocol === 'file:') {
    return readLocalFile(url);
  }

  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`Failed to load flatsql-wasi.wasm: ${response.status}`);
  }

  return new Uint8Array(await response.arrayBuffer());
}

async function readLocalFile(url: URL): Promise<Uint8Array> {
  const fsSpecifier = ['node', 'fs/promises'].join(':');
  const urlSpecifier = ['node', 'url'].join(':');
  const [{ readFile }, { fileURLToPath }] = await Promise.all([
    import(fsSpecifier),
    import(urlSpecifier),
  ]);

  return new Uint8Array(await readFile(fileURLToPath(url)));
}
