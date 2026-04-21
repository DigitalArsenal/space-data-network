import { createSelector } from '../../../../../webui/node_modules/redux-bundler/dist/redux-bundler.js';

import { trustedPeerListToPeerLocationsForSwarm } from '../peer-source.js';

function createPeersLocations () {
  const bundle = {
    name: 'peerLocations',
    reducer: (state = {}, _action) => state,
  };

  bundle.selectPeerLocations = () => ({});

  bundle.selectPeerLocationsForSwarm = createSelector(
    'selectPeers',
    (peers) => trustedPeerListToPeerLocationsForSwarm(peers?.map((peer) => ({
      id: peer?.peer,
      addrs: peer?.addr ? [peer.addr] : [],
      trust_level: peer?.sdnMeta?.trustLevel,
      name: peer?.sdnMeta?.name,
      organization: peer?.sdnMeta?.organization,
      metadata: {
        agent_version: peer?.identify?.AgentVersion,
        protocols: Array.isArray(peer?.streams)
          ? peer.streams
            .map((stream) => stream?.protocol)
            .filter(Boolean)
            .join(',')
          : '',
      },
    })) ?? []),
  );

  bundle.selectPeersCoordinates = createSelector(
    'selectPeerLocationsForSwarm',
    () => [],
  );

  return bundle;
}

export default createPeersLocations;
