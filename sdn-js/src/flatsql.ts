type FlatSQLWASIURL = string | URL;

const DEFAULT_WASI_RELATIVE_PATH = '../wasm/flatsql-wasi.wasm';

let cachedWASIModule: Promise<Uint8Array> | null = null;
let cachedDefaultWASIURL: URL | null = null;

/**
 * Preload the packaged FlatSQL WASI module for runtimes that need direct WASM access.
 */
export async function preloadFlatSQLWASI(wasiUrl?: FlatSQLWASIURL): Promise<Uint8Array> {
  if (wasiUrl) {
    return loadFlatSQLWASI(resolveFlatSQLWASIURL(wasiUrl));
  }
  if (!cachedWASIModule) {
    cachedWASIModule = loadFlatSQLWASI(getDefaultWASIURL());
  }

  return cachedWASIModule;
}

/**
 * Return the packaged FlatSQL WASI URL for diagnostics/bootstrapping.
 */
export function getFlatSQLWASIPath(wasiUrl?: FlatSQLWASIURL): string {
  return resolveFlatSQLWASIURL(wasiUrl).toString();
}

function resolveFlatSQLWASIURL(wasiUrl?: FlatSQLWASIURL): URL {
  if (!wasiUrl) {
    return getDefaultWASIURL();
  }
  if (wasiUrl instanceof URL) {
    return wasiUrl;
  }

  return new URL(wasiUrl, getDocumentBaseURI());
}

function getDefaultWASIURL(): URL {
  if (!cachedDefaultWASIURL) {
    const importMetaUrl = import.meta.url;
    if (typeof importMetaUrl !== 'string' || importMetaUrl.length === 0) {
      throw new Error(
        'Unable to resolve packaged FlatSQL WASI URL because import.meta.url is unavailable; pass an explicit WASI URL.',
      );
    }
    cachedDefaultWASIURL = new URL(DEFAULT_WASI_RELATIVE_PATH, importMetaUrl);
  }

  return cachedDefaultWASIURL;
}

function getDocumentBaseURI(): string | undefined {
  if (typeof document === 'undefined') {
    return undefined;
  }
  return document.baseURI;
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
