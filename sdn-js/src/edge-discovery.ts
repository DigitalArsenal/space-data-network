/**
 * Edge Relay Discovery - Loads edge relay addresses for bootstrapping
 */

/**
 * Default edge relay addresses (fallback when WASM is not available)
 */
export const DEFAULT_EDGE_RELAYS = [
  '/ip4/209.182.234.97/tcp/8080/ws/p2p/16Uiu2HAkxKtJncDGfgtFpx4mNqtrzbBBrCZ8iaKKyKuEqEHuEz5J',
  // Additional relays can be added here
];

let edgeRelaysModule: EdgeRelaysModule | null = null;
let cachedRelays: string[] | null = null;

interface EdgeRelaysModule {
  ready: Promise<void>;
  _get_edge_relays: () => number;
  UTF8ToString: (ptr: number) => string;
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
 * Load the edge relays WASM module
 */
async function loadEdgeRelaysWasm(): Promise<EdgeRelaysModule | null> {
  try {
    // Check if we're in a browser environment
    if (typeof window === 'undefined') {
      return null;
    }

    // Try to fetch the WASM file from the same origin
    const wasmPaths = [
      './edge-relays.wasm',
      '/edge-relays.wasm',
      'https://cdn.spacedatanetwork.org/edge-relays.wasm',
    ];

    for (const path of wasmPaths) {
      try {
        const response = await fetch(path);
        if (!response.ok) continue;

        const wasmBytes = await response.arrayBuffer();
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
 * Edge relay discovery class for dynamic relay management
 */
export class EdgeDiscovery {
  private knownRelays: Set<string>;
  private refreshInterval: number | null = null;

  constructor(initialRelays: string[] = DEFAULT_EDGE_RELAYS) {
    this.knownRelays = new Set(initialRelays);
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
