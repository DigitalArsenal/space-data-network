import { describe, expect, it } from 'vitest';

import { createAppState } from '../../ui/src/state/app-state';

describe('createAppState', () => {
  it('tracks provider, delivery events, and store selection through explicit mutators', () => {
    const state = createAppState();

    state.setProvider({
      publicKey: '02abc',
      peerId: '16Uiu2HAmTest',
      relayAddresses: ['/ip4/127.0.0.1/tcp/14080/ws/p2p/16Uiu2HAmTest'],
    });
    state.pushDeliveryEvent({ stage: 'grant-received', detail: 'ok' });
    state.setStoreSelection({ kind: 'plugin', key: 'licensing@0.1.0' });

    expect(state.snapshot().provider?.peerId).toBe('16Uiu2HAmTest');
    expect(state.snapshot().deliveryEvents).toHaveLength(1);
    expect(state.snapshot().storeSelection).toEqual({ kind: 'plugin', key: 'licensing@0.1.0' });
  });
});
