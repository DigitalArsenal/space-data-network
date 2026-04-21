import { createAsyncResourceBundle, createSelector } from '../../../../../webui/node_modules/redux-bundler/dist/redux-bundler.js';
import ms from '../../../../../webui/node_modules/milliseconds/milliseconds.js';

import { createHostedRegistryPeerSource, normalizeTrustedPeerToSwarmPeer } from '../peer-source.js';

const peerSource = createHostedRegistryPeerSource();
const peersTTL = peerSource.staleMs ?? ms.seconds(10);

const bundle = createAsyncResourceBundle({
  name: 'peers',
  actionBaseType: 'PEERS',
  getPromise: async () => {
    const peers = await peerSource.listPeers();
    return peers.map(normalizeTrustedPeerToSwarmPeer).filter((peer) => peer.peer);
  },
  staleAfter: peersTTL,
  persist: false,
  checkIfOnline: false,
});

const asyncResourceReducer = bundle.reducer;

bundle.reducer = (state, action) => {
  const asyncResult = asyncResourceReducer(state, action);

  if (action.type === 'SET_SELECTED_PEER') {
    return { ...asyncResult, selectedPeers: action.payload };
  }

  return asyncResult;
};

bundle.selectPeersCount = createSelector(
  'selectPeers',
  (peers) => Array.isArray(peers) ? peers.length : 0,
);

bundle.reactPeersFetchWhenIdle = createSelector(
  'selectPeersShouldUpdate',
  (shouldUpdate) => {
    if (shouldUpdate) {
      return { actionCreator: 'doFetchPeers' };
    }
  },
);

bundle.reactPeersFetchWhenActive = createSelector(
  'selectAppTime',
  'selectRouteInfo',
  'selectPeersRaw',
  (appTime, routeInfo, peersInfo) => {
    const lastSuccess = peersInfo.lastSuccess || 0;
    if (routeInfo.url === '/peers' && !peersInfo.isLoading && appTime - lastSuccess > ms.seconds(5)) {
      return { actionCreator: 'doFetchPeers' };
    }
  },
);

bundle.selectSelectedPeers = (state) => state.peers.selectedPeers;

bundle.doSetSelectedPeers = (peer) => ({ dispatch }) => {
  dispatch({ type: 'SET_SELECTED_PEER', payload: peer });
};

export default bundle;
