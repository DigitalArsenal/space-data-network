import { beforeEach, describe, expect, it, vi } from 'vitest';

const { createBrowserModuleHarness } = vi.hoisted(() => ({
  createBrowserModuleHarness: vi.fn(async () => ({
    invoke: vi.fn(async () => ({
      statusCode: 0,
      outputs: [],
    })),
  })),
}));

const { aesGcmDecryptWithIv } = vi.hoisted(() => ({
  aesGcmDecryptWithIv: vi.fn(async () => new TextEncoder().encode('live bundle')),
}));

vi.mock('space-data-module-sdk/testing/browser', () => ({
  createBrowserModuleHarness,
}));

vi.mock('../../crypto/hd-wallet', () => {
  return {
    aesGcmDecryptWithIv,
  };
});

import {
  decryptGrantProtectedModuleBundle,
  decryptEncryptedModuleBundle,
  invokeLoadedModule,
  loadDecryptedModule,
  unwrapGrantContentKey,
} from './live-delivery';

describe('live-delivery', () => {
  beforeEach(() => {
    createBrowserModuleHarness.mockClear();
    aesGcmDecryptWithIv.mockClear();
  });

  it('unwraps direct content keys and emits decrypt/load/invoke lifecycle events', async () => {
    const contentKey = new Uint8Array(32).fill(4);
    const deliveryEvents: string[] = [];

    const unwrapped = await unwrapGrantContentKey(
      {
        keyBytes: contentKey,
        providerEphemeralPublicKey: new Uint8Array(),
      },
      new Uint8Array(32).fill(7),
      {
        onEvent(event) {
          deliveryEvents.push(event.stage);
        },
      },
    );

    expect(unwrapped).toEqual(contentKey);

    const encryptedBundleBytes = new Uint8Array(12 + 4 + 16);
    encryptedBundleBytes.set(new Uint8Array(12).fill(1), 0);
    encryptedBundleBytes.set(new Uint8Array(20).fill(2), 12);
    const decryptedBundle = await decryptEncryptedModuleBundle(
      encryptedBundleBytes,
      contentKey,
      new TextEncoder().encode('listing=protected-od;grant=g1;epoch=e1'),
      {
        onEvent(event) {
          deliveryEvents.push(event.stage);
        },
      },
    );

    expect(new TextDecoder().decode(decryptedBundle)).toBe('live bundle');
    expect(aesGcmDecryptWithIv).toHaveBeenCalledWith(
      contentKey,
      new Uint8Array(20).fill(2),
      new Uint8Array(12).fill(1),
      new TextEncoder().encode('listing=protected-od;grant=g1;epoch=e1'),
    );

    const harness = await loadDecryptedModule(new Uint8Array([0, 97, 115, 109]), {
      onEvent(event) {
        deliveryEvents.push(event.stage);
      },
    });
    const response = await invokeLoadedModule(
      harness,
      { methodId: 'echo', inputs: [] },
      {
        onEvent(event) {
          deliveryEvents.push(event.stage);
        },
      },
    );

    expect(response.statusCode).toBe(0);
    expect(createBrowserModuleHarness).toHaveBeenCalledTimes(1);
    expect(deliveryEvents).toEqual([
      'unwrap-start',
      'unwrap-complete',
      'decrypt-start',
      'decrypt-complete',
      'sdk-load-start',
      'sdk-load-complete',
      'invoke-start',
      'invoke-result',
    ]);
  });

  it('uses direct external arena invocation for direct-surface SDK harnesses', async () => {
    const invoke = vi.fn(async () => ({ statusCode: 1, outputs: [] }));
    const invokeDirect = vi.fn(async () => ({ statusCode: 0, outputs: [] }));
    const memory = new WebAssembly.Memory({ initial: 1 });
    const response = await invokeLoadedModule(
      {
        runtime: { surface: 'direct' },
        memory,
        invoke,
        invokeDirect,
      },
      { methodId: 'echo', inputs: [] },
    );

    expect(response.statusCode).toBe(0);
    expect(invoke).not.toHaveBeenCalled();
    expect(invokeDirect).toHaveBeenCalledTimes(1);
    expect(invokeDirect.mock.calls[0]?.[0]).toMatchObject({
      methodId: 'echo',
      inputs: [],
      externalArena: expect.any(Uint8Array),
    });
    expect((invokeDirect.mock.calls[0]?.[0] as { externalArena: Uint8Array }).externalArena.buffer).toBe(memory.buffer);
  });

  it('fails closed for encrypted grant key unwraps that must run in client-decrypt WASM', async () => {
    await expect(
      unwrapGrantContentKey(
        {
          keyBytes: new Uint8Array(),
          encryptedPayload: new Uint8Array([1, 2, 3]),
          providerEphemeralPublicKey: new Uint8Array(32).fill(3),
          keyMaterialRootType: 'REC',
          header: {
            context: 'space-data-network/module-delivery/grant/v1',
            rootType: 'REC',
          },
        },
        new Uint8Array(32).fill(7),
      ),
    ).rejects.toThrow(/WASM client-decrypt module/);
  });

  it('routes encrypted REC/KMF grants through client-decrypt for browser artifact decryption', async () => {
    const decryptArtifact = vi.fn(async () => new TextEncoder().encode('protected wasm'));
    const grantResponseBytes = new Uint8Array([1, 2, 3, 4]);
    const encryptedBundleBytes = new Uint8Array([5, 6, 7, 8]);
    const recipientPrivateKey = new Uint8Array(32).fill(7);
    const events: string[] = [];

    const decrypted = await decryptGrantProtectedModuleBundle(
      {
        grantResponseBytes,
        encryptedBundleBytes,
      },
      recipientPrivateKey,
      { decryptArtifact },
      {
        onEvent(event) {
          events.push(event.stage);
        },
      },
    );

    expect(new TextDecoder().decode(decrypted)).toBe('protected wasm');
    expect(decryptArtifact).toHaveBeenCalledWith(
      {
        grantResponseBytes,
        encryptedBundleBytes,
      },
      recipientPrivateKey,
    );
    expect(events).toEqual(['unwrap-start', 'decrypt-start', 'decrypt-complete']);
  });

  it('fails closed when client-decrypt rejects a tampered grant envelope', async () => {
    const decryptArtifact = vi.fn(async () => {
      throw new Error('invalid grant envelope authentication tag');
    });
    const events: string[] = [];

    await expect(
      decryptGrantProtectedModuleBundle(
        {
          grantResponseBytes: new Uint8Array([1, 2, 3, 4]),
          encryptedBundleBytes: new Uint8Array([5, 6, 7, 8]),
        },
        new Uint8Array(32).fill(7),
        { decryptArtifact },
        {
          onEvent(event) {
            events.push(event.stage);
          },
        },
      ),
    ).rejects.toThrow(/invalid grant envelope authentication tag/);

    expect(decryptArtifact).toHaveBeenCalledTimes(1);
    expect(events).toEqual(['unwrap-start', 'decrypt-start']);
  });

  it('rejects undersized encrypted bundle payloads before decrypting', async () => {
    await expect(
      decryptEncryptedModuleBundle(
        new Uint8Array(28),
        new Uint8Array(32).fill(4),
        undefined,
      ),
    ).rejects.toThrow(/iv and authentication tag/);
    expect(aesGcmDecryptWithIv).not.toHaveBeenCalled();
  });

  it('passes canonical grant AAD into AES-GCM so tampered delivery metadata fails closed', async () => {
    const encryptedBundleBytes = new Uint8Array(12 + 4 + 16);
    const aad = new TextEncoder().encode('listing=protected-od;grant=g1;epoch=e1');
    aesGcmDecryptWithIv.mockRejectedValueOnce(new Error('authentication failed'));

    await expect(
      decryptEncryptedModuleBundle(
        encryptedBundleBytes,
        new Uint8Array(32).fill(4),
        aad,
      ),
    ).rejects.toThrow(/authentication failed/);
    expect(aesGcmDecryptWithIv).toHaveBeenCalledWith(
      new Uint8Array(32).fill(4),
      new Uint8Array(20),
      new Uint8Array(12),
      aad,
    );
  });
});
