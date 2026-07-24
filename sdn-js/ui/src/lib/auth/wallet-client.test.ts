import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { beforeEach, describe, expect, it, vi } from 'vitest';

const clients: Array<{ destroy: ReturnType<typeof vi.fn> }> = [];
const createSdnWalletClient = vi.hoisted(() => vi.fn(() => {
  const client = {
    connect: vi.fn(),
    destroy: vi.fn(async () => undefined),
    disconnect: vi.fn(),
    getSnapshot: vi.fn(() => ({ identity: null, status: 'dormant' as const })),
    openAccount: vi.fn(),
    requestSdnLoginV1: vi.fn(),
    requestSdnLoginV2: vi.fn(),
    subscribe: vi.fn(),
  };
  clients.push(client);
  return client;
}));

vi.mock('hd-wallet-ui/client/sdn', () => ({ createSdnWalletClient }));

interface DocumentFixture {
  readonly document: Document;
  dispatchDocument(type: string): void;
  dispatchWindow(type: string): void;
}

function documentFixture(): DocumentFixture {
  const documentListeners = new Map<string, EventListener>();
  const windowListeners = new Map<string, EventListener>();
  const defaultView = {
    addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
      windowListeners.set(type, listener as EventListener);
    },
  };
  const document = {
    addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
      documentListeners.set(type, listener as EventListener);
    },
    defaultView,
  } as unknown as Document;

  return {
    document,
    dispatchDocument(type) {
      documentListeners.get(type)?.(new Event(type));
    },
    dispatchWindow(type) {
      windowListeners.get(type)?.(new Event(type));
    },
  };
}

beforeEach(() => {
  clients.length = 0;
  createSdnWalletClient.mockClear();
});
describe('getSdnWalletClient', () => {
  it('returns one client per document and destroys it once on teardown', async () => {
    const modulePath = fileURLToPath(new URL('./wallet-client.ts', import.meta.url));
    expect(existsSync(modulePath), 'missing lib/auth/wallet-client.ts').toBe(true);
    const { getSdnWalletClient } = await import('./wallet-client');
    const fixture = documentFixture();

    const first = getSdnWalletClient(fixture.document);
    const second = getSdnWalletClient(fixture.document);

    expect(first).toBe(second);
    expect(createSdnWalletClient).toHaveBeenCalledTimes(1);

    fixture.dispatchWindow('pagehide');
    fixture.dispatchDocument('freeze');
    expect(clients[0].destroy).toHaveBeenCalledTimes(1);
  });

  it('creates independent clients for independent app documents', async () => {
    const modulePath = fileURLToPath(new URL('./wallet-client.ts', import.meta.url));
    expect(existsSync(modulePath), 'missing lib/auth/wallet-client.ts').toBe(true);
    const { getSdnWalletClient } = await import('./wallet-client');
    const firstDocument = documentFixture();
    const secondDocument = documentFixture();

    expect(getSdnWalletClient(firstDocument.document)).not.toBe(
      getSdnWalletClient(secondDocument.document),
    );
    expect(createSdnWalletClient).toHaveBeenCalledTimes(2);
  });
});
