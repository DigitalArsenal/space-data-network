/**
 * SDN Trust Bundle
 *
 * Admin-only UI state for:
 * - Trusted peer registry (/api/peers, /api/groups, /api/blocklist, /api/settings)
 * - Wallet-auth user management (/api/auth/users)
 */

import { createSelector } from 'redux-bundler'

const initialState = {
  peers: [],
  groups: [],
  blocklist: [],
  settings: null,
  users: [],
  nodeInfo: null,
  loading: false,
  error: null,
  lastFetch: 0
}

const withXrw = (headers = {}) => ({
  ...headers,
  Accept: 'application/json',
  'X-Requested-With': 'XMLHttpRequest'
})

const withAcceptJSON = (headers = {}) => ({
  ...headers,
  Accept: 'application/json'
})

const sdnTrustBundle = {
  name: 'sdnTrust',

  reducer (state = initialState, action) {
    switch (action.type) {
      case 'SDN_TRUST_FETCH_STARTED':
        return { ...state, loading: true, error: null }
      case 'SDN_TRUST_FETCH_FINISHED':
        return {
          ...state,
          ...action.payload,
          loading: false,
          error: null,
          lastFetch: Date.now()
        }
      case 'SDN_TRUST_FETCH_FAILED':
        return { ...state, loading: false, error: action.payload || 'Failed to load trust data' }
      default:
        return state
    }
  },

  selectSdnTrust (state) {
    return state.sdnTrust
  },

  selectTrustedPeers: createSelector(
    'selectSdnTrust',
    (t) => t.peers || []
  ),

  selectPeerGroups: createSelector(
    'selectSdnTrust',
    (t) => t.groups || []
  ),

  selectBlockedPeers: createSelector(
    'selectSdnTrust',
    (t) => t.blocklist || []
  ),

  selectTrustSettings: createSelector(
    'selectSdnTrust',
    (t) => t.settings
  ),

  selectAuthUsers: createSelector(
    'selectSdnTrust',
    (t) => t.users || []
  ),

  selectTrustNodeInfo: createSelector(
    'selectSdnTrust',
    (t) => t.nodeInfo
  ),

  selectTrustLoading: createSelector(
    'selectSdnTrust',
    (t) => !!t.loading
  ),

  selectTrustError: createSelector(
    'selectSdnTrust',
    (t) => t.error
  ),

  selectTrustLevelByPeerId: createSelector(
    'selectTrustedPeers',
    (peers) => {
      const out = {}
      for (const p of peers || []) {
        if (p && p.id) out[String(p.id)] = p.trust_level
      }
      return out
    }
  ),

  // Fetch trust data when admins view Trust or Peers pages (keeps badges fresh).
  reactSdnTrustAutoFetch: createSelector(
    'selectRouteInfo',
    'selectIsAdminUser',
    'selectSdnTrust',
    (routeInfo, isAdmin, trust) => {
      if (!isAdmin) return
      const url = routeInfo && routeInfo.url ? String(routeInfo.url) : ''
      if (!(url.startsWith('/trust') || url.startsWith('/peers'))) return
      if (trust && trust.loading) return
      const last = trust && trust.lastFetch ? trust.lastFetch : 0
      if (Date.now() - last < 30 * 1000) return
      return { actionCreator: 'doFetchTrustData' }
    }
  ),

  doFetchTrustData () {
    return async ({ dispatch, store }) => {
      dispatch({ type: 'SDN_TRUST_FETCH_STARTED' })
      try {
        const resps = await Promise.all([
          fetch('/api/peers', { headers: withAcceptJSON(), credentials: 'same-origin' }),
          fetch('/api/groups', { headers: withAcceptJSON(), credentials: 'same-origin' }),
          fetch('/api/blocklist', { headers: withAcceptJSON(), credentials: 'same-origin' }),
          fetch('/api/settings', { headers: withAcceptJSON(), credentials: 'same-origin' }),
          fetch('/api/auth/users', { headers: withAcceptJSON(), credentials: 'same-origin' }),
          fetch('/api/node/info', { headers: withAcceptJSON(), credentials: 'same-origin' })
        ])

        // If any endpoint indicates the session is gone, bounce to login.
        if (resps.some(r => r.status === 401)) {
          await store.doLogout()
          dispatch({ type: 'SDN_TRUST_FETCH_FAILED', payload: 'Not authenticated' })
          return
        }

        const [peersRes, groupsRes, blocklistRes, settingsRes, usersRes, nodeInfoRes] = resps
        if (!peersRes.ok) throw new Error(`GET /api/peers failed (${peersRes.status})`)
        if (!groupsRes.ok) throw new Error(`GET /api/groups failed (${groupsRes.status})`)
        if (!blocklistRes.ok) throw new Error(`GET /api/blocklist failed (${blocklistRes.status})`)
        if (!settingsRes.ok) throw new Error(`GET /api/settings failed (${settingsRes.status})`)
        if (!usersRes.ok) throw new Error(`GET /api/auth/users failed (${usersRes.status})`)
        if (!nodeInfoRes.ok) throw new Error(`GET /api/node/info failed (${nodeInfoRes.status})`)

        const [peers, groups, blocklist, settings, users, nodeInfo] = await Promise.all([
          peersRes.json(),
          groupsRes.json(),
          blocklistRes.json(),
          settingsRes.json(),
          usersRes.json(),
          nodeInfoRes.json()
        ])

        dispatch({
          type: 'SDN_TRUST_FETCH_FINISHED',
          payload: { peers, groups, blocklist, settings, users, nodeInfo }
        })
      } catch (err) {
        dispatch({
          type: 'SDN_TRUST_FETCH_FAILED',
          payload: err && err.message ? err.message : 'Failed to load trust data'
        })
      }
    }
  },

  doAddTrustedPeer (peer) {
    return async ({ store }) => {
      const res = await fetch('/api/peers', {
        method: 'POST',
        headers: withXrw({ 'Content-Type': 'application/json' }),
        credentials: 'same-origin',
        body: JSON.stringify(peer)
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `add peer failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  },

  doUpdateTrustedPeer (peerId, patch) {
    return async ({ store }) => {
      const res = await fetch(`/api/peers/${encodeURIComponent(peerId)}`, {
        method: 'PUT',
        headers: withXrw({ 'Content-Type': 'application/json' }),
        credentials: 'same-origin',
        body: JSON.stringify(patch)
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `update peer failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  },

  doRemoveTrustedPeer (peerId) {
    return async ({ store }) => {
      const res = await fetch(`/api/peers/${encodeURIComponent(peerId)}`, {
        method: 'DELETE',
        headers: withXrw(),
        credentials: 'same-origin'
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok && res.status !== 204) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `remove peer failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  },

  doAddGroup (group) {
    return async ({ store }) => {
      const res = await fetch('/api/groups', {
        method: 'POST',
        headers: withXrw({ 'Content-Type': 'application/json' }),
        credentials: 'same-origin',
        body: JSON.stringify(group)
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `add group failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  },

  doRemoveGroup (groupName) {
    return async ({ store }) => {
      const res = await fetch(`/api/groups/${encodeURIComponent(groupName)}`, {
        method: 'DELETE',
        headers: withXrw(),
        credentials: 'same-origin'
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok && res.status !== 204) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `remove group failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  },

  doBlockPeer (peerId) {
    return async ({ store }) => {
      const res = await fetch('/api/blocklist', {
        method: 'POST',
        headers: withXrw({ 'Content-Type': 'application/json' }),
        credentials: 'same-origin',
        body: JSON.stringify({ peer_id: peerId })
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `block peer failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  },

  doUnblockPeer (peerId) {
    return async ({ store }) => {
      const res = await fetch(`/api/blocklist/${encodeURIComponent(peerId)}`, {
        method: 'DELETE',
        headers: withXrw(),
        credentials: 'same-origin'
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok && res.status !== 204) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `unblock peer failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  },

  doSetStrictMode (strictMode) {
    return async ({ store }) => {
      const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: withXrw({ 'Content-Type': 'application/json' }),
        credentials: 'same-origin',
        body: JSON.stringify({ strict_mode: !!strictMode })
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `update settings failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  },

  doAddAuthUser (user) {
    return async ({ store }) => {
      const res = await fetch('/api/auth/users', {
        method: 'POST',
        headers: withXrw({ 'Content-Type': 'application/json' }),
        credentials: 'same-origin',
        body: JSON.stringify(user)
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `add user failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  },

  doRemoveAuthUser (xpub) {
    return async ({ store }) => {
      const res = await fetch(`/api/auth/users/${encodeURIComponent(xpub)}`, {
        method: 'DELETE',
        headers: withXrw(),
        credentials: 'same-origin'
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `remove user failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  },

  doUpdateAuthUserTrust (xpub, trustLevel) {
    return async ({ store }) => {
      const res = await fetch(`/api/auth/users/${encodeURIComponent(xpub)}`, {
        method: 'PUT',
        headers: withXrw({ 'Content-Type': 'application/json' }),
        credentials: 'same-origin',
        body: JSON.stringify({ trust_level: trustLevel })
      })
      if (res.status === 401) {
        await store.doLogout()
        return
      }
      if (!res.ok) {
        const txt = await res.text().catch(() => '')
        throw new Error(txt || `update user failed (${res.status})`)
      }
      await store.doFetchTrustData()
    }
  }
}

export default sdnTrustBundle
