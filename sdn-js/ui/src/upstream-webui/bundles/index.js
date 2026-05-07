import { composeBundles, createCacheBundle, createSelector } from 'redux-bundler';
import ipfsProvider from '../../../../../webui/src/bundles/ipfs-provider.js';
import appIdle from '../../../../../webui/src/bundles/app-idle.js';
import nodeBandwidthChartBundle from '../../../../../webui/src/bundles/node-bandwidth-chart.js';
import nodeBandwidthBundle from '../../../../../webui/src/bundles/node-bandwidth.js';
import peersBundle from './peers.js';
import peerLocationsBundle from './peer-locations.js';
import pinningBundle from '../../../../../webui/src/bundles/pinning.js';
import routesBundle from '../overrides/bundles/routes.js';
import redirectsBundle from './redirects.js';
import filesBundle from '../../../../../webui/src/bundles/files/index.js';
import configBundle from '../../../../../webui/src/bundles/config.js';
import configSaveBundle from '../../../../../webui/src/bundles/config-save.js';
import toursBundle from '../../../../../webui/src/bundles/tours.js';
import notifyBundle from '../../../../../webui/src/bundles/notify.js';
import connectedBundle from '../../../../../webui/src/bundles/connected.js';
import retryInitBundle from '../../../../../webui/src/bundles/retry-init.js';
import bundleCache from '../../../../../webui/src/lib/bundle-cache.js';
import ipfsDesktop from '../../../../../webui/src/bundles/ipfs-desktop.js';
import repoStats from '../../../../../webui/src/bundles/repo-stats.js';
import createAnalyticsBundle from '../../../../../webui/src/bundles/analytics.js';
import experimentsBundle from '../../../../../webui/src/bundles/experiments.js';
import cliTutorModeBundle from '../../../../../webui/src/bundles/cli-tutor-mode.js';
import gatewayBundle from '../../../../../webui/src/bundles/gateway.js';
import ipnsBundle from '../../../../../webui/src/bundles/ipns.js';
import { contextBridge } from '../../../../../webui/src/helpers/context-bridge.jsx';

export default composeBundles(
  {
    name: 'bridgedContextCatchAll',
    reactRouteInfoToBridge: createSelector(
      'selectRouteInfo',
      (routeInfo) => {
        contextBridge.setContext('selectRouteInfo', routeInfo);
      },
    ),
  },
  createCacheBundle({
    cacheFn: bundleCache.set,
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
);
