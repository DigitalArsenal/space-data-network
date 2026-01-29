/**
 * SDN Stats Bundle - Phase 17.3: SDN Dashboard / Status Overview
 *
 * Provides SDN-specific network statistics:
 * - Active PubSub topics
 * - Data volume
 * - Schema types supported
 */
import { createSelector } from 'redux-bundler'
// ms import removed - using inline values

const SDN_PUBSUB_TOPICS = [
  'sdn/omm',
  'sdn/cdm',
  'sdn/epm',
  'sdn/rfm',
  'sdn/tca',
  'sdn/tle'
]

const initialState = {
  pubsubTopics: [],
  pubsubPeers: {},
  dataVolume: 0,
  schemaTypes: ['OMM', 'CDM', 'EPM', 'RFM', 'TCA', 'TLE'],
  lastFetch: 0,
  isFetching: false,
  epmIdentity: null
}

const sdnStatsBundle = {
  name: 'sdnStats',

  reducer (state = initialState, action) {
    switch (action.type) {
      case 'SDN_STATS_FETCH_STARTED':
        return { ...state, isFetching: true }
      case 'SDN_STATS_FETCH_FINISHED':
        return {
          ...state,
          ...action.payload,
          lastFetch: Date.now(),
          isFetching: false
        }
      case 'SDN_STATS_FETCH_FAILED':
        return { ...state, isFetching: false }
      default:
        return state
    }
  },

  doFetchSdnStats () {
    return async ({ dispatch, getIpfs }) => {
      dispatch({ type: 'SDN_STATS_FETCH_STARTED' })
      try {
        const ipfs = getIpfs()
        let pubsubTopics = []
        const pubsubPeers = {}

        // Try to get PubSub topics (may fail if not enabled)
        try {
          const topics = await ipfs.pubsub.ls()
          pubsubTopics = topics || []

          // Get peer counts per SDN topic
          for (const topic of pubsubTopics) {
            if (SDN_PUBSUB_TOPICS.some(t => topic.includes(t))) {
              try {
                const peers = await ipfs.pubsub.peers(topic)
                pubsubPeers[topic] = peers?.length || 0
              } catch (_) {
                pubsubPeers[topic] = 0
              }
            }
          }
        } catch (_) {
          // PubSub may not be enabled
        }

        // Get repo stats for data volume estimate
        let dataVolume = 0
        try {
          const stats = await ipfs.repo.stat()
          dataVolume = Number(stats.repoSize) || 0
        } catch (_) {}

        dispatch({
          type: 'SDN_STATS_FETCH_FINISHED',
          payload: {
            pubsubTopics,
            pubsubPeers,
            dataVolume
          }
        })
      } catch (err) {
        console.error('SDN stats fetch failed:', err)
        dispatch({ type: 'SDN_STATS_FETCH_FAILED' })
      }
    }
  },

  selectSdnStats (state) {
    return state.sdnStats
  },

  selectSdnPubsubTopics: createSelector(
    'selectSdnStats',
    (stats) => stats.pubsubTopics.filter(t =>
      SDN_PUBSUB_TOPICS.some(sdn => t.includes(sdn))
    )
  ),

  selectSdnActivePubsubCount: createSelector(
    'selectSdnPubsubTopics',
    (topics) => topics.length
  ),

  selectSdnDataVolume: createSelector(
    'selectSdnStats',
    (stats) => stats.dataVolume
  ),

  selectSdnSchemaTypes: createSelector(
    'selectSdnStats',
    (stats) => stats.schemaTypes
  ),

  // Fetch stats periodically on status page
  reactSdnStatsFetch: createSelector(
    'selectAppTime',
    'selectRouteInfo',
    'selectIpfsConnected',
    (appTime, routeInfo, ipfsConnected) => {
      if (!ipfsConnected) return
      if (routeInfo.url === '/' || routeInfo.url.startsWith('/status')) {
        // We use appTime implicitly to trigger periodic updates
        return { actionCreator: 'doFetchSdnStats' }
      }
    }
  )
}

export default sdnStatsBundle
