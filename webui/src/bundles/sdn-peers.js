/**
 * SDN Peers Bundle - Phase 17.2: SDN vs IPFS Peer Separation
 *
 * Detects which peers support the SDN protocol:
 *   /spacedatanetwork/sds-exchange/1.0.0
 *
 * Provides selectors for SDN peers vs standard IPFS peers.
 * Caches SDN peer status to avoid repeated protocol queries.
 */
import { createSelector } from 'redux-bundler'
import ms from 'milliseconds'

const SDN_PROTOCOL = '/spacedatanetwork/sds-exchange/1.0.0'
const SDN_PEERS_CACHE_KEY = 'sdn-peers-cache'
const CACHE_TTL = ms.minutes(5)

// Load cache from localStorage
function loadCache () {
  try {
    const raw = window.localStorage.getItem(SDN_PEERS_CACHE_KEY)
    if (raw) {
      const parsed = JSON.parse(raw)
      // Prune expired entries
      const now = Date.now()
      const pruned = {}
      for (const [key, val] of Object.entries(parsed)) {
        if (now - val.ts < CACHE_TTL) {
          pruned[key] = val
        }
      }
      return pruned
    }
  } catch (_) {}
  return {}
}

function saveCache (cache) {
  try {
    window.localStorage.setItem(SDN_PEERS_CACHE_KEY, JSON.stringify(cache))
  } catch (_) {}
}

const initialState = {
  sdnPeerIds: {}, // { peerId: { isSdn: bool, ts: number } }
  lastCheck: 0,
  isChecking: false
}

const sdnPeersBundle = {
  name: 'sdnPeers',

  reducer (state, action) {
    if (state == null) {
      return { ...initialState, sdnPeerIds: loadCache() }
    }
    switch (action.type) {
      case 'SDN_PEERS_CHECK_STARTED':
        return { ...state, isChecking: true }
      case 'SDN_PEERS_CHECK_FINISHED':
        return {
          ...state,
          sdnPeerIds: { ...state.sdnPeerIds, ...action.payload },
          lastCheck: Date.now(),
          isChecking: false
        }
      case 'SDN_PEERS_CHECK_FAILED':
        return { ...state, isChecking: false }
      default:
        return state
    }
  },

  doCheckSdnPeers () {
    return async ({ dispatch, getIpfs, store }) => {
      dispatch({ type: 'SDN_PEERS_CHECK_STARTED' })
      try {
        const peers = store.selectPeers()
        if (!Array.isArray(peers)) {
          dispatch({ type: 'SDN_PEERS_CHECK_FAILED' })
          return
        }

        const currentCache = store.selectSdnPeerCache()
        const updates = {}
        const now = Date.now()

        for (const peer of peers) {
          const peerId = peer.peer?.toString() || peer.id?.toString()
          if (!peerId) continue

          // Use cache if fresh
          const cached = currentCache[peerId]
          if (cached && (now - cached.ts) < CACHE_TTL) {
            continue
          }

          // Check if protocols include SDN protocol
          // The peer data from swarm.peers({ identify: true }) may include protocols
          let isSdn = false
          if (peer.protocols && Array.isArray(peer.protocols)) {
            isSdn = peer.protocols.includes(SDN_PROTOCOL)
          } else if (typeof peer.protocols === 'string') {
            isSdn = peer.protocols.includes(SDN_PROTOCOL)
          }

          updates[peerId] = { isSdn, ts: now }
        }

        const merged = { ...currentCache, ...updates }
        saveCache(merged)
        dispatch({ type: 'SDN_PEERS_CHECK_FINISHED', payload: updates })
      } catch (err) {
        console.error('SDN peer check failed:', err)
        dispatch({ type: 'SDN_PEERS_CHECK_FAILED' })
      }
    }
  },

  selectSdnPeerCache (state) {
    return state.sdnPeers.sdnPeerIds
  },

  selectSdnPeerIds: createSelector(
    'selectSdnPeerCache',
    (cache) => {
      const ids = new Set()
      for (const [id, val] of Object.entries(cache)) {
        if (val.isSdn) ids.add(id)
      }
      return ids
    }
  ),

  selectSdnPeersCount: createSelector(
    'selectSdnPeerIds',
    (sdnIds) => sdnIds.size
  ),

  selectIpfsOnlyPeersCount: createSelector(
    'selectPeersCount',
    'selectSdnPeersCount',
    (total, sdnCount) => Math.max(0, total - sdnCount)
  ),

  // Re-check peers periodically when on peers or status page
  reactSdnPeersCheck: createSelector(
    'selectAppTime',
    'selectRouteInfo',
    'selectIpfsConnected',
    'selectPeers',
    (appTime, routeInfo, ipfsConnected, peers, state) => {
      if (!ipfsConnected || !Array.isArray(peers) || peers.length === 0) return
      const sdnState = state?.sdnPeers || {}
      const lastCheck = sdnState.lastCheck || 0
      const isChecking = sdnState.isChecking

      if (!isChecking && (appTime - lastCheck) > ms.seconds(15)) {
        return { actionCreator: 'doCheckSdnPeers' }
      }
    }
  )
}

export default sdnPeersBundle
