import { describe, expect, it } from 'vitest';

import {
  buildObservedPeersMarkup,
  buildProviderDescriptorText,
  buildTimelineMarkup,
} from '../../ui/src/views/network-view';

describe('network view helpers', () => {
  it('renders empty timeline state when no events are present', () => {
    expect(buildTimelineMarkup([])).toContain('Challenge, grant, fetch, decrypt, load, and invoke events appear in order.');
  });

  it('renders provider identity fields into descriptor text', () => {
    expect(buildProviderDescriptorText({
      publicKey: '02abc',
      peerId: '16Uiu2HAmTest',
      ipns: '/ipns/16Uiu2HAmTest',
      relayAddresses: ['/ip4/127.0.0.1/tcp/8080/ws/p2p/16Uiu2HAmTest'],
      identity: {
        xpub: 'xpub-test',
        addresses: [{ chain: 'bitcoin', address: 'bc1test' }],
      },
    })).toContain('"xpub": "xpub-test"');
  });

  it('renders recent observed peer sightings', () => {
    expect(buildObservedPeersMarkup([
      {
        peerId: '16Uiu2HAmTest',
        observedAt: 1,
        sources: ['provider', 'dht'],
        detail: 'seeded peer',
      },
    ])).toContain('seeded peer');
  });
});
