import { composeBundles, createCacheBundle, createSelector } from 'redux-bundler'
import ipfsProvider from './ipfs-provider.js'
import appIdle from './app-idle.js'
import nodeBandwidthChartBundle from './node-bandwidth-chart.js'
import nodeBandwidthBundle from './node-bandwidth.js'
import peersBundle from './peers.js'
import peerLocationsBundle from './peer-locations.js'
import pinningBundle from './pinning.js'
import routesBundle from './routes.js'
import redirectsBundle from './redirects.js'
import filesBundle from './files/index.js'
import configBundle from './config.js'
import configSaveBundle from './config-save.js'
import toursBundle from './tours.js'
import notifyBundle from './notify.js'
import connectedBundle from './connected.js'
import retryInitBundle from './retry-init.js'
import bundleCache from '../lib/bundle-cache.js'
import ipfsDesktop from './ipfs-desktop.js'
import repoStats from './repo-stats.js'
import createAnalyticsBundle from './analytics.js'
import experimentsBundle from './experiments.js'
import cliTutorModeBundle from './cli-tutor-mode.js'
import gatewayBundle from './gateway.js'
import ipnsBundle from './ipns.js'
import sdnThemeBundle from './sdn-theme.js'
import sdnAuthBundle from './sdn-auth.js'
import sdnTrustBundle from './sdn-trust.js'
import sdnPeersBundle from './sdn-peers.js'
import sdnStatsBundle from './sdn-stats.js'
import sdnContextBundle from './sdn-context.js'
import { contextBridge } from '../helpers/context-bridge'

export default composeBundles(
  {
    name: 'bridgedContextCatchAll',
    reactRouteInfoToBridge: createSelector(
      'selectRouteInfo',
      (routeInfo) => {
        contextBridge.setContext('selectRouteInfo', routeInfo)
      }
    )
  },
  createCacheBundle({
    cacheFn: bundleCache.set
  }),
  appIdle({ idleTimeout: 5000 }),
  ipfsProvider,
  routesBundle,
  redirectsBundle,
  toursBundle,
  filesBundle(),
  configBundle,
  configSaveBundle,
  gatewayBundle,
  nodeBandwidthBundle,
  nodeBandwidthChartBundle(),
  peersBundle,
  peerLocationsBundle(),
  pinningBundle,
  notifyBundle,
  connectedBundle,
  retryInitBundle,
  experimentsBundle,
  ipfsDesktop,
  repoStats,
  cliTutorModeBundle,
  createAnalyticsBundle({}),
  ipnsBundle,
  sdnThemeBundle,
  sdnAuthBundle,
  sdnTrustBundle,
  sdnPeersBundle,
  sdnStatsBundle,
  sdnContextBundle
)
