/**
 * Edge Relay Discovery - Loads edge relay addresses for bootstrapping
 * Features:
 * - Encrypted WASM module with relay list
 * - SRI hash verification for integrity
 * - Fallback relay list for offline scenarios
 */

/**
 * Default edge relay addresses (fallback when WASM is not available or fails)
 * These are hardcoded bootstrap nodes that should always be available.
 */
export const DEFAULT_EDGE_RELAYS = [
  '/ip4/209.182.234.97/tcp/8080/ws/p2p/16Uiu2HAkxKtJncDGfgtFpx4mNqtrzbBBrCZ8iaKKyKuEqEHuEz5J',
  '/dns4/relay1.spacedatanetwork.org/tcp/443/wss/p2p/12D3KooWRelay1',
  '/dns4/relay2.spacedatanetwork.org/tcp/443/wss/p2p/12D3KooWRelay2',
];

/**
 * Fallback relays for regional availability
 */
export const REGIONAL_FALLBACK_RELAYS: Record<string, string[]> = {
  'us-east': ['/dns4/us-east.relay.spacedatanetwork.org/tcp/443/wss/p2p/12D3KooWUSEast1'],
  'eu-west': ['/dns4/eu-west.relay.spacedatanetwork.org/tcp/443/wss/p2p/12D3KooWEUWest1'],
  'ap-southeast': ['/dns4/ap-southeast.relay.spacedatanetwork.org/tcp/443/wss/p2p/12D3KooWAPSE1'],
};

let edgeRelaysModule: EdgeRelaysModule | null = null;
let cachedRelays: string[] | null = null;
let wasmVerified = false;

interface EdgeRelaysModule {
  ready: Promise<void>;
  _get_edge_relays: () => number;
  UTF8ToString: (ptr: number) => string;
}

interface WasmLoadOptions {
  /** Expected SRI hash for integrity verification */
  expectedSri?: string;
  /** Skip integrity verification (not recommended for production) */
  skipIntegrityCheck?: boolean;
}

/**
 * Load edge relays from the encrypted WASM module
 */
export async function loadEdgeRelays(): Promise<string[]> {
  if (cachedRelays) {
    return cachedRelays;
  }

  try {
    // Try to load WASM module dynamically
    if (!edgeRelaysModule) {
      edgeRelaysModule = await loadEdgeRelaysWasm();
    }

    if (edgeRelaysModule) {
      await edgeRelaysModule.ready;

      // Get decrypted relay list from WASM
      const relaysPtr = edgeRelaysModule._get_edge_relays();
      const relaysJson = edgeRelaysModule.UTF8ToString(relaysPtr);

      cachedRelays = JSON.parse(relaysJson);
      return cachedRelays!;
    }
  } catch (err) {
    console.warn('Failed to load encrypted edge relays, using defaults:', err);
  }

  // Fall back to default relays
  cachedRelays = DEFAULT_EDGE_RELAYS;
  return cachedRelays;
}

/**
 * Get bootstrap relay addresses
 * This is the main entry point for SDNNode initialization
 */
export async function getBootstrapRelays(): Promise<string[]> {
  try {
    return await loadEdgeRelays();
  } catch (err) {
    console.warn('Failed to load edge relays, using fallback:', err);
    return DEFAULT_EDGE_RELAYS;
  }
}

/**
 * Verify WASM integrity using SRI hash
 */
async function verifySri(data: ArrayBuffer, expectedSri: string): Promise<boolean> {
  try {
    // Parse the expected SRI hash (format: "sha384-base64hash")
    const match = expectedSri.match(/^sha(256|384|512)-(.+)$/);
    if (!match) {
      console.warn('Invalid SRI format:', expectedSri);
      return false;
    }

    const algorithm = `SHA-${match[1]}`;
    const expectedHash = match[2];

    // Compute hash of the data
    const hashBuffer = await crypto.subtle.digest(algorithm, data);
    const hashArray = new Uint8Array(hashBuffer);
    const actualHash = btoa(String.fromCharCode(...hashArray));

    // Compare hashes
    if (actualHash !== expectedHash) {
      console.error('WASM integrity check failed: hash mismatch');
      return false;
    }

    return true;
  } catch (err) {
    console.error('SRI verification error:', err);
    return false;
  }
}

/**
 * Fetch SRI hash from CDN
 */
async function fetchSriHash(wasmPath: string): Promise<string | null> {
  try {
    const sriPath = wasmPath + '.sri';
    const response = await fetch(sriPath);
    if (!response.ok) return null;
    return (await response.text()).trim();
  } catch {
    return null;
  }
}

/**
 * Load the edge relays WASM module with integrity verification
 */
async function loadEdgeRelaysWasm(options: WasmLoadOptions = {}): Promise<EdgeRelaysModule | null> {
  try {
    // Check if we're in a browser environment
    if (typeof window === 'undefined') {
      return null;
    }

    // Try to fetch the WASM file from the same origin
    const wasmPaths = [
      './edge-relays.wasm',
      '/edge-relays.wasm',
      'https://cdn.spacedatanetwork.org/wasm/edge-relays.wasm',
    ];

    for (const path of wasmPaths) {
      try {
        const response = await fetch(path);
        if (!response.ok) continue;

        const wasmBytes = await response.arrayBuffer();

        // Verify integrity if not skipped
        if (!options.skipIntegrityCheck) {
          let expectedSri = options.expectedSri;

          // Try to fetch SRI hash from CDN if not provided
          if (!expectedSri) {
            expectedSri = (await fetchSriHash(path)) ?? undefined;
          }

          if (expectedSri) {
            const isValid = await verifySri(wasmBytes, expectedSri);
            if (!isValid) {
              console.error(`WASM from ${path} failed integrity check`);
              continue;
            }
            wasmVerified = true;
          } else {
            console.warn(`No SRI hash available for ${path}, loading without verification`);
          }
        }

        const wasmModule = await WebAssembly.instantiate(wasmBytes, {
          env: {
            memory: new WebAssembly.Memory({ initial: 256 }),
          },
        });

        return {
          ready: Promise.resolve(),
          _get_edge_relays: wasmModule.instance.exports.get_edge_relays as () => number,
          UTF8ToString: (ptr: number) => {
            const memory = wasmModule.instance.exports.memory as WebAssembly.Memory;
            const view = new Uint8Array(memory.buffer);
            let end = ptr;
            while (view[end] !== 0) end++;
            return new TextDecoder().decode(view.slice(ptr, end));
          },
        };
      } catch {
        continue;
      }
    }

    return null;
  } catch (err) {
    console.warn('Failed to load edge relays WASM:', err);
    return null;
  }
}

/**
 * Check if the WASM module was verified
 */
export function isWasmVerified(): boolean {
  return wasmVerified;
}

/**
 * Get relays for a specific region (fallback)
 */
export function getRegionalRelays(region?: string): string[] {
  if (region && REGIONAL_FALLBACK_RELAYS[region]) {
    return REGIONAL_FALLBACK_RELAYS[region];
  }
  // Return all regional relays if no specific region
  return Object.values(REGIONAL_FALLBACK_RELAYS).flat();
}

/**
 * Get all fallback relays (default + regional)
 */
export function getAllFallbackRelays(): string[] {
  const allRelays = new Set([
    ...DEFAULT_EDGE_RELAYS,
    ...getRegionalRelays(),
  ]);
  return Array.from(allRelays);
}

/**
 * Edge relay discovery class for dynamic relay management
 */
export class EdgeDiscovery {
  private knownRelays: Set<string>;
  private failedRelays: Map<string, number>; // relay -> failure count
  private refreshInterval: number | null = null;
  private maxFailures = 3;

  constructor(initialRelays: string[] = DEFAULT_EDGE_RELAYS) {
    this.knownRelays = new Set(initialRelays);
    this.failedRelays = new Map();
  }

  /**
   * Get all known relay addresses
   */
  getRelays(): string[] {
    return Array.from(this.knownRelays);
  }

  /**
   * Add a new relay address
   */
  addRelay(addr: string): void {
    this.knownRelays.add(addr);
  }

  /**
   * Remove a relay address
   */
  removeRelay(addr: string): void {
    this.knownRelays.delete(addr);
  }

  /**
   * Check if a relay is known
   */
  hasRelay(addr: string): boolean {
    return this.knownRelays.has(addr);
  }

  /**
   * Mark a relay as failed (tracks failures for reliability scoring)
   */
  markFailed(addr: string): void {
    const failures = (this.failedRelays.get(addr) || 0) + 1;
    this.failedRelays.set(addr, failures);

    // Remove relay if it fails too many times
    if (failures >= this.maxFailures) {
      this.knownRelays.delete(addr);
      console.warn(`Relay ${addr} removed after ${failures} failures`);
    }
  }

  /**
   * Mark a relay as successful (resets failure count)
   */
  markSuccess(addr: string): void {
    this.failedRelays.delete(addr);
    this.knownRelays.add(addr);
  }

  /**
   * Get the best relays (prioritizes those with fewer failures)
   */
  getBestRelays(count: number = 3): string[] {
    const relays = this.getRelays();

    // Sort by failure count (fewer failures = better)
    const sorted = relays.sort((a, b) => {
      const failA = this.failedRelays.get(a) || 0;
      const failB = this.failedRelays.get(b) || 0;
      return failA - failB;
    });

    return sorted.slice(0, count);
  }

  /**
   * Ensure we have minimum number of relays by adding fallbacks
   */
  ensureMinimumRelays(minimum: number = 2): void {
    if (this.knownRelays.size < minimum) {
      const fallbacks = getAllFallbackRelays();
      for (const relay of fallbacks) {
        if (this.knownRelays.size >= minimum) break;
        this.knownRelays.add(relay);
      }
    }
  }

  /**
   * Start periodic refresh from WASM
   */
  startRefresh(intervalMs: number = 300000): void {
    if (this.refreshInterval) {
      clearInterval(this.refreshInterval);
    }

    this.refreshInterval = window.setInterval(async () => {
      try {
        // Clear cache to force reload
        cachedRelays = null;
        const newRelays = await loadEdgeRelays();
        for (const relay of newRelays) {
          this.knownRelays.add(relay);
        }
      } catch (err) {
        console.warn('Failed to refresh edge relays:', err);
      }
    }, intervalMs);
  }

  /**
   * Stop periodic refresh
   */
  stopRefresh(): void {
    if (this.refreshInterval) {
      clearInterval(this.refreshInterval);
      this.refreshInterval = null;
    }
  }

  /**
   * Get a circuit relay address for a target peer
   */
  getCircuitAddress(targetPeerId: string): string | null {
    const relays = this.getRelays();
    if (relays.length === 0) {
      return null;
    }

    // Pick a random relay
    const relay = relays[Math.floor(Math.random() * relays.length)];
    return `${relay}/p2p-circuit/p2p/${targetPeerId}`;
  }
}
