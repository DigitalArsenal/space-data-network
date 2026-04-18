import { describe, expect, it } from 'vitest';

import { ObservedPeerIndex } from './observed-peers';

describe('ObservedPeerIndex', () => {
  it('counts a peer once across multiple evidence sources', () => {
    const index = new ObservedPeerIndex();

    index.record({ peerId: '12D3KooWAlpha', source: 'dht' });
    index.record({ peerId: '12D3KooWAlpha', source: 'protocol' });

    expect(index.count()).toBe(1);
    expect(index.list()).toEqual([
      expect.objectContaining({
        peerId: '12D3KooWAlpha',
        sources: ['dht', 'protocol'],
      }),
    ]);
  });

  it('retains the latest evidence metadata for a peer without duplicating it', () => {
    const index = new ObservedPeerIndex();

    index.record({
      peerId: '12D3KooWBravo',
      source: 'seed',
      observedAt: 50,
      detail: 'bootstrap relay',
    });
    index.record({
      peerId: '12D3KooWBravo',
      source: 'identity',
      observedAt: 75,
      detail: 'verified EPM',
    });

    expect(index.get('12D3KooWBravo')).toEqual({
      peerId: '12D3KooWBravo',
      observedAt: 75,
      detail: 'verified EPM',
      sources: ['identity', 'seed'],
    });
  });
});
