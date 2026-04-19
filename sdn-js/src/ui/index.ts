/**
 * SDN Admin UI — isomorphic UI entry point.
 *
 * Can be used:
 *   1. Hosted on sdn-server at /admin (server talks to local APIs)
 *   2. In browser via sdn-js with Helia for local P2P mode
 *
 * Usage (browser / sdn-js):
 *   import { mountAdminUI } from '@spacedatanetwork/sdn-js/ui'
 *   await mountAdminUI(document.getElementById('sdn-admin'), { mode: 'local' })
 *
 * Usage (hosted server):
 *   The admin.go template already includes the full UI. sdn-js merely
 *   enriches it by exposing window.SDNConnection when loaded.
 */

export { SDNConnection } from './connection.js'
export type {
  ConnectionMode,
  NodeInfo,
  PeerEntry,
  StoreListingEntry,
  DeliveryEvent,
  SDNConnectionOptions,
} from './connection.js'

import { SDNConnection } from './connection.js'
import type { SDNConnectionOptions } from './connection.js'

/**
 * Mount the SDN admin UI into `container`.
 *
 * In server mode the UI drives the sdn-server HTTP APIs.
 * In local mode it starts a Helia node, seeds from the live server,
 * and expands discovery via libp2p / DHT.
 */
export async function mountAdminUI(
  container: HTMLElement,
  opts: SDNConnectionOptions & { seedServer?: string } = {}
): Promise<SDNConnection> {
  const conn = new SDNConnection(opts)

  if (opts.mode === 'local' || !opts.serverUrl) {
    await conn.startLocal(opts.heliaNode)
  } else {
    await conn.connectToServer(opts.serverUrl)
  }

  // Expose on window so the admin.go template JS can discover it
  // without requiring a module bundler.
  if (typeof window !== 'undefined') {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(window as any).SDNConnection = conn
  }

  return conn
}

/**
 * Bootstrap for the admin.go template:
 * If this module is loaded in a page that already has the admin UI HTML
 * rendered, wire the connection up to the existing JS state object (S).
 */
export function wireToAdminTemplate(conn: SDNConnection): void {
  if (typeof window === 'undefined') return
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const S = (window as any).S
  if (!S) return

  // Patch apiFetch to route through SDNConnection's HTTP transport
  // or Helia when in local mode.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(window as any).__sdnConn = conn

  // Notify the template of the initial peer count
  const badge = document.getElementById('peer-count')
  const update = async () => {
    const n = await conn.getPeerCount()
    if (badge) badge.textContent = String(n)
    if (S) S.peerCount = n
  }
  update()
  setInterval(update, 10000)
}
