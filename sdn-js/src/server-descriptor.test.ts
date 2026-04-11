import { Builder } from 'flatbuffers';
import { describe, expect, it, vi } from 'vitest';

vi.mock('./crypto/hd-wallet', async () => {
  const actual = await vi.importActual<typeof import('./crypto/hd-wallet')>('./crypto/hd-wallet');
  return {
    ...actual,
    derivePeerIdFromPublicKey: vi.fn(async () => 'provider-peer-id'),
  };
});

import { normalizeServerDescriptor } from './server-descriptor';

describe('server-descriptor.normalizeServerDescriptor', () => {
  it('normalizes a JSON descriptor with a required compressed public key', async () => {
    const descriptor = await normalizeServerDescriptor({
      publicKey: '02'.padEnd(66, '1'),
      relayAddresses: ['/dns4/relay.example/tcp/443/wss/p2p/relay-peer'],
      cid: 'bafyproviderdescriptor',
    });

    expect(descriptor).toMatchObject({
      peerId: 'provider-peer-id',
      publicKeyHex: '02'.padEnd(66, '1'),
      relayAddresses: ['/dns4/relay.example/tcp/443/wss/p2p/relay-peer'],
      cid: 'bafyproviderdescriptor',
      source: 'descriptor',
    });
    expect(descriptor.publicKey).toBeInstanceOf(Uint8Array);
    expect(descriptor.publicKey).toHaveLength(33);
  });

  it('extracts the provider key and relay hints from EPM bytes', async () => {
    const epmBytes = buildEPM({
      publicKeyHex: '03'.padEnd(66, 'a'),
      multiformatAddresses: ['/dns4/provider.example/tcp/443/wss/p2p/provider-peer-id'],
    });

    const descriptor = await normalizeServerDescriptor(epmBytes);

    expect(descriptor).toMatchObject({
      peerId: 'provider-peer-id',
      publicKeyHex: '03'.padEnd(66, 'a'),
      relayAddresses: ['/dns4/provider.example/tcp/443/wss/p2p/provider-peer-id'],
      source: 'epm',
    });
    expect(descriptor.rawEpmBytes).toEqual(epmBytes);
  });

  it('rejects descriptors that omit the provider public key', async () => {
    await expect(
      normalizeServerDescriptor({
        cid: 'bafyproviderdescriptor',
      } as never),
    ).rejects.toThrow(/public key/i);
  });

  it('rejects descriptors whose resolved EPM disagrees with the declared public key', async () => {
    await expect(
      normalizeServerDescriptor(
        {
          publicKey: '02'.padEnd(66, '1'),
          cid: 'bafyproviderdescriptor',
        },
        {
          resolveCID: async () => buildEPM({ publicKeyHex: '03'.padEnd(66, '2') }),
        },
      ),
    ).rejects.toThrow(/public key mismatch/i);
  });

  it('rejects descriptors whose declared peer id disagrees with the public key trust root', async () => {
    await expect(
      normalizeServerDescriptor({
        publicKey: '02'.padEnd(66, '1'),
        peerId: 'wrong-peer-id',
      }),
    ).rejects.toThrow(/peer id/i);
  });
});

function buildEPM(input: {
  publicKeyHex: string;
  multiformatAddresses?: string[];
}): Uint8Array {
  const builder = new Builder(256);

  const publicKeyOffset = builder.createString(input.publicKeyHex);
  const addressTypeOffset = builder.createString('secp256k1');

  builder.startObject(7);
  builder.addFieldOffset(0, publicKeyOffset, 0);
  builder.addFieldOffset(5, addressTypeOffset, 0);
  builder.addFieldInt8(6, 0, 0);
  const cryptoKeyOffset = builder.endObject();

  const keysOffset = createOffsetVector(builder, [cryptoKeyOffset]);
  const addressOffsets = (input.multiformatAddresses ?? []).map((value) => builder.createString(value));
  const multiformatOffset = addressOffsets.length > 0 ? createOffsetVector(builder, addressOffsets) : 0;

  builder.startObject(15);
  builder.addFieldOffset(13, keysOffset, 0);
  if (multiformatOffset !== 0) {
    builder.addFieldOffset(14, multiformatOffset, 0);
  }
  const epmOffset = builder.endObject();
  builder.finish(epmOffset, '$EPM');
  return builder.asUint8Array();
}

function createOffsetVector(builder: Builder, offsets: number[]): number {
  builder.startVector(4, offsets.length, 4);
  for (let index = offsets.length - 1; index >= 0; index -= 1) {
    builder.addOffset(offsets[index]);
  }
  return builder.endVector();
}
