/**
 * SDN EPM (Entity Profile Message) Bundle
 *
 * Manages the node's identity card / EPM state:
 * - Node EPM (JSON, vCard, QR)
 * - Peer EPMs
 * - Profile updates
 */

import { createSelector } from 'redux-bundler'

const initialState = {
  nodeEpm: null,
  nodeVCard: null,
  nodeQrUrl: null,
  peerEpms: {},
  loading: false,
  error: null,
  lastFetch: 0
}

const sdnEpmBundle = {
  name: 'sdnEpm',

  reducer (state = initialState, action) {
    switch (action.type) {
      case 'SDN_EPM_FETCH_STARTED':
        return { ...state, loading: true, error: null }
      case 'SDN_EPM_NODE_LOADED':
        return { ...state, nodeEpm: action.payload, loading: false, lastFetch: Date.now() }
      case 'SDN_EPM_VCARD_LOADED':
        return { ...state, nodeVCard: action.payload, loading: false }
      case 'SDN_EPM_QR_LOADED':
        return { ...state, nodeQrUrl: action.payload, loading: false }
      case 'SDN_EPM_PEER_LOADED':
        return {
          ...state,
          peerEpms: { ...state.peerEpms, [action.payload.peerId]: action.payload.data },
          loading: false
        }
      case 'SDN_EPM_FETCH_FAILED':
        return { ...state, loading: false, error: action.payload }
      case 'SDN_EPM_PROFILE_UPDATED':
        return { ...state, nodeEpm: action.payload, lastFetch: Date.now() }
      default:
        return state
    }
  },

  selectSdnEpm (state) {
    return state.sdnEpm
  },

  selectNodeEpm: createSelector(
    'selectSdnEpm',
    (e) => e.nodeEpm
  ),

  selectNodeVCard: createSelector(
    'selectSdnEpm',
    (e) => e.nodeVCard
  ),

  selectNodeQrUrl: createSelector(
    'selectSdnEpm',
    (e) => e.nodeQrUrl
  ),

  selectPeerEpms: createSelector(
    'selectSdnEpm',
    (e) => e.peerEpms || {}
  ),

  selectEpmLoading: createSelector(
    'selectSdnEpm',
    (e) => e.loading
  ),

  doFetchNodeEPM: () => async ({ dispatch }) => {
    dispatch({ type: 'SDN_EPM_FETCH_STARTED' })
    try {
      const res = await fetch('/api/node/epm/json', {
        credentials: 'same-origin',
        headers: { Accept: 'application/json' }
      })
      if (res.status === 401) {
        dispatch({ type: 'SDN_AUTH_LOGOUT' })
        return
      }
      if (!res.ok) throw new Error(`EPM fetch failed: ${res.status}`)
      const data = await res.json()
      dispatch({ type: 'SDN_EPM_NODE_LOADED', payload: data })
    } catch (err) {
      dispatch({ type: 'SDN_EPM_FETCH_FAILED', payload: err.message })
    }
  },

  doFetchNodeVCard: () => async ({ dispatch }) => {
    try {
      const res = await fetch('/api/node/epm/vcard', {
        credentials: 'same-origin'
      })
      if (!res.ok) throw new Error(`vCard fetch failed: ${res.status}`)
      const text = await res.text()
      dispatch({ type: 'SDN_EPM_VCARD_LOADED', payload: text })
    } catch (err) {
      dispatch({ type: 'SDN_EPM_FETCH_FAILED', payload: err.message })
    }
  },

  doFetchNodeQR: () => async ({ dispatch }) => {
    try {
      const res = await fetch('/api/node/epm/qr', {
        credentials: 'same-origin'
      })
      if (!res.ok) throw new Error(`QR fetch failed: ${res.status}`)
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      dispatch({ type: 'SDN_EPM_QR_LOADED', payload: url })
    } catch (err) {
      dispatch({ type: 'SDN_EPM_FETCH_FAILED', payload: err.message })
    }
  },

  doUpdateNodeProfile: (profile) => async ({ dispatch }) => {
    try {
      const res = await fetch('/api/node/epm', {
        method: 'PUT',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(profile)
      })
      if (!res.ok) throw new Error(`Profile update failed: ${res.status}`)
      const data = await res.json()
      dispatch({ type: 'SDN_EPM_PROFILE_UPDATED', payload: data })
      return data
    } catch (err) {
      dispatch({ type: 'SDN_EPM_FETCH_FAILED', payload: err.message })
      throw err
    }
  },

  doFetchPeerEPMVCard: (peerId) => async ({ dispatch }) => {
    try {
      const res = await fetch(`/api/peers/${encodeURIComponent(peerId)}/epm/vcard`, {
        credentials: 'same-origin'
      })
      if (!res.ok) throw new Error(`Peer EPM fetch failed: ${res.status}`)
      const text = await res.text()
      dispatch({ type: 'SDN_EPM_PEER_LOADED', payload: { peerId, data: text } })
      return text
    } catch (err) {
      dispatch({ type: 'SDN_EPM_FETCH_FAILED', payload: err.message })
    }
  },

  doDownloadPeerVCard: (peerId) => async () => {
    try {
      const res = await fetch(`/api/peers/${encodeURIComponent(peerId)}/epm/vcard`, {
        credentials: 'same-origin'
      })
      if (!res.ok) return
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `peer-${peerId.slice(0, 8)}.vcf`
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      console.error('Failed to download vCard:', err)
    }
  }
}

export default sdnEpmBundle
