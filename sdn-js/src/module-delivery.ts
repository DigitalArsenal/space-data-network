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
import * as flatbuffers from 'flatbuffers';
import { KMF } from 'spacedatastandards.org/lib/js/REC/KMF.js';
import { LGR } from 'spacedatastandards.org/lib/js/REC/LGR.js';
import { PLG } from 'spacedatastandards.org/lib/js/REC/PLG.js';
import { licensingGrantMessageType } from 'spacedatastandards.org/lib/js/REC/licensingGrantMessageType.js';

import type { DerivedIdentity, EncryptionKeyPair, KeyPair } from './crypto/types';
import type {
  LicensingGrantMessage,
  LicensingGrantModuleDescriptor,
  LicensingWrappedContentKey,
} from 'space-data-module-sdk/licensing';
import { sha256, sign, verify } from './crypto/hd-wallet';
import { discoverProvider } from './discovery';
import {
  normalizeServerDescriptor,
  type NormalizedServerDescriptor,
  type ServerDescriptorInput,
  type ServerDescriptorResolver,
} from './server-descriptor';

export const MODULE_DELIVERY_PROTOCOL_ID = '/space-data-network/module-delivery/1.0.0';

export type ModuleDeliveryStage =
  | 'provider-discovery'
  | 'challenge-sent'
  | 'challenge-received'
  | 'grant-received'
  | 'cid-fetch-start'
  | 'cid-fetch-complete'
  | 'cid-fetch-validated'
  | 'cid-fetch-error'
  | 'unwrap-start'
  | 'unwrap-complete'
  | 'decrypt-start'
  | 'decrypt-complete'
  | 'sdk-load-start'
  | 'sdk-load-complete'
  | 'invoke-start'
  | 'invoke-result'
  | 'invoke-error';

export interface ModuleDeliveryEvent {
  stage: ModuleDeliveryStage;
  timestamp: number;
  moduleId?: string;
  moduleVersion?: string;
  providerPeerId?: string;
  cid?: string;
  bytes?: number;
  detail?: string;
  error?: string;
  candidateAddrs?: string[];
  discoveryCID?: string;
}

export interface ModuleDeliveryObserver {
  onEvent?: (event: ModuleDeliveryEvent) => void;
}

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
  observer?: ModuleDeliveryObserver;
}

export interface ModuleGrantResult {
  provider: NormalizedServerDescriptor;
  grant: GrantResponsePayload;
  grantResponseBytes: Uint8Array;
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
  const observer = options.observer;

  emitModuleDeliveryEvent(observer, {
    stage: 'provider-discovery',
    timestamp: Date.now(),
    moduleId,
    moduleVersion,
    providerPeerId: provider.peerId,
    candidateAddrs: candidateAddrs.slice(),
    discoveryCID: discovery.discoveryCID,
    detail: provider.relayAddresses.length > 0 ? 'descriptor-relays' : 'dht-discovery',
  });

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
  emitModuleDeliveryEvent(observer, {
    stage: 'challenge-sent',
    timestamp: Date.now(),
    moduleId,
    moduleVersion,
    providerPeerId: provider.peerId,
    candidateAddrs: candidateAddrs.slice(),
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
  emitModuleDeliveryEvent(observer, {
    stage: 'challenge-received',
    timestamp: Date.now(),
    moduleId,
    moduleVersion,
    providerPeerId: challenge.providerPeerId || provider.peerId,
    detail: `expiresAt=${challenge.expiresAtMs}`,
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
  const grant = await decodeGrantResponse(grantResponseBytes, {
    reqId,
    moduleId,
    moduleVersion,
    expectedDomain: requesterDomain,
    requestedTimeoutMs,
    requestedAtMs,
    trustedGrantVerifierPublicKeys: provider.grantVerifierPublicKeys,
  });
  console.info('[sdn-js] grant response received', {
    moduleId,
    reqId,
    grantedDomain: grant.grantedDomain,
    grantedTimeoutMs: grant.grantedTimeoutMs,
  });
  emitModuleDeliveryEvent(observer, {
    stage: 'grant-received',
    timestamp: Date.now(),
    moduleId,
    moduleVersion,
    providerPeerId: provider.peerId,
    cid: grant.bundleDescriptor.cid,
    detail: `grantedDomain=${grant.grantedDomain}`,
  });

  return { provider, grant, grantResponseBytes: grantResponseBytes.slice() };
}

export async function fetchEncryptedModuleBundle(
  transport: Pick<ModuleDeliveryTransport, 'fetchCIDBytes'>,
  result: ModuleGrantResult,
  observer?: ModuleDeliveryObserver,
): Promise<EncryptedModuleBundleResult> {
  console.info('[sdn-js] fetching encrypted CID', {
    moduleId: result.grant.bundleDescriptor.moduleId,
    cid: result.grant.bundleDescriptor.cid,
  });
  emitModuleDeliveryEvent(observer, {
    stage: 'cid-fetch-start',
    timestamp: Date.now(),
    moduleId: result.grant.bundleDescriptor.moduleId,
    moduleVersion: result.grant.bundleDescriptor.moduleVersion,
    providerPeerId: result.provider.peerId,
    cid: result.grant.bundleDescriptor.cid,
  });
  const encryptedBundleBytes = await transport.fetchCIDBytes(result.grant.bundleDescriptor.cid);
  console.info('[sdn-js] fetched encrypted CID', {
    moduleId: result.grant.bundleDescriptor.moduleId,
    cid: result.grant.bundleDescriptor.cid,
    bytes: encryptedBundleBytes.length,
  });
  emitModuleDeliveryEvent(observer, {
    stage: 'cid-fetch-complete',
    timestamp: Date.now(),
    moduleId: result.grant.bundleDescriptor.moduleId,
    moduleVersion: result.grant.bundleDescriptor.moduleVersion,
    providerPeerId: result.provider.peerId,
    cid: result.grant.bundleDescriptor.cid,
    bytes: encryptedBundleBytes.length,
  });
  const digest = await sha256(encryptedBundleBytes);

  if (
    result.grant.bundleDescriptor.contentHash.length > 0 &&
    !equalBytes(digest, result.grant.bundleDescriptor.contentHash)
  ) {
    emitModuleDeliveryEvent(observer, {
      stage: 'cid-fetch-error',
      timestamp: Date.now(),
      moduleId: result.grant.bundleDescriptor.moduleId,
      moduleVersion: result.grant.bundleDescriptor.moduleVersion,
      providerPeerId: result.provider.peerId,
      cid: result.grant.bundleDescriptor.cid,
      error: 'encrypted bundle hash mismatch',
    });
    throw new ModuleDeliveryProtocolError('hash_mismatch', 'encrypted bundle hash mismatch');
  }

  if (
    result.grant.bundleDescriptor.sizeBytes > 0 &&
    encryptedBundleBytes.length !== result.grant.bundleDescriptor.sizeBytes
  ) {
    emitModuleDeliveryEvent(observer, {
      stage: 'cid-fetch-error',
      timestamp: Date.now(),
      moduleId: result.grant.bundleDescriptor.moduleId,
      moduleVersion: result.grant.bundleDescriptor.moduleVersion,
      providerPeerId: result.provider.peerId,
      cid: result.grant.bundleDescriptor.cid,
      error: 'encrypted bundle size mismatch',
    });
    throw new ModuleDeliveryProtocolError('size_mismatch', 'encrypted bundle size mismatch');
  }
  emitModuleDeliveryEvent(observer, {
    stage: 'cid-fetch-validated',
    timestamp: Date.now(),
    moduleId: result.grant.bundleDescriptor.moduleId,
    moduleVersion: result.grant.bundleDescriptor.moduleVersion,
    providerPeerId: result.provider.peerId,
    cid: result.grant.bundleDescriptor.cid,
    bytes: encryptedBundleBytes.length,
  });

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
  return fetchEncryptedModuleBundle(transport, grant, options.observer);
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

async function decodeGrantResponse(
  bytes: Uint8Array,
  options: {
    reqId: string;
    moduleId: string;
    moduleVersion?: string;
    expectedDomain: string;
    requestedTimeoutMs: number;
    requestedAtMs: number;
    trustedGrantVerifierPublicKeys?: Uint8Array[];
  },
): Promise<GrantResponsePayload> {
  try {
    const decodedGrant = decodeLicensingGrant(bytes);
    const validatedGrant = validateLicensingGrant(decodedGrant, options);
    await validateGrantEnvelope(validatedGrant, options.requestedAtMs, options.trustedGrantVerifierPublicKeys);
    const bundleDescriptor = extractGrantModuleDescriptor(validatedGrant);
    const wrappedContentKey = extractWrappedContentKey(validatedGrant);
    validateWrappedContentKeyEnvelope(wrappedContentKey);

    return mapLicensingGrant(validatedGrant, bundleDescriptor, wrappedContentKey);
  } catch (error) {
    throw asModuleDeliveryProtocolError(error, 'invalid_grant');
  }
}

async function validateGrantEnvelope(
  grant: LicensingGrantMessage,
  requestedAtMs: number,
  trustedGrantVerifierPublicKeys: Uint8Array[] = [],
): Promise<void> {
  const providerSignature = cloneOptionalBytes(grant.providerSignature);
  if (providerSignature.length !== 64) {
    throw new ModuleDeliveryProtocolError(
      'invalid_grant',
      'licensing grant provider signature must be 64 bytes',
    );
  }
  if (providerSignature.every((byte) => byte === 0)) {
    throw new ModuleDeliveryProtocolError(
      'invalid_grant',
      'licensing grant provider signature must not be all zeroes',
    );
  }

  if (grant.expiresAtMs > 0 && grant.expiresAtMs <= requestedAtMs) {
    throw new ModuleDeliveryProtocolError('grant_expired', 'licensing grant has expired');
  }

  const status = trimOptional(grant.grantStatus)?.toLowerCase();
  if (status === 'revoked') {
    throw new ModuleDeliveryProtocolError('grant_revoked', 'licensing grant has been revoked');
  }
  if (status && status !== 'active' && status !== 'granted') {
    throw new ModuleDeliveryProtocolError(
      'grant_status_invalid',
      `licensing grant status is not active: ${status}`,
    );
  }

  const grantVerifierPublicKey = cloneOptionalBytes(grant.grantVerifierPublicKey);
  if (
    trustedGrantVerifierPublicKeys.length > 0 &&
    !trustedGrantVerifierPublicKeys.some((trustedKey) => equalBytes(trustedKey, grantVerifierPublicKey))
  ) {
    throw new ModuleDeliveryProtocolError(
      'invalid_grant_verifier',
      'licensing grant verifier public key is not advertised by the provider EPM',
    );
  }

  const unsignedGrant = encodeUnsignedGrantForProviderSignature(grant);
  const signatureValid = await verify(
    grantVerifierPublicKey,
    unsignedGrant,
    providerSignature,
  );
  if (!signatureValid) {
    throw new ModuleDeliveryProtocolError(
      'invalid_grant_signature',
      'licensing grant provider signature verification failed',
    );
  }
}

function validateWrappedContentKeyEnvelope(wrappedContentKey: LicensingWrappedContentKey): void {
  const rootType = trimOptional(
    wrappedContentKey.header?.rootType ?? wrappedContentKey.keyMaterialRootType,
  )?.replace(/^\$/, '').toUpperCase();
  if (rootType !== 'KMF') {
    return;
  }

  const payload = cloneOptionalBytes(wrappedContentKey.encryptedPayload);
  if (!KMF.bufferHasIdentifier(new flatbuffers.ByteBuffer(payload))) {
    throw new ModuleDeliveryProtocolError(
      'invalid_grant',
      'wrapped content key payload is not a valid KMF envelope',
    );
  }
}

function encodeUnsignedGrantForProviderSignature(grant: LicensingGrantMessage): Uint8Array {
  const rawBytes = cloneOptionalBytes(grant.rawBytes);
  if (rawBytes.length === 0) {
    return encodeUnsignedGrantFromDecodedMessage(grant);
  }

  const root = LGR.getRootAsLGR(new flatbuffers.ByteBuffer(rawBytes));
  if (root.MESSAGE_TYPE() !== licensingGrantMessageType.Granted) {
    throw new ModuleDeliveryProtocolError('invalid_grant', 'expected granted licensing record');
  }

  const builder = new flatbuffers.Builder(Math.max(2048, rawBytes.length));
  const requestIdOffset = builder.createString(root.REQUEST_ID() || '');
  const moduleIdOffset = builder.createString(root.MODULE_ID() || '');
  const moduleVersionOffset = createOptionalString(builder, root.MODULE_VERSION());
  const requesterPeerIdOffset = createOptionalString(builder, root.REQUESTER_PEER_ID());
  const requesterXpubOffset = createOptionalString(builder, root.REQUESTER_XPUB());
  const requestedDomainOffset = createOptionalString(builder, root.REQUESTED_DOMAIN());
  const grantedDomainOffset = createOptionalString(builder, root.GRANTED_DOMAIN());
  const requiredScopeOffset = createOptionalString(builder, root.REQUIRED_SCOPE());
  const grantStatusOffset = createOptionalString(builder, root.GRANT_STATUS());
  const denialReasonOffset = createOptionalString(builder, root.DENIAL_REASON());
  const capabilityTokenOffset = createOptionalVector(
    builder,
    LGR.createCapabilityTokenVector,
    root.capabilityTokenArray(),
  );
  const moduleDescriptor = root.MODULE_DESCRIPTOR();
  const moduleDescriptorOffset = moduleDescriptor
    ? createModuleDescriptorForSignature(builder, moduleDescriptor)
    : 0;
  const wrappedHeaderOffset = root.WRAPPED_CONTENT_KEY_HEADER()?.unpack().pack(builder) ?? 0;
  const wrappedPayloadOffset = createOptionalVector(
    builder,
    LGR.createWrappedContentKeyPayloadVector,
    root.wrappedContentKeyPayloadArray(),
  );
  const verifierPubkeyOffset = createOptionalVector(
    builder,
    LGR.createGrantVerifierPubkeyVector,
    root.grantVerifierPubkeyArray(),
  );

  LGR.startLGR(builder);
  LGR.addMessageType(builder, licensingGrantMessageType.Granted);
  LGR.addRequestId(builder, requestIdOffset);
  LGR.addModuleId(builder, moduleIdOffset);
  if (moduleVersionOffset !== 0) {
    LGR.addModuleVersion(builder, moduleVersionOffset);
  }
  if (requesterPeerIdOffset !== 0) {
    LGR.addRequesterPeerId(builder, requesterPeerIdOffset);
  }
  if (requesterXpubOffset !== 0) {
    LGR.addRequesterXpub(builder, requesterXpubOffset);
  }
  if (requestedDomainOffset !== 0) {
    LGR.addRequestedDomain(builder, requestedDomainOffset);
  }
  LGR.addRequestedTimeoutMs(builder, root.REQUESTED_TIMEOUT_MS());
  if (grantedDomainOffset !== 0) {
    LGR.addGrantedDomain(builder, grantedDomainOffset);
  }
  LGR.addGrantedTimeoutMs(builder, root.GRANTED_TIMEOUT_MS());
  LGR.addExpiresAt(builder, root.EXPIRES_AT());
  if (requiredScopeOffset !== 0) {
    LGR.addRequiredScope(builder, requiredScopeOffset);
  }
  if (grantStatusOffset !== 0) {
    LGR.addGrantStatus(builder, grantStatusOffset);
  }
  if (denialReasonOffset !== 0) {
    LGR.addDenialReason(builder, denialReasonOffset);
  }
  if (capabilityTokenOffset !== 0) {
    LGR.addCapabilityToken(builder, capabilityTokenOffset);
  }
  if (moduleDescriptorOffset !== 0) {
    LGR.addModuleDescriptor(builder, moduleDescriptorOffset);
  }
  if (wrappedHeaderOffset !== 0) {
    LGR.addWrappedContentKeyHeader(builder, wrappedHeaderOffset);
  }
  if (wrappedPayloadOffset !== 0) {
    LGR.addWrappedContentKeyPayload(builder, wrappedPayloadOffset);
  }
  if (verifierPubkeyOffset !== 0) {
    LGR.addGrantVerifierPubkey(builder, verifierPubkeyOffset);
  }
  const rootOffset = LGR.endLGR(builder);
  LGR.finishLGRBuffer(builder, rootOffset);
  return builder.asUint8Array();
}

function createModuleDescriptorForSignature(
  builder: flatbuffers.Builder,
  descriptor: PLG,
): flatbuffers.Offset {
  const pluginIdOffset = builder.createString(descriptor.PLUGIN_ID() || '');
  const nameOffset = createOptionalString(builder, descriptor.NAME());
  const versionOffset = createOptionalString(builder, descriptor.VERSION());
  const descriptionOffset = createOptionalString(builder, descriptor.DESCRIPTION());
  const wasmHashOffset = createOptionalVector(
    builder,
    PLG.createWasmHashVector,
    descriptor.wasmHashArray(),
  );
  const wasmCidOffset = createOptionalString(builder, descriptor.WASM_CID());
  const encryptedWasmHashOffset = createOptionalVector(
    builder,
    PLG.createEncryptedWasmHashVector,
    descriptor.encryptedWasmHashArray(),
  );
  const requiredScopeOffset = createOptionalString(builder, descriptor.REQUIRED_SCOPE());
  const keyIdOffset = createOptionalString(builder, descriptor.KEY_ID());
  const allowedDomainsOffset = createOptionalStringVector(
    builder,
    PLG.createAllowedDomainsVector,
    descriptor.allowedDomainsLength(),
    (index) => descriptor.ALLOWED_DOMAINS(index),
  );

  PLG.startPLG(builder);
  PLG.addPluginId(builder, pluginIdOffset);
  if (nameOffset !== 0) {
    PLG.addName(builder, nameOffset);
  }
  if (versionOffset !== 0) {
    PLG.addVersion(builder, versionOffset);
  }
  if (descriptionOffset !== 0) {
    PLG.addDescription(builder, descriptionOffset);
  }
  PLG.addPluginType(builder, descriptor.PLUGIN_TYPE());
  PLG.addAbiVersion(builder, descriptor.ABI_VERSION());
  if (wasmHashOffset !== 0) {
    PLG.addWasmHash(builder, wasmHashOffset);
  }
  PLG.addWasmSize(builder, descriptor.WASM_SIZE());
  if (wasmCidOffset !== 0) {
    PLG.addWasmCid(builder, wasmCidOffset);
  }
  if (encryptedWasmHashOffset !== 0) {
    PLG.addEncryptedWasmHash(builder, encryptedWasmHashOffset);
  }
  PLG.addEncryptedWasmSize(builder, descriptor.ENCRYPTED_WASM_SIZE());
  PLG.addEncrypted(builder, descriptor.ENCRYPTED());
  if (requiredScopeOffset !== 0) {
    PLG.addRequiredScope(builder, requiredScopeOffset);
  }
  if (keyIdOffset !== 0) {
    PLG.addKeyId(builder, keyIdOffset);
  }
  if (allowedDomainsOffset !== 0) {
    PLG.addAllowedDomains(builder, allowedDomainsOffset);
  }
  PLG.addMaxGrantTimeoutMs(builder, descriptor.MAX_GRANT_TIMEOUT_MS());
  return PLG.endPLG(builder);
}

function encodeUnsignedGrantFromDecodedMessage(grant: LicensingGrantMessage): Uint8Array {
  const builder = new flatbuffers.Builder(512);
  const requestIdOffset = builder.createString(grant.reqId);
  const moduleIdOffset = builder.createString(grant.moduleId);
  const moduleVersionOffset = createOptionalString(builder, grant.moduleVersion);
  const requesterPeerIdOffset = createOptionalString(builder, grant.requesterPeerId);
  const requesterXpubOffset = createOptionalString(builder, grant.requesterXpub);
  const requestedDomainOffset = createOptionalString(builder, grant.requestedDomain);
  const grantedDomainOffset = createOptionalString(builder, grant.grantedDomain);
  const requiredScopeOffset = createOptionalString(builder, grant.requiredScope);
  const grantStatusOffset = createOptionalString(builder, grant.grantStatus);
  const denialReasonOffset = createOptionalString(builder, grant.denialReason);
  const capabilityTokenOffset = createOptionalVector(
    builder,
    LGR.createCapabilityTokenVector,
    grant.capabilityToken,
  );
  const verifierPubkeyOffset = createOptionalVector(
    builder,
    LGR.createGrantVerifierPubkeyVector,
    grant.grantVerifierPublicKey,
  );

  LGR.startLGR(builder);
  LGR.addMessageType(builder, licensingGrantMessageType.Granted);
  LGR.addRequestId(builder, requestIdOffset);
  LGR.addModuleId(builder, moduleIdOffset);
  if (moduleVersionOffset !== 0) {
    LGR.addModuleVersion(builder, moduleVersionOffset);
  }
  if (requesterPeerIdOffset !== 0) {
    LGR.addRequesterPeerId(builder, requesterPeerIdOffset);
  }
  if (requesterXpubOffset !== 0) {
    LGR.addRequesterXpub(builder, requesterXpubOffset);
  }
  if (requestedDomainOffset !== 0) {
    LGR.addRequestedDomain(builder, requestedDomainOffset);
  }
  LGR.addRequestedTimeoutMs(builder, BigInt(grant.requestedTimeoutMs));
  if (grantedDomainOffset !== 0) {
    LGR.addGrantedDomain(builder, grantedDomainOffset);
  }
  LGR.addGrantedTimeoutMs(builder, BigInt(grant.grantedTimeoutMs));
  LGR.addExpiresAt(builder, BigInt(grant.expiresAtMs));
  if (requiredScopeOffset !== 0) {
    LGR.addRequiredScope(builder, requiredScopeOffset);
  }
  if (grantStatusOffset !== 0) {
    LGR.addGrantStatus(builder, grantStatusOffset);
  }
  if (denialReasonOffset !== 0) {
    LGR.addDenialReason(builder, denialReasonOffset);
  }
  if (capabilityTokenOffset !== 0) {
    LGR.addCapabilityToken(builder, capabilityTokenOffset);
  }
  if (verifierPubkeyOffset !== 0) {
    LGR.addGrantVerifierPubkey(builder, verifierPubkeyOffset);
  }
  const rootOffset = LGR.endLGR(builder);
  LGR.finishLGRBuffer(builder, rootOffset);
  return builder.asUint8Array();
}

function createOptionalString(
  builder: flatbuffers.Builder,
  value: string | null | undefined,
): flatbuffers.Offset {
  const normalized = trimOptional(value);
  return normalized ? builder.createString(normalized) : 0;
}

function createOptionalVector(
  builder: flatbuffers.Builder,
  createVector: (builder: flatbuffers.Builder, data: Uint8Array) => flatbuffers.Offset,
  value: Uint8Array | null | undefined,
): flatbuffers.Offset {
  const bytes = cloneOptionalBytes(value);
  return bytes.length > 0 ? createVector(builder, bytes) : 0;
}

function createOptionalStringVector(
  builder: flatbuffers.Builder,
  createVector: (builder: flatbuffers.Builder, data: flatbuffers.Offset[]) => flatbuffers.Offset,
  length: number,
  readValue: (index: number) => string | null,
): flatbuffers.Offset {
  if (length <= 0) {
    return 0;
  }
  const offsets: flatbuffers.Offset[] = [];
  for (let index = 0; index < length; index += 1) {
    const value = trimOptional(readValue(index));
    if (value) {
      offsets.push(builder.createString(value));
    }
  }
  return offsets.length > 0 ? createVector(builder, offsets) : 0;
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

function emitModuleDeliveryEvent(
  observer: ModuleDeliveryObserver | undefined,
  event: ModuleDeliveryEvent,
): void {
  observer?.onEvent?.(event);
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
