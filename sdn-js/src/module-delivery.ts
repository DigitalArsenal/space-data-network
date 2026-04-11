import {
  MODULE_DELIVERY_PROTOCOL_ID,
  decodeModuleDeliveryMessage,
  encodeModuleDeliveryMessage,
} from '@spacedatanetwork/plugin-sdk';

import type { DerivedIdentity, EncryptionKeyPair, KeyPair } from './crypto/types';
import { sign } from './crypto/hd-wallet';
import { discoverProvider } from './discovery';
import {
  normalizeServerDescriptor,
  type NormalizedServerDescriptor,
  type ServerDescriptorInput,
  type ServerDescriptorResolver,
} from './server-descriptor';

export interface ModuleDeliveryTransport {
  dialProtocol(
    targetPeerId: string,
    protocolId: string,
    payload: Uint8Array,
    candidateAddrs?: string[],
  ): Promise<Uint8Array>;
  fetchCIDBytes(cid: string): Promise<Uint8Array>;
  discoverProviders?(discoveryCID: string): Promise<DiscoveredProvider[]>;
}

export { MODULE_DELIVERY_PROTOCOL_ID };

export interface DiscoveredProvider {
  peerId: string;
  multiaddrs: string[];
}

export interface RequesterIdentity {
  peerId: string;
  xpub?: string;
  signingKey: Pick<KeyPair, 'privateKey' | 'publicKey'>;
  encryptionKey: Pick<EncryptionKeyPair, 'privateKey' | 'publicKey'>;
}

export interface ModuleGrantRequestOptions {
  serverDescriptor: ServerDescriptorInput;
  descriptorResolver?: ServerDescriptorResolver;
  requesterIdentity: Pick<DerivedIdentity, 'peerId' | 'xpub' | 'signingKey' | 'encryptionKey'> | RequesterIdentity;
  moduleId: string;
  moduleVersion?: string;
  moduleVariant?: string;
  reqId?: string;
  requestedAtMs?: number;
}

export interface ModuleGrantResult {
  provider: NormalizedServerDescriptor;
  grant: GrantResponsePayload;
}

export interface EncryptedModuleBundleResult extends ModuleGrantResult {
  encryptedBundleBytes: Uint8Array;
}

interface GrantChallengePayload {
  reqId: string;
  challenge: Uint8Array;
  expiresAtMs: number;
  providerPeerId: string;
  providerPublicKey: Uint8Array;
}

interface BundleDescriptorPayload {
  cid: string;
  contentHash: Uint8Array;
  sizeBytes: number;
  moduleId: string;
  moduleVersion?: string;
}

interface WrappedContentKeyPayload {
  wrappingAlgorithm: string;
  recipientPublicKey: Uint8Array;
  ephemeralPublicKey: Uint8Array;
  nonce: Uint8Array;
  ciphertext: Uint8Array;
  tag: Uint8Array;
}

interface GrantResponsePayload {
  reqId: string;
  bundleDescriptor: BundleDescriptorPayload;
  wrappedContentKey: WrappedContentKeyPayload;
}

export async function requestModuleGrant(
  transport: ModuleDeliveryTransport,
  options: ModuleGrantRequestOptions,
): Promise<ModuleGrantResult> {
  const provider = await normalizeServerDescriptor(
    options.serverDescriptor,
    options.descriptorResolver,
  );
  const requesterIdentity = normalizeRequesterIdentity(options.requesterIdentity);
  const discovery = await discoverProvider(provider.publicKey);
  const candidateAddrs = await resolveCandidateAddresses(transport, provider, discovery.peerId, discovery.discoveryCID);
  const reqId = normalizeRequiredString(options.reqId || createReqId(), 'reqId');
  const requestedAtMs = options.requestedAtMs ?? Date.now();

  const challengeResponse = await sendMessage(transport, provider.peerId, candidateAddrs, {
    type: 'grant_request',
    payload: {
      reqId,
      moduleId: normalizeRequiredString(options.moduleId, 'moduleId'),
      moduleVersion: trimOptional(options.moduleVersion),
      moduleVariant: trimOptional(options.moduleVariant),
      requesterPeerId: requesterIdentity.peerId,
      requesterXpub: trimOptional(requesterIdentity.xpub),
      requesterSigningPublicKey: requesterIdentity.signingKey.publicKey,
      requesterEncryptionPublicKey: requesterIdentity.encryptionKey.publicKey,
      requestedAtMs,
    },
  });

  if (challengeResponse.type === 'error_response') {
    throw new ModuleDeliveryProtocolError(challengeResponse.payload.code, challengeResponse.payload.message);
  }
  if (challengeResponse.type !== 'grant_challenge') {
    throw new ModuleDeliveryProtocolError(
      'unexpected_response',
      `expected grant_challenge, received ${challengeResponse.type}`,
    );
  }

  const challenge = challengeResponse.payload as GrantChallengePayload;
  if (challenge.reqId !== reqId) {
    throw new ModuleDeliveryProtocolError('request_mismatch', 'grant challenge request id mismatch');
  }
  if (challenge.providerPeerId && challenge.providerPeerId !== provider.peerId) {
    throw new ModuleDeliveryProtocolError('provider_mismatch', 'provider peer id mismatch');
  }
  if (challenge.providerPublicKey.length > 0 && !equalBytes(challenge.providerPublicKey, provider.publicKey)) {
    throw new ModuleDeliveryProtocolError('provider_mismatch', 'provider public key mismatch');
  }

  const signature = await sign(requesterIdentity.signingKey.privateKey, challenge.challenge);
  const grantResponse = await sendMessage(transport, provider.peerId, candidateAddrs, {
    type: 'grant_proof',
    payload: {
      reqId,
      moduleId: normalizeRequiredString(options.moduleId, 'moduleId'),
      moduleVersion: trimOptional(options.moduleVersion),
      requesterPeerId: requesterIdentity.peerId,
      requesterSigningPublicKey: requesterIdentity.signingKey.publicKey,
      requesterEncryptionPublicKey: requesterIdentity.encryptionKey.publicKey,
      challenge: challenge.challenge,
      signature,
      provedAtMs: Date.now(),
    },
  });

  if (grantResponse.type === 'error_response') {
    throw new ModuleDeliveryProtocolError(grantResponse.payload.code, grantResponse.payload.message);
  }
  if (grantResponse.type !== 'grant_response') {
    throw new ModuleDeliveryProtocolError(
      'unexpected_response',
      `expected grant_response, received ${grantResponse.type}`,
    );
  }

  const grant = grantResponse.payload as GrantResponsePayload;
  if (grant.reqId !== reqId) {
    throw new ModuleDeliveryProtocolError('request_mismatch', 'grant response request id mismatch');
  }

  return { provider, grant };
}

export async function fetchEncryptedModuleBundle(
  transport: Pick<ModuleDeliveryTransport, 'fetchCIDBytes'>,
  result: ModuleGrantResult,
): Promise<EncryptedModuleBundleResult> {
  const encryptedBundleBytes = await transport.fetchCIDBytes(result.grant.bundleDescriptor.cid);
  const digest = await sha256(encryptedBundleBytes);

  if (
    result.grant.bundleDescriptor.contentHash.length > 0 &&
    !equalBytes(digest, result.grant.bundleDescriptor.contentHash)
  ) {
    throw new ModuleDeliveryProtocolError('hash_mismatch', 'encrypted bundle hash mismatch');
  }

  if (
    result.grant.bundleDescriptor.sizeBytes > 0 &&
    encryptedBundleBytes.length !== result.grant.bundleDescriptor.sizeBytes
  ) {
    throw new ModuleDeliveryProtocolError('size_mismatch', 'encrypted bundle size mismatch');
  }

  return {
    ...result,
    encryptedBundleBytes,
  };
}

export async function requestEncryptedModuleBundle(
  transport: ModuleDeliveryTransport,
  options: ModuleGrantRequestOptions,
): Promise<EncryptedModuleBundleResult> {
  const grant = await requestModuleGrant(transport, options);
  return fetchEncryptedModuleBundle(transport, grant);
}

export class ModuleDeliveryProtocolError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = 'ModuleDeliveryProtocolError';
    this.code = code;
  }
}

async function sendMessage(
  transport: ModuleDeliveryTransport,
  targetPeerId: string,
  candidateAddrs: string[],
  message: {
    type: 'grant_request' | 'grant_challenge' | 'grant_proof' | 'grant_response' | 'error_response';
    payload: Record<string, unknown>;
  },
) {
  const responseBytes = await transport.dialProtocol(
    targetPeerId,
    MODULE_DELIVERY_PROTOCOL_ID,
    encodeModuleDeliveryMessage(message),
    candidateAddrs,
  );
  return decodeModuleDeliveryMessage(responseBytes);
}

async function resolveCandidateAddresses(
  transport: ModuleDeliveryTransport,
  provider: NormalizedServerDescriptor,
  peerId: string,
  discoveryCID: string,
): Promise<string[]> {
  if (provider.relayAddresses.length > 0) {
    return provider.relayAddresses;
  }
  if (!transport.discoverProviders) {
    return [];
  }

  const candidates = await transport.discoverProviders(discoveryCID);
  return candidates
    .filter((candidate) => candidate.peerId === peerId)
    .flatMap((candidate) => candidate.multiaddrs);
}

function normalizeRequesterIdentity(
  identity: Pick<DerivedIdentity, 'peerId' | 'xpub' | 'signingKey' | 'encryptionKey'> | RequesterIdentity,
): RequesterIdentity {
  return {
    peerId: normalizeRequiredString(identity.peerId, 'requesterIdentity.peerId'),
    xpub: trimOptional(identity.xpub),
    signingKey: {
      privateKey: cloneBytes(identity.signingKey.privateKey),
      publicKey: cloneBytes(identity.signingKey.publicKey),
    },
    encryptionKey: {
      privateKey: cloneBytes(identity.encryptionKey.privateKey),
      publicKey: cloneBytes(identity.encryptionKey.publicKey),
    },
  };
}

function normalizeRequiredString(value: string, name: string): string {
  const normalized = value.trim();
  if (!normalized) {
    throw new Error(`${name} is required`);
  }
  return normalized;
}

function trimOptional(value: string | undefined): string | undefined {
  const normalized = String(value || '').trim();
  return normalized || undefined;
}

function cloneBytes(value: Uint8Array): Uint8Array {
  return value.slice();
}

function createReqId(): string {
  return `req-${Math.random().toString(36).slice(2, 10)}`;
}

async function sha256(value: Uint8Array): Promise<Uint8Array> {
  if (globalThis.crypto?.subtle) {
    const digest = await globalThis.crypto.subtle.digest('SHA-256', Uint8Array.from(value));
    return new Uint8Array(digest);
  }

  const cryptoModule = await import('node:crypto');
  return new Uint8Array(cryptoModule.createHash('sha256').update(Buffer.from(value)).digest());
}

function equalBytes(left: Uint8Array, right: Uint8Array): boolean {
  if (left.length !== right.length) {
    return false;
  }
  for (let index = 0; index < left.length; index += 1) {
    if (left[index] !== right[index]) {
      return false;
    }
  }
  return true;
}
