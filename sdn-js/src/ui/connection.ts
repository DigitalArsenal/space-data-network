/**
 * SDNConnection — isomorphic connection layer for the SDN admin UI.
 *
 * Implements the same interface whether backed by:
 *   - A local Helia node (browser / sdn-js)
 *   - A remote SDN server reachable via HTTP
 *
 * No helper service required. Browser clients seed from the demo/live
 * server then expand via libp2p/DHT automatically.
 */

import type { HeliaSDNNode } from '../helia.js'
import type { DiscoveredProvider } from '../module-delivery.js'

export type ConnectionMode = 'local' | 'server'

export interface NodeInfo {
  peer_id: string
  version?: string
  uptime?: number
  addresses: string[]
  connected_peers?: number
  network?: string
}

export interface PeerEntry {
  peer_id: string
  name?: string
  org?: string
  trust: string
  addresses: string[]
  last_seen?: string
  notes?: string
}

export interface StoreListingEntry {
  id: string
  title: string
  description?: string
  access_type: string
  price?: number
  payment_currency?: string
  provider_peer_id?: string
  plg_cid?: string
}

export interface DeliveryEvent {
  phase: string
  status: 'pending' | 'active' | 'done' | 'fail'
  detail?: string
  ts: number
}

export interface SDNConnectionOptions {
  mode?: ConnectionMode
  serverUrl?: string
  heliaNode?: HeliaSDNNode
  /** Seed server used to bootstrap DHT discovery in local mode */
  seedServer?: string
}

const DEFAULT_SEED = 'https://spaceaware.io'

export class SDNConnection {
  mode: ConnectionMode
  serverUrl: string
  heliaNode: HeliaSDNNode | null
  private seedServer: string
  private _peerCount = 0

  constructor(opts: SDNConnectionOptions = {}) {
    this.mode      = opts.mode      ?? 'local'
    this.serverUrl = opts.serverUrl ?? ''
    this.heliaNode = opts.heliaNode ?? null
    this.seedServer = opts.seedServer ?? DEFAULT_SEED
  }

  // ── Connection management ─────────────────────────────────────────

  async connectToServer(url: string): Promise<NodeInfo> {
    const info = await this._serverFetch<NodeInfo>(url + '/api/node/info')
    this.serverUrl = url
    this.mode = 'server'
    return info
  }

  /** Start a local Helia node, seeding from the demo/live server. */
  async startLocal(existingNode?: HeliaSDNNode): Promise<void> {
    if (existingNode) {
      this.heliaNode = existingNode
      this.mode = 'local'
      return
    }
    const { createHeliaSDNNode } = await import('../helia.js')
    // Bootstrap from seed server's libp2p multiaddrs
    const seedAddrs = await this._fetchSeedAddrs()
    this.heliaNode = await createHeliaSDNNode({ bootstrapAddrs: seedAddrs })
    this.mode = 'local'
    this._pollPeerCount()
  }

  private async _fetchSeedAddrs(): Promise<string[]> {
    try {
      const r = await fetch(this.seedServer + '/api/node/info',
        { headers: { 'X-Requested-With': 'XMLHttpRequest' } })
      if (!r.ok) return []
      const d = await r.json() as NodeInfo
      return d.addresses ?? []
    } catch {
      return []
    }
  }

  private _pollPeerCount() {
    const poll = () => {
      if (!this.heliaNode) return
      const h = this.heliaNode as { libp2p?: { getPeers?: () => unknown[] } }
      const peers = h.libp2p?.getPeers?.() ?? []
      this._peerCount = Array.isArray(peers) ? peers.length : 0
    }
    poll()
    setInterval(poll, 5000)
  }

  // ── API surface ───────────────────────────────────────────────────

  async getNodeInfo(): Promise<NodeInfo> {
    if (this.mode === 'local' && this.heliaNode) {
      return this._heliaNodeInfo()
    }
    return this._apiFetch<NodeInfo>('/api/node/info')
  }

  async getPeers(): Promise<PeerEntry[]> {
    if (this.mode === 'local' && this.heliaNode) {
      return this._heliaPeers()
    }
    const d = await this._apiFetch<{ peers?: PeerEntry[] }>('/api/v1/admin/peers')
    return d.peers ?? []
  }

  async getListings(): Promise<StoreListingEntry[]> {
    if (this.mode === 'local' && this.heliaNode) {
      return this._heliaListings()
    }
    const d = await this._apiFetch<{ listings?: StoreListingEntry[] } | StoreListingEntry[]>('/api/storefront/listings')
    if (Array.isArray(d)) return d
    return (d as { listings?: StoreListingEntry[] }).listings ?? []
  }

  async getProviderDescriptor(): Promise<DiscoveredProvider | null> {
    if (this.mode === 'local' && this.heliaNode) {
      return null // discovery via libp2p DHT happens in module-delivery
    }
    try {
      return await this._apiFetch<DiscoveredProvider>('/api/module-delivery/provider')
    } catch {
      return null
    }
  }

  async getPeerCount(): Promise<number> {
    if (this.mode === 'local') return this._peerCount
    try {
      const d = await this._apiFetch<NodeInfo>('/api/node/info')
      return d.connected_peers ?? 0
    } catch {
      return 0
    }
  }

  // ── Local Helia implementations ────────────────────────────────────

  private _heliaNodeInfo(): NodeInfo {
    const h = this.heliaNode as {
      libp2p?: {
        peerId?: { toString(): string }
        getMultiaddrs?: () => Array<{ toString(): string }>
        getPeers?: () => unknown[]
      }
    }
    const lp = h?.libp2p
    return {
      peer_id:         lp?.peerId?.toString() ?? 'local',
      addresses:       (lp?.getMultiaddrs?.() ?? []).map(a => a.toString()),
      connected_peers: (lp?.getPeers?.() ?? []).length,
      network:         'local-helia',
    }
  }

  private _heliaPeers(): PeerEntry[] {
    const h = this.heliaNode as {
      libp2p?: { getPeers?: () => Array<{ toString(): string }> }
    }
    const raw = h?.libp2p?.getPeers?.() ?? []
    return raw.map(p => ({
      peer_id: p.toString(),
      trust:   'standard',
      addresses: [],
    }))
  }

  private async _heliaListings(): Promise<StoreListingEntry[]> {
    // In local mode, listings come from the storefront DHT topic
    // For now return empty — this will be filled by pubsub discovery
    return []
  }

  // ── HTTP transport ─────────────────────────────────────────────────

  private async _apiFetch<T>(path: string): Promise<T> {
    const base = this.mode === 'server' && this.serverUrl
      ? this.serverUrl.replace(/\/$/, '') : ''
    return this._serverFetch<T>(base + path)
  }

  private async _serverFetch<T>(url: string): Promise<T> {
    const r = await fetch(url, { headers: { 'X-Requested-With': 'XMLHttpRequest' } })
    if (!r.ok) throw new Error(`HTTP ${r.status} ${r.statusText}`)
    return r.json() as Promise<T>
  }
}
