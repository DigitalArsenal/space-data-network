import { describe, expect, it } from 'vitest';

import { createNetworkWorkspaceController } from '../../ui/src/controllers/network-workspace-controller';

describe('createNetworkWorkspaceController', () => {
  it('creates the controller without referencing removed connection status state', () => {
    const controller = createNetworkWorkspaceController({
      defaultProviderDescriptor: {
        publicKey: 'abcd',
        peerId: 'peer-id',
        relayAddresses: ['/ip4/127.0.0.1/tcp/4001/ws'],
      },
      getProviderDescriptorCandidates: () => [],
      getSelectedPluginListing: () => undefined,
      loadRuntimeModules: async () => {
        throw new Error('not used');
      },
      parseFirstBrowserBundle: async () => ({ canonicalModuleHashHex: 'deadbeef' }),
      root: { querySelector: () => null } as unknown as HTMLElement,
      state: {
        admin: undefined,
        deliveryEvents: [],
        observedPeers: {
          count: () => 0,
          list: () => [],
          record: () => undefined,
        },
        identity: undefined,
        node: undefined,
        provider: undefined,
      },
    });

    expect(controller).toHaveProperty('runLiveFlow');
    expect(controller).toHaveProperty('refreshProviderDescriptor');
  });
});
