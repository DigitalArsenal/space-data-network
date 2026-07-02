import { describe, it, expect, vi, beforeEach } from 'vitest';

// Mock @helia/unixfs per suite convention (helia.test.ts mocks 'helia').
const addBytesMock = vi.fn();
const catMock = vi.fn();
vi.mock('@helia/unixfs', () => ({
  unixfs: () => ({ addBytes: addBytesMock, cat: catMock }),
}));

import { createModuleHostCapabilityAdapters } from './module-host-adapters';
import type { ModuleHostRecordStore } from './module-host-adapters';

function fakeStore(): ModuleHostRecordStore & { records: unknown[] } {
  const records: unknown[] = [];
  return {
    records,
    async store(schema, data, peerId, signature) {
      records.push({ schema, data, peerId, signature });
      return 'cid-abc';
    },
    async query(schema, filter) {
      return [{ schema, limit: filter?.limit }];
    },
    async delete(cid) {
      records.push({ deleted: cid });
    },
  };
}

function fakePubsub() {
  const published: Array<{ topic: string; data: Uint8Array }> = [];
  const topics = new Set<string>();
  const pubsub = {
    publish: vi.fn(async (topic: string, data: Uint8Array) => {
      published.push({ topic, data });
    }),
    subscribe: vi.fn((topic: string) => topics.add(topic)),
    unsubscribe: vi.fn((topic: string) => topics.delete(topic)),
    getTopics: vi.fn(() => [...topics]),
    addEventListener: vi.fn(),
  };
  const libp2p = { services: { pubsub } } as never;
  return { libp2p, pubsub, published, topics };
}

describe('createModuleHostCapabilityAdapters', () => {
  beforeEach(() => {
    addBytesMock.mockReset();
    catMock.mockReset();
  });

  it('includes only adapters whose backing service is provided', () => {
    expect(createModuleHostCapabilityAdapters({})).toEqual({});
    const withStore = createModuleHostCapabilityAdapters({ storage: fakeStore() });
    expect(Object.keys(withStore)).toEqual(['storage']);
  });

  it('storage adapter mirrors the Go cap contract (write/query/delete)', async () => {
    const store = fakeStore();
    const adapters = createModuleHostCapabilityAdapters({
      storage: store,
      peerId: 'peer-1',
    });
    // storage.write {schema, data:base64} -> {cid}
    const writeResult = await adapters.storage!.write({
      schema: 'OMM',
      data: btoa(String.fromCharCode(1, 2, 3)),
    });
    expect(writeResult).toEqual({ cid: 'cid-abc' });
    expect(store.records[0]).toMatchObject({ schema: 'OMM', peerId: 'peer-1' });
    expect([...(store.records[0] as { data: Uint8Array }).data]).toEqual([1, 2, 3]);

    const queryResult = await adapters.storage!.query({ schema: 'OMM', limit: 5 });
    expect(queryResult).toEqual([{ schema: 'OMM', limit: 5 }]);

    await expect(adapters.storage!.write({ data: 'AA==' })).rejects.toThrow(/schema/);
    expect(await adapters.storage!.delete({ cid: 'cid-abc' })).toBe(true);
  });

  it('pubsub adapter publishes utf8 payloads and lists topics', async () => {
    const { libp2p, published, pubsub } = fakePubsub();
    const adapters = createModuleHostCapabilityAdapters({ libp2p });
    expect(await adapters.pubsub!.publish({ topic: 'sdn/t', data: 'hello' })).toBe(true);
    expect(published[0].topic).toBe('sdn/t');
    expect(new TextDecoder().decode(published[0].data)).toBe('hello');

    await adapters.pubsub!.subscribe({ topic: 'sdn/t' });
    expect(pubsub.subscribe).toHaveBeenCalledWith('sdn/t');
    expect(await adapters.pubsub!.list_topics({})).toEqual({ topics: ['sdn/t'] });
    await adapters.pubsub!.unsubscribe({ topic: 'sdn/t' });
    expect(pubsub.unsubscribe).toHaveBeenCalledWith('sdn/t');
  });

  it('ipfs adapter maps add/cat onto unixfs', async () => {
    addBytesMock.mockResolvedValue({ toString: () => 'bafy-test' });
    catMock.mockImplementation(async function* () {
      yield Uint8Array.from([10, 20]);
      yield Uint8Array.from([30]);
    });
    const adapters = createModuleHostCapabilityAdapters({
      helia: {} as never,
    });
    // ipfs.add {content} -> {Hash, Size}
    const added = await adapters.ipfs!.add({ content: 'abc' });
    expect(added).toEqual({ Hash: 'bafy-test', Size: 3 });
    expect([...(addBytesMock.mock.calls[0][0] as Uint8Array)]).toEqual([97, 98, 99]);

    const bytes = (await adapters.ipfs!.cat({ cid: 'bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi' })) as Uint8Array;
    expect([...bytes]).toEqual([10, 20, 30]);
  });

  it('walletSign adapter serves configured key slots', async () => {
    const key = Uint8Array.from({ length: 32 }, (_, i) => i);
    const adapters = createModuleHostCapabilityAdapters({
      keySlots: {
        'node-signing': key,
        lazy: () => Promise.resolve(Uint8Array.from([9])),
      },
    });
    expect([...((await adapters.walletSign!.get({ slotId: 'node-signing' })) as Uint8Array)]).toEqual([
      ...key,
    ]);
    expect([...((await adapters.walletSign!.get({ slotId: 'lazy' })) as Uint8Array)]).toEqual([9]);
    await expect(adapters.walletSign!.get({ slotId: 'nope' })).rejects.toThrow(
      /unknown key slot/,
    );
  });
});
