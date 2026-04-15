import { beforeEach, describe, expect, it, vi } from 'vitest';

const createHeliaMock = vi.fn(async (init: unknown) => ({ init }));

vi.mock('helia', () => ({
  createHelia: createHeliaMock,
}));

describe('createHeliaFromLibp2p', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
  });

  it('adapts two-argument stream handlers to incoming stream data objects', async () => {
    const incoming = {
      stream: { id: 'stream-1', protocol: '/ipfs/bitswap/1.2.0' },
      connection: { remotePeer: 'peer-1' },
    };
    const originalHandle = vi.fn(async (_protocols, handler) => {
      handler(incoming);
    });

    const { createHeliaFromLibp2p } = await import('./helia');
    await createHeliaFromLibp2p({ handle: originalHandle } as any);

    const patchedLibp2p = createHeliaMock.mock.calls[0][0].libp2p;
    const spy = vi.fn();
    const handler = function (stream: unknown, connection: unknown) {
      spy(stream, connection);
    };
    await patchedLibp2p.handle('/ipfs/bitswap/1.2.0', handler, {});

    expect(spy).toHaveBeenCalledWith(incoming.stream, incoming.connection);
  });

  it('leaves single-argument stream handlers untouched', async () => {
    const incoming = {
      stream: { id: 'stream-1', protocol: '/ipfs/bitswap/1.2.0' },
      connection: { remotePeer: 'peer-1' },
    };
    const originalHandle = vi.fn(async (_protocols, handler) => {
      handler(incoming);
    });

    const { createHeliaFromLibp2p } = await import('./helia');
    await createHeliaFromLibp2p({ handle: originalHandle } as any);

    const patchedLibp2p = createHeliaMock.mock.calls[0][0].libp2p;
    const handler = vi.fn();
    await patchedLibp2p.handle('/test/1.0.0', handler, {});

    expect(handler).toHaveBeenCalledWith(incoming);
    expect(handler.mock.calls[0]).toHaveLength(1);
  });
});
