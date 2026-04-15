import {
  LicensingProtocolError,
  decodeLicensingChallengeMessage,
  decodeLicensingGrant,
  encodeLicensingChallengeRequest,
  encodeLicensingProof,
  extractGrantModuleDescriptor,
  extractWrappedContentKey,
  validateLicensingGrant,
} from 'space-data-module-sdk/licensing';

import type { DerivedIdentity, EncryptionKeyPair, KeyPair } from './crypto/types';
import type {
  LicensingGrantMessage,
  LicensingGrantModuleDescriptor,
  LicensingWrappedContentKey,
} from 'space-data-module-sdk/licensing';
import { sha256, sign } from './crypto/hd-wallet';
import { discoverProvider } from './discovery';
import {
  normalizeServerDescriptor,
  type NormalizedServerDescriptor,
  type ServerDescriptorInput,
  type ServerDescriptorResolver,
} from './server-descriptor';

export const MODULE_DELIVERY_PROTOCOL_ID = '/space-data-network/module-delivery/1.0.0';

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
  requesterDomain: string;
  requestedTimeoutMs: number;
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
  moduleId: string;
  moduleVersion?: string;
  requestedDomain?: string;
  requestedTimeoutMs: number;
  requestedAtMs: number;
  challengeNonce: Uint8Array;
  expiresAtMs: number;
  providerPeerId: string;
  rawBytes: Uint8Array;
}

interface BundleDescriptorPayload {
  cid: string;
  contentHash: Uint8Array;
  sizeBytes: number;
  moduleId: string;
  moduleVersion?: string;
  keyId?: string;
  requiredScope?: string;
  allowedDomains: string[];
  maxGrantTimeoutMs: number;
  encrypted: boolean;
}

interface WrappedContentKeyPayload {
  wrappingAlgorithm: string;
  contentKeyId?: string;
  contentKeyRole?: string;
  contentKeyAlgorithm?: string;
  contentKeyEncoding?: string;
  keyBytes: Uint8Array;
  contentKeyVersion?: number;
  recipientKeyId?: string;
  requesterEphemeralPublicKey: Uint8Array;
  providerEphemeralPublicKey: Uint8Array;
  hkdfSalt: Uint8Array;
  iv: Uint8Array;
  ciphertext: Uint8Array;
  tag: Uint8Array;
  expiresAtMs: number;
  recipientPublicKey: Uint8Array;
  ephemeralPublicKey: Uint8Array;
  nonce: Uint8Array;
  header?: WrappedContentKeyHeaderPayload;
  encryptedPayload?: Uint8Array;
  recipientKeyIdBytes?: Uint8Array;
  schemaHash?: Uint8Array;
  keyMaterialRootType?: string;
}

interface WrappedContentKeyHeaderPayload {
  version: number;
  keyExchange: string;
  symmetric: string;
  keyDerivation: string;
  ephemeralPublicKey: Uint8Array;
  nonceStart: Uint8Array;
  recipientKeyId: Uint8Array;
  context?: string;
  schemaHash: Uint8Array;
  rootType?: string;
  timestamp?: number;
}

interface GrantResponsePayload {
  reqId: string;
  moduleId: string;
  moduleVersion?: string;
  requestedDomain?: string;
  requestedTimeoutMs: number;
  grantedDomain: string;
  grantedTimeoutMs: number;
  expiresAtMs: number;
  requiredScope?: string;
  grantStatus?: string;
  capabilityToken: Uint8Array;
  grantVerifierPublicKey: Uint8Array;
  providerSignature: Uint8Array;
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
  const candidateAddrs = await resolveCandidateAddresses(
    transport,
    provider,
    discovery.peerId,
    discovery.discoveryCID,
  );
  const reqId = normalizeRequiredString(options.reqId || createReqId(), 'reqId');
  const requestedAtMs = options.requestedAtMs ?? Date.now();
  const requesterDomain = normalizeRequiredString(options.requesterDomain, 'requesterDomain');
  const requestedTimeoutMs = normalizeRequestedTimeoutMs(options.requestedTimeoutMs);
  const moduleId = normalizeRequiredString(options.moduleId, 'moduleId');
  const moduleVersion = trimOptional(options.moduleVersion);

  const challengeRequestBytes = encodeChallengeRequest({
    reqId,
    moduleId,
    moduleVersion,
    requesterPeerId: requesterIdentity.peerId,
    requesterXpub: trimOptional(requesterIdentity.xpub),
    requesterSigningPublicKey: requesterIdentity.signingKey.publicKey,
    requesterEphemeralPublicKey: requesterIdentity.encryptionKey.publicKey,
    requesterDomain,
    requestedTimeoutMs,
    requestedAtMs,
    providerPeerId: provider.peerId,
  });
  const challengeResponseBytes = await transport.dialProtocol(
    provider.peerId,
    MODULE_DELIVERY_PROTOCOL_ID,
    challengeRequestBytes,
    candidateAddrs,
  );
  const challenge = decodeChallengeResponse(challengeResponseBytes);
  console.info('[sdn-js] challenge received', {
    moduleId,
    reqId,
    expiresAtMs: challenge.expiresAtMs,
  });

  if (challenge.reqId !== reqId) {
    throw new ModuleDeliveryProtocolError('request_mismatch', 'grant challenge request id mismatch');
  }
  if (challenge.moduleId !== moduleId) {
    throw new ModuleDeliveryProtocolError('request_mismatch', 'grant challenge module id mismatch');
  }
  if (moduleVersion && challenge.moduleVersion && challenge.moduleVersion !== moduleVersion) {
    throw new ModuleDeliveryProtocolError('request_mismatch', 'grant challenge module version mismatch');
  }
  if (challenge.providerPeerId && challenge.providerPeerId !== provider.peerId) {
    throw new ModuleDeliveryProtocolError('provider_mismatch', 'provider peer id mismatch');
  }

  const signature = await sign(requesterIdentity.signingKey.privateKey, challenge.rawBytes);
  const proofBytes = encodeGrantProof({
    reqId,
    moduleId,
    moduleVersion,
    requesterPeerId: requesterIdentity.peerId,
    requesterXpub: trimOptional(requesterIdentity.xpub),
    requesterDomain,
    requestedTimeoutMs,
    requesterEphemeralPublicKey: requesterIdentity.encryptionKey.publicKey,
    challengeNonce: challenge.challengeNonce,
    challengeExpiresAtMs: challenge.expiresAtMs,
    providerPeerId: challenge.providerPeerId,
    signature,
    requesterSigningPublicKey: requesterIdentity.signingKey.publicKey,
    timestampMs: Date.now(),
  });
  const grantResponseBytes = await transport.dialProtocol(
    provider.peerId,
    MODULE_DELIVERY_PROTOCOL_ID,
    proofBytes,
    candidateAddrs,
  );
  const grant = decodeGrantResponse(grantResponseBytes, {
    reqId,
    moduleId,
    moduleVersion,
    expectedDomain: requesterDomain,
    requestedTimeoutMs,
  });
  console.info('[sdn-js] grant response received', {
    moduleId,
    reqId,
    grantedDomain: grant.grantedDomain,
    grantedTimeoutMs: grant.grantedTimeoutMs,
  });

  return { provider, grant };
}

export async function fetchEncryptedModuleBundle(
  transport: Pick<ModuleDeliveryTransport, 'fetchCIDBytes'>,
  result: ModuleGrantResult,
): Promise<EncryptedModuleBundleResult> {
  console.info('[sdn-js] fetching encrypted CID', {
    moduleId: result.grant.bundleDescriptor.moduleId,
    cid: result.grant.bundleDescriptor.cid,
  });
  const encryptedBundleBytes = await transport.fetchCIDBytes(result.grant.bundleDescriptor.cid);
  console.info('[sdn-js] fetched encrypted CID', {
    moduleId: result.grant.bundleDescriptor.moduleId,
    cid: result.grant.bundleDescriptor.cid,
    bytes: encryptedBundleBytes.length,
  });
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

function asModuleDeliveryProtocolError(
  error: unknown,
  fallbackCode: string,
): ModuleDeliveryProtocolError {
  if (error instanceof ModuleDeliveryProtocolError) {
    return error;
  }
  if (error instanceof LicensingProtocolError) {
    return new ModuleDeliveryProtocolError(error.code || fallbackCode, error.message);
  }
  if (error instanceof Error) {
    return new ModuleDeliveryProtocolError(fallbackCode, error.message);
  }
  return new ModuleDeliveryProtocolError(fallbackCode, String(error));
}

function encodeChallengeRequest(options: {
  reqId: string;
  moduleId: string;
  moduleVersion?: string;
  requesterPeerId: string;
  requesterXpub?: string;
  requesterSigningPublicKey: Uint8Array;
  requesterEphemeralPublicKey: Uint8Array;
  requesterDomain: string;
  requestedTimeoutMs: number;
  requestedAtMs: number;
  providerPeerId: string;
}): Uint8Array {
  return encodeLicensingChallengeRequest(options);
}

function encodeGrantProof(options: {
  reqId: string;
  moduleId: string;
  moduleVersion?: string;
  requesterPeerId: string;
  requesterXpub?: string;
  requesterDomain: string;
  requestedTimeoutMs: number;
  requesterEphemeralPublicKey: Uint8Array;
  challengeNonce: Uint8Array;
  challengeExpiresAtMs: number;
  providerPeerId: string;
  signature: Uint8Array;
  requesterSigningPublicKey: Uint8Array;
  timestampMs: number;
}): Uint8Array {
  return encodeLicensingProof(options);
}

function decodeChallengeResponse(bytes: Uint8Array): GrantChallengePayload {
  try {
    const message = decodeLicensingChallengeMessage(bytes);
    if (message.messageType === 'error') {
      throw new ModuleDeliveryProtocolError(
        normalizeProtocolCode(message.errorCode, 'challenge_rejected'),
        message.errorMessage || 'licensing challenge rejected',
      );
    }
    if (message.messageType !== 'response' || message.role !== 'provider') {
      throw new ModuleDeliveryProtocolError('unexpected_response', 'expected licensing challenge response');
    }

    return {
      reqId: message.reqId,
      moduleId: message.moduleId,
      moduleVersion: trimOptional(message.moduleVersion),
      requestedDomain: trimOptional(message.requestedDomain),
      requestedTimeoutMs: message.requestedTimeoutMs ?? 0,
      requestedAtMs: message.requestedAtMs ?? 0,
      challengeNonce: cloneOptionalBytes(message.challengeNonce),
      expiresAtMs: message.expiresAtMs ?? 0,
      providerPeerId: trimOptional(message.providerPeerId) || '',
      rawBytes: cloneBytes(message.rawBytes),
    };
  } catch (error) {
    throw asModuleDeliveryProtocolError(error, 'invalid_response');
  }
}

function decodeGrantResponse(
  bytes: Uint8Array,
  options: {
    reqId: string;
    moduleId: string;
    moduleVersion?: string;
    expectedDomain: string;
    requestedTimeoutMs: number;
  },
): GrantResponsePayload {
  try {
    const decodedGrant = decodeLicensingGrant(bytes);
    const validatedGrant = validateLicensingGrant(decodedGrant, options);
    const bundleDescriptor = extractGrantModuleDescriptor(validatedGrant);
    const wrappedContentKey = extractWrappedContentKey(validatedGrant);

    return mapLicensingGrant(validatedGrant, bundleDescriptor, wrappedContentKey);
  } catch (error) {
    throw asModuleDeliveryProtocolError(error, 'invalid_grant');
  }
}

function mapLicensingGrant(
  grant: LicensingGrantMessage,
  bundleDescriptor: LicensingGrantModuleDescriptor,
  wrappedContentKey: LicensingWrappedContentKey,
): GrantResponsePayload {
  return {
    reqId: grant.reqId,
    moduleId: grant.moduleId,
    moduleVersion: trimOptional(grant.moduleVersion),
    requestedDomain: trimOptional(grant.requestedDomain),
    requestedTimeoutMs: grant.requestedTimeoutMs,
    grantedDomain: normalizeRequiredString(grant.grantedDomain || '', 'grant.grantedDomain'),
    grantedTimeoutMs: grant.grantedTimeoutMs,
    expiresAtMs: grant.expiresAtMs,
    requiredScope: trimOptional(grant.requiredScope),
    grantStatus: trimOptional(grant.grantStatus),
    capabilityToken: cloneOptionalBytes(grant.capabilityToken),
    grantVerifierPublicKey: cloneOptionalBytes(grant.grantVerifierPublicKey),
    providerSignature: cloneOptionalBytes(grant.providerSignature),
    bundleDescriptor: mapBundleDescriptor(bundleDescriptor),
    wrappedContentKey: mapWrappedContentKey(wrappedContentKey),
  };
}

function mapBundleDescriptor(descriptor: LicensingGrantModuleDescriptor): BundleDescriptorPayload {
  return {
    cid: descriptor.cid,
    contentHash: cloneOptionalBytes(descriptor.contentHash),
    sizeBytes: descriptor.sizeBytes,
    moduleId: normalizeRequiredString(descriptor.moduleId, 'bundleDescriptor.moduleId'),
    moduleVersion: trimOptional(descriptor.moduleVersion),
    keyId: trimOptional(descriptor.keyId),
    requiredScope: trimOptional(descriptor.requiredScope),
    allowedDomains: descriptor.allowedDomains.slice(),
    maxGrantTimeoutMs: descriptor.maxGrantTimeoutMs,
    encrypted: descriptor.encrypted,
  };
}

function mapWrappedContentKey(key: LicensingWrappedContentKey): WrappedContentKeyPayload {
  return {
    wrappingAlgorithm: key.wrappingAlgorithm,
    contentKeyId: trimOptional(key.contentKeyId),
    contentKeyRole: trimOptional(key.contentKeyRole),
    contentKeyAlgorithm: trimOptional(key.contentKeyAlgorithm),
    contentKeyEncoding: trimOptional(key.contentKeyEncoding),
    keyBytes: cloneOptionalBytes(key.keyBytes),
    contentKeyVersion: key.contentKeyVersion,
    recipientKeyId: trimOptional(key.recipientKeyId),
    requesterEphemeralPublicKey: cloneOptionalBytes(key.requesterEphemeralPublicKey),
    providerEphemeralPublicKey: cloneOptionalBytes(key.providerEphemeralPublicKey),
    hkdfSalt: cloneOptionalBytes(key.hkdfSalt),
    iv: cloneOptionalBytes(key.iv),
    ciphertext: cloneOptionalBytes(key.ciphertext),
    tag: cloneOptionalBytes(key.tag),
    expiresAtMs: key.expiresAtMs,
    recipientPublicKey: cloneOptionalBytes(key.recipientPublicKey),
    ephemeralPublicKey: cloneOptionalBytes(key.ephemeralPublicKey),
    nonce: cloneOptionalBytes(key.nonce),
    header: mapWrappedContentKeyHeader(key.header),
    encryptedPayload: cloneOptionalBytes(key.encryptedPayload),
    recipientKeyIdBytes: cloneOptionalBytes(key.recipientKeyIdBytes),
    schemaHash: cloneOptionalBytes(key.schemaHash),
    keyMaterialRootType: trimOptional(key.keyMaterialRootType),
  };
}

function mapWrappedContentKeyHeader(
  header: LicensingWrappedContentKey['header'],
): WrappedContentKeyHeaderPayload {
  return {
    version: header.version,
    keyExchange: header.keyExchange,
    symmetric: header.symmetric,
    keyDerivation: header.keyDerivation,
    ephemeralPublicKey: cloneOptionalBytes(header.ephemeralPublicKey),
    nonceStart: cloneOptionalBytes(header.nonceStart),
    recipientKeyId: cloneOptionalBytes(header.recipientKeyId),
    context: trimOptional(header.context),
    schemaHash: cloneOptionalBytes(header.schemaHash),
    rootType: trimOptional(header.rootType),
    timestamp: header.timestamp,
  };
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

function trimOptional(value: string | undefined | null): string | undefined {
  const normalized = String(value || '').trim();
  return normalized || undefined;
}

function cloneBytes(value: Uint8Array): Uint8Array {
  return value.slice();
}

function cloneOptionalBytes(value: Uint8Array | null | undefined): Uint8Array {
  return value ? value.slice() : new Uint8Array(0);
}

function createReqId(): string {
  return `req-${Math.random().toString(36).slice(2, 10)}`;
}

function normalizeRequestedTimeoutMs(value: number): number {
  if (!Number.isFinite(value) || value <= 0) {
    throw new Error('requestedTimeoutMs must be a positive number');
  }
  return Math.trunc(value);
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

function normalizeProtocolCode(value: string | null | undefined, fallback: string): string {
  const normalized = String(value || '').trim();
  return normalized || fallback;
}
