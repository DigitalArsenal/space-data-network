/**
 * SDN Auth Bundle
 *
 * Cookie-based session auth backed by HD wallet challenge-response:
 * - GET  /api/auth/me
 * - POST /api/auth/challenge
 * - POST /api/auth/verify
 * - POST /api/auth/logout
 * - GET  /api/auth/status
 */

import { createSelector } from 'redux-bundler'

const initialState = {
  authEnabled: true,
  authenticated: false,
  user: null,
  identity: null,
  loading: true,
  error: null,
  status: null
}

const bytesToHex = (bytes) => Array.from(bytes || [])
  .map(b => b.toString(16).padStart(2, '0'))
  .join('')

const decodeBase64RawToBytes = (raw) => {
  // Server returns base64.RawStdEncoding (no padding); atob expects padding.
  let b64 = raw || ''
  while (b64.length % 4 !== 0) b64 += '='
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

const sdnAuthBundle = {
  name: 'sdnAuth',

  reducer (state = initialState, action) {
    switch (action.type) {
      case 'SDN_AUTH_CHECK_STARTED':
        return { ...state, loading: true, error: null }
      case 'SDN_AUTH_CHECK_FINISHED':
        return {
          ...state,
          loading: false,
          error: null,
          authEnabled: action.payload.authEnabled,
          authenticated: action.payload.authenticated,
          user: action.payload.user,
          identity: action.payload.identity || state.identity
        }
      case 'SDN_AUTH_STATUS_UPDATED':
        return { ...state, status: action.payload }
      case 'SDN_AUTH_LOGIN_STARTED':
        return { ...state, loading: true, error: null }
      case 'SDN_AUTH_LOGIN_FAILED':
        return { ...state, loading: false, error: action.payload || 'Login failed', authenticated: false, user: null }
      case 'SDN_AUTH_LOGOUT_FINISHED':
        return { ...state, loading: false, error: null, authenticated: false, user: null, identity: null }
      default:
        return state
    }
  },

  selectSdnAuth (state) {
    return state.sdnAuth
  },

  selectIsAuthenticated: createSelector(
    'selectSdnAuth',
    (auth) => !!auth.authenticated
  ),

  selectAuthUser: createSelector(
    'selectSdnAuth',
    (auth) => auth.user
  ),

  selectAuthLoading: createSelector(
    'selectSdnAuth',
    (auth) => !!auth.loading
  ),

  selectAuthError: createSelector(
    'selectSdnAuth',
    (auth) => auth.error
  ),

  selectAuthEnabled: createSelector(
    'selectSdnAuth',
    (auth) => !!auth.authEnabled
  ),

  selectAuthStatus: createSelector(
    'selectSdnAuth',
    (auth) => auth.status
  ),

  selectIsAdminUser: createSelector(
    'selectAuthUser',
    'selectAuthEnabled',
    (user, authEnabled) => {
      if (!authEnabled) return true
      const tl = (user && user.trust_level) ? String(user.trust_level).toLowerCase() : ''
      return tl === 'admin'
    }
  ),

  selectWalletIdentity: createSelector(
    'selectSdnAuth',
    (auth) => auth.identity
  ),

  doCheckSession () {
    return async ({ dispatch }) => {
      dispatch({ type: 'SDN_AUTH_CHECK_STARTED' })
      try {
        const res = await fetch('/api/auth/me', {
          method: 'GET',
          headers: { Accept: 'application/json' },
          credentials: 'same-origin'
        })

        if (res.status === 404) {
          // Auth disabled on the server (local dev mode).
          dispatch({
            type: 'SDN_AUTH_CHECK_FINISHED',
            payload: { authEnabled: false, authenticated: true, user: null }
          })
          return
        }

        if (res.status === 401) {
          dispatch({
            type: 'SDN_AUTH_CHECK_FINISHED',
            payload: { authEnabled: true, authenticated: false, user: null }
          })
          return
        }

        if (!res.ok) {
          throw new Error(`session check failed (${res.status})`)
        }

        const user = await res.json()
        dispatch({
          type: 'SDN_AUTH_CHECK_FINISHED',
          payload: { authEnabled: true, authenticated: true, user }
        })
      } catch (err) {
        console.error('SDN auth session check failed:', err)
        dispatch({
          type: 'SDN_AUTH_CHECK_FINISHED',
          payload: { authEnabled: true, authenticated: false, user: null }
        })
      }
    }
  },

  doFetchAuthStatus () {
    return async ({ dispatch }) => {
      try {
        const res = await fetch('/api/auth/status', {
          method: 'GET',
          headers: { Accept: 'application/json' },
          credentials: 'same-origin'
        })
        if (!res.ok) return
        const status = await res.json()
        dispatch({ type: 'SDN_AUTH_STATUS_UPDATED', payload: status })
      } catch (_) {}
    }
  },

  doWalletLogin (identity) {
    return async ({ dispatch }) => {
      dispatch({ type: 'SDN_AUTH_LOGIN_STARTED' })
      try {
        if (!identity) throw new Error('missing identity')
        if (!identity.xpub) throw new Error('missing xpub')
        if (!identity.signingPublicKey) throw new Error('missing signingPublicKey')
        if (typeof identity.sign !== 'function') throw new Error('missing sign()')

        const xpub = String(identity.xpub)
        const pubKeyHex = bytesToHex(identity.signingPublicKey)

        const challengeResp = await fetch('/api/auth/challenge', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'X-Requested-With': 'XMLHttpRequest'
          },
          credentials: 'same-origin',
          body: JSON.stringify({
            xpub,
            client_pubkey_hex: pubKeyHex,
            ts: Math.floor(Date.now() / 1000)
          })
        })
        const challengeData = await challengeResp.json().catch(() => ({}))
        if (!challengeResp.ok) {
          throw new Error(challengeData.message || 'challenge failed')
        }

        const challengeBytes = decodeBase64RawToBytes(challengeData.challenge)
        const signature = await identity.sign(challengeBytes)
        const sigHex = bytesToHex(signature)

        const verifyResp = await fetch('/api/auth/verify', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'X-Requested-With': 'XMLHttpRequest'
          },
          credentials: 'same-origin',
          body: JSON.stringify({
            challenge_id: challengeData.challenge_id,
            xpub,
            client_pubkey_hex: pubKeyHex,
            challenge: challengeData.challenge,
            signature_hex: sigHex
          })
        })
        const verifyData = await verifyResp.json().catch(() => ({}))
        if (!verifyResp.ok) {
          throw new Error(verifyData.message || 'verification failed')
        }

        // Cookie is set by the server; just mark authenticated in UI.
        dispatch({
          type: 'SDN_AUTH_CHECK_FINISHED',
          payload: { authEnabled: true, authenticated: true, user: verifyData.user || null, identity }
        })
      } catch (err) {
        dispatch({ type: 'SDN_AUTH_LOGIN_FAILED', payload: err && err.message ? err.message : 'Login failed' })
      }
    }
  },

  doLogout () {
    return async ({ dispatch }) => {
      try {
        await fetch('/api/auth/logout', {
          method: 'POST',
          headers: { 'X-Requested-With': 'XMLHttpRequest' },
          credentials: 'same-origin'
        })
      } catch (_) {}
      dispatch({ type: 'SDN_AUTH_LOGOUT_FINISHED' })
    }
  }
}

export default sdnAuthBundle
