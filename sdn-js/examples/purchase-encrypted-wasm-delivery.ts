import {
  SDNNode,
  identityFromMnemonic,
  initHDWallet,
} from '@spacedatanetwork/sdn-js';
import {
  decryptEncryptedModuleBundle,
  invokeLoadedModule,
  loadDecryptedModule,
  loadMarketplaceListingsFromServer,
  unwrapGrantContentKey,
} from '@spacedatanetwork/sdn-js/ui';
import {
  PaymentMethod,
  createStorefrontClient,
} from '@spacedatanetwork/sdn-js/storefront';

interface PurchaseEncryptedWasmDeliveryOptions {
  marketplaceBaseUrl: string;
  storefrontApiBaseUrl: string;
  mnemonic: string;
  providerPublicKey: string;
  providerRelayAddresses?: string[];
  requesterDomain: string;
  moduleId: string;
  moduleVersion?: string;
  tierName?: string;
  paymentMethod?: PaymentMethod;
  methodId?: string;
  inputs?: Array<{ payload: Uint8Array }>;
}

export async function purchaseEncryptedWasmDeliveryAndRun(
  options: PurchaseEncryptedWasmDeliveryOptions,
) {
  await initHDWallet();
  const identity = await identityFromMnemonic(options.mnemonic);
  const node = await SDNNode.create({
    identity,
    enableRelayProbing: true,
  });

  try {
    const listings = await loadMarketplaceListingsFromServer(options.marketplaceBaseUrl);
    const listing = listings.find((candidate) =>
      candidate.pluginId === options.moduleId &&
      (!options.moduleVersion || candidate.version === options.moduleVersion)
    );
    if (!listing) {
      throw new Error(`PLG listing not found for ${options.moduleId}`);
    }

    const storefront = createStorefrontClient({
      apiBaseUrl: options.storefrontApiBaseUrl,
      peerId: identity.peerId,
      encryptionPubkey: identity.encryptionKey.publicKey,
      keyAlgorithm: 'x25519',
    });

    const purchase = await storefront.createPurchase({
      listingId: listing.pluginId,
      tierName: options.tierName ?? 'default',
      paymentMethod: options.paymentMethod ?? PaymentMethod.SDNCredits,
      encryptionPubkey: identity.encryptionKey.publicKey,
      keyAlgorithm: 'x25519',
      preferredDeliveryMethod: 'IPFSPin',
    });

    if ((options.paymentMethod ?? PaymentMethod.SDNCredits) === PaymentMethod.SDNCredits) {
      await storefront.payWithCredits(purchase.requestId);
    }

    const delivery = await node.requestEncryptedModuleBundle({
      serverDescriptor: {
        publicKey: options.providerPublicKey,
        relayAddresses: options.providerRelayAddresses,
      },
      moduleId: listing.pluginId,
      moduleVersion: listing.version,
      requesterDomain: options.requesterDomain,
      requestedTimeoutMs: 300_000,
    });

    const contentKey = await unwrapGrantContentKey(
      delivery.grant.wrappedContentKey,
      identity.encryptionKey.privateKey,
    );
    const wasmBytes = await decryptEncryptedModuleBundle(
      delivery.encryptedBundleBytes,
      contentKey,
    );
    const harness = await loadDecryptedModule(wasmBytes);
    const result = await invokeLoadedModule(harness, {
      methodId: options.methodId ?? 'invoke',
      inputs: options.inputs ?? [],
    });

    harness.destroy?.();
    return {
      listing,
      purchase,
      delivery,
      result,
    };
  } finally {
    await node.stop();
  }
}
