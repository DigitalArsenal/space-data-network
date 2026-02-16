/**
 * SDN Peer Graph Bundle
 *
 * Manages peer graph state for force-directed and tree visualizations.
 */

import { createSelector } from 'redux-bundler'

const initialState = {
  snapshot: null,
  loading: false,
  error: null,
  lastFetch: 0,
  schema: null,
  viewMode: 'force', // 'force' | 'tree'
  autoRefresh: false
}

const sdnGraphBundle = {
  name: 'sdnGraph',

  reducer (state = initialState, action) {
    switch (action.type) {
      case 'SDN_GRAPH_FETCH_STARTED':
        return { ...state, loading: true, error: null }
      case 'SDN_GRAPH_LOADED':
        return { ...state, snapshot: action.payload, loading: false, lastFetch: Date.now() }
      case 'SDN_GRAPH_FETCH_FAILED':
        return { ...state, loading: false, error: action.payload }
      case 'SDN_GRAPH_SCHEMA_LOADED':
        return { ...state, schema: action.payload }
      case 'SDN_GRAPH_SET_VIEW':
        return { ...state, viewMode: action.payload }
      case 'SDN_GRAPH_SET_AUTO_REFRESH':
        return { ...state, autoRefresh: action.payload }
      default:
        return state
    }
  },

  selectSdnGraph (state) {
    return state.sdnGraph
  },

  selectGraphSnapshot: createSelector(
    'selectSdnGraph',
    (g) => g.snapshot
  ),

  selectGraphLoading: createSelector(
    'selectSdnGraph',
    (g) => !!g.loading
  ),

  selectGraphError: createSelector(
    'selectSdnGraph',
    (g) => g.error
  ),

  selectGraphViewMode: createSelector(
    'selectSdnGraph',
    (g) => g.viewMode
  ),

  selectGraphAutoRefresh: createSelector(
    'selectSdnGraph',
    (g) => g.autoRefresh
  ),

  selectGraphSchema: createSelector(
    'selectSdnGraph',
    (g) => g.schema
  ),

  doFetchGraph: () => async ({ dispatch }) => {
    dispatch({ type: 'SDN_GRAPH_FETCH_STARTED' })
    try {
      const res = await fetch('/api/peers/graph', {
        credentials: 'same-origin',
        headers: { Accept: 'application/json' }
      })
      if (res.status === 401) {
        dispatch({ type: 'SDN_AUTH_LOGOUT' })
        return
      }
      if (!res.ok) throw new Error(`Graph fetch failed: ${res.status}`)
      const data = await res.json()
      dispatch({ type: 'SDN_GRAPH_LOADED', payload: data })
    } catch (err) {
      dispatch({ type: 'SDN_GRAPH_FETCH_FAILED', payload: err.message })
    }
  },

  doFetchGraphSchema: () => async ({ dispatch }) => {
    try {
      const res = await fetch('/api/peers/graph/schema', {
        credentials: 'same-origin'
      })
      if (!res.ok) return
      const text = await res.text()
      dispatch({ type: 'SDN_GRAPH_SCHEMA_LOADED', payload: text })
    } catch (err) {
      console.error('Failed to fetch graph schema:', err)
    }
  },

  doSetGraphView: (mode) => ({ dispatch }) => {
    dispatch({ type: 'SDN_GRAPH_SET_VIEW', payload: mode })
  },

  doSetGraphAutoRefresh: (enabled) => ({ dispatch }) => {
    dispatch({ type: 'SDN_GRAPH_SET_AUTO_REFRESH', payload: enabled })
  }
}

export default sdnGraphBundle
