import { createSdnWalletClient } from 'hd-wallet-ui/client/sdn';

import { walletConsumer } from '../wallet-consumer.generated';

type SdnWalletClient = ReturnType<typeof createSdnWalletClient>;

const SDN_WALLET_CLIENT_ID = 'sdn-node-console-v1';
const SDN_WALLET_REGISTRY_SHA256 = 'e1ce6fe903c9700484a8a87d96581c8cad97063dabf63030b4518a31a3bdaa93';

const EXPECTED_OPERATIONS = [
  'sdn.auth.jcs-envelope.v2',
  'sdn.auth.raw-challenge.v1',
  'sdn.wallet.account.v1',
  'sdn.wallet.connect.v1',
] as const;
const EXPECTED_AUDIENCES = ['sdn-login:sdn.spaceaware.io'] as const;
const clientsByDocument = new WeakMap<Document, SdnWalletClient>();

function assertGeneratedConsumer(): void {
  if (
    walletConsumer.clientId !== SDN_WALLET_CLIENT_ID
    || walletConsumer.callbackUri !== 'https://sdn.spaceaware.io/wallet/callback'
    || walletConsumer.walletVersion !== '2.0.28'
    || walletConsumer.registryReleaseSha256 !== SDN_WALLET_REGISTRY_SHA256
    || JSON.stringify(walletConsumer.allowedOperations) !== JSON.stringify(EXPECTED_OPERATIONS)
    || JSON.stringify(walletConsumer.audiences) !== JSON.stringify(EXPECTED_AUDIENCES)
  ) {
    throw new Error('invalid generated SDN wallet consumer');
  }
}

/**
 * Returns the one public wallet client owned by this browser document.
 * Credential material and signing stay isolated on the wallet origin; this
 * seam only exposes the reviewed public relay client.
 */
export function getSdnWalletClient(doc: Document = document): SdnWalletClient {
  const existing = clientsByDocument.get(doc);
  if (existing) return existing;

  assertGeneratedConsumer();
  const client = createSdnWalletClient();
  clientsByDocument.set(doc, client);

  let destroyed = false;
  const destroy = (): void => {
    if (destroyed) return;
    destroyed = true;
    clientsByDocument.delete(doc);
    void client.destroy();
  };

  doc.defaultView?.addEventListener('pagehide', destroy, { once: true });
  doc.addEventListener('freeze', destroy, { once: true });
  return client;
}
