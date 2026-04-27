import {
  MODULE_DELIVERY_PROTOCOL_ID,
} from '../module-delivery';
import { SDNNode } from '../node';
import {
  decryptEncryptedModuleBundle,
  unwrapGrantContentKey,
  type WrappedContentKeyLike,
} from '../ui/runtime/live-delivery';
import type { LoadedWallet } from './wallet';

export interface QueryModuleDeliveryOptions {
  nodeUrl: string;
  moduleId: string;
  moduleVersion?: string;
  requesterDomain: string;
  requestedTimeoutMs?: number;
  wallet: LoadedWallet;
  providerDescriptor?: Record<string, unknown>;
  fetchImpl?: typeof fetch;
  nodeFactory?: () => Promise<QueryDeliveryNode>;
  unwrapContentKey?: (wrappedContentKey: unknown, recipientPrivateKey: Uint8Array) => Promise<Uint8Array>;
  decryptBundle?: (encryptedBundleBytes: Uint8Array, contentKey: Uint8Array) => Promise<Uint8Array>;
}

export interface QueryDeliveryNode {
  requestEncryptedModuleBundle(options: Record<string, unknown>): Promise<QueryDeliveryResult>;
  stop?: () => Promise<void>;
}

export interface QueryDeliveryResult {
  provider: {
    peerId?: string;
  };
  grant: {
    bundleDescriptor: {
      cid?: string;
      moduleId?: string;
      moduleVersion?: string;
    };
    wrappedContentKey: unknown;
  };
  encryptedBundleBytes: Uint8Array;
}

export interface QueryModuleDeliverySummary {
  protocol_id: string;
  provider_peer_id: string;
  module_id: string;
  module_version?: string;
  cid: string;
  encrypted_size_bytes: number;
  decrypted_size_bytes: number;
}

export async function queryModuleDelivery(
  options: QueryModuleDeliveryOptions,
): Promise<QueryModuleDeliverySummary> {
  const nodeOrigin = normalizeNodeOrigin(options.nodeUrl);
  const serverDescriptor = options.providerDescriptor
    ?? await fetchProviderDescriptor(nodeOrigin, options.fetchImpl ?? fetch);
  const node = options.nodeFactory
    ? await options.nodeFactory()
    : await createDefaultNode(options.wallet, nodeOrigin);
  try {
    const result = await node.requestEncryptedModuleBundle({
      serverDescriptor,
      requesterIdentity: options.wallet.identity,
      moduleId: options.moduleId,
      moduleVersion: options.moduleVersion,
      requesterDomain: options.requesterDomain,
      requestedTimeoutMs: options.requestedTimeoutMs ?? 300_000,
    });
    const unwrap = options.unwrapContentKey ?? defaultUnwrapContentKey;
    const decrypt = options.decryptBundle ?? decryptEncryptedModuleBundle;
    const contentKey = await unwrap(
      result.grant.wrappedContentKey,
      options.wallet.identity.encryptionKey.privateKey,
    );
    const decryptedBundle = await decrypt(result.encryptedBundleBytes, contentKey);
    return {
      protocol_id: MODULE_DELIVERY_PROTOCOL_ID,
      provider_peer_id: result.provider.peerId ?? '',
      module_id: result.grant.bundleDescriptor.moduleId ?? options.moduleId,
      module_version: result.grant.bundleDescriptor.moduleVersion,
      cid: result.grant.bundleDescriptor.cid ?? '',
      encrypted_size_bytes: result.encryptedBundleBytes.length,
      decrypted_size_bytes: decryptedBundle.length,
    };
  } finally {
    await node.stop?.();
  }
}

async function createDefaultNode(wallet: LoadedWallet, nodeOrigin: string): Promise<QueryDeliveryNode> {
  void nodeOrigin;
  return SDNNode.create({
    identity: wallet.identity,
    enableStorage: false,
  }) as Promise<QueryDeliveryNode>;
}

function defaultUnwrapContentKey(
  wrappedContentKey: unknown,
  recipientPrivateKey: Uint8Array,
): Promise<Uint8Array> {
  return unwrapGrantContentKey(
    wrappedContentKey as WrappedContentKeyLike,
    recipientPrivateKey,
  );
}

function normalizeNodeOrigin(nodeUrl: string): string {
  return new URL(nodeUrl).origin;
}

async function fetchProviderDescriptor(
  nodeOrigin: string,
  fetchImpl: typeof fetch,
): Promise<Record<string, unknown>> {
  const response = await fetchImpl(`${nodeOrigin}/api/module-delivery/provider`, {
    method: 'GET',
    headers: { Accept: 'application/json' },
  });
  if (!response.ok) {
    throw new Error(`provider descriptor fetch failed: ${response.status} ${await response.text()}`);
  }
  return response.json() as Promise<Record<string, unknown>>;
}
