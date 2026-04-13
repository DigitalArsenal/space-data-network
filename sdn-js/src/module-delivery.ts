import * as flatbuffers from 'flatbuffers';
import { LCH } from 'spacedatastandards.org/lib/js/REC/LCH.js';
import { LGR } from 'spacedatastandards.org/lib/js/REC/LGR.js';
import { LPF } from 'spacedatastandards.org/lib/js/REC/LPF.js';
import { PLG } from 'spacedatastandards.org/lib/js/REC/PLG.js';
import { licensingChallengeMessageType } from 'spacedatastandards.org/lib/js/REC/licensingChallengeMessageType.js';
import { licensingChallengeRole } from 'spacedatastandards.org/lib/js/REC/licensingChallengeRole.js';
import { licensingGrantMessageType } from 'spacedatastandards.org/lib/js/REC/licensingGrantMessageType.js';
import { licensingProofMessageType } from 'spacedatastandards.org/lib/js/REC/licensingProofMessageType.js';
import { licensingWrappedKeyAlgorithm } from 'spacedatastandards.org/lib/js/REC/licensingWrappedKeyAlgorithm.js';

import type { DerivedIdentity, EncryptionKeyPair, KeyPair } from './crypto/types';
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
  encrypted: boolean;
}

interface WrappedContentKeyPayload {
  wrappingAlgorithm: string;
  contentKeyId?: string;
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
  const grant = decodeGrantResponse(grantResponseBytes);
  console.info('[sdn-js] grant response received', {
    moduleId,
    reqId,
    grantedDomain: grant.grantedDomain,
    grantedTimeoutMs: grant.grantedTimeoutMs,
  });

  if (grant.reqId !== reqId) {
    throw new ModuleDeliveryProtocolError('request_mismatch', 'grant response request id mismatch');
  }
  if (grant.moduleId !== moduleId) {
    throw new ModuleDeliveryProtocolError('request_mismatch', 'grant response module id mismatch');
  }
  if (moduleVersion && grant.moduleVersion && grant.moduleVersion !== moduleVersion) {
    throw new ModuleDeliveryProtocolError('request_mismatch', 'grant response module version mismatch');
  }
  if (grant.grantedDomain !== requesterDomain) {
    throw new ModuleDeliveryProtocolError(
      'grant_policy_mismatch',
      'grant domain does not match the requested domain',
    );
  }
  if (grant.grantedTimeoutMs <= 0 || grant.grantedTimeoutMs > requestedTimeoutMs) {
    throw new ModuleDeliveryProtocolError(
      'grant_policy_mismatch',
      'grant timeout exceeds the requested timeout',
    );
  }
  if (grant.grantVerifierPublicKey.length !== 32) {
    throw new ModuleDeliveryProtocolError(
      'invalid_grant',
      'grant verifier public key must be 32 bytes',
    );
  }

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
  const builder = new flatbuffers.Builder(512);
  const reqIdOffset = builder.createString(options.reqId);
  const moduleIdOffset = builder.createString(options.moduleId);
  const moduleVersionOffset = options.moduleVersion ? builder.createString(options.moduleVersion) : 0;
  const requesterPeerIdOffset = builder.createString(options.requesterPeerId);
  const requesterXpubOffset = options.requesterXpub ? builder.createString(options.requesterXpub) : 0;
  const requesterSigningPubkeyOffset = LCH.createRequesterSigningPubkeyVector(
    builder,
    options.requesterSigningPublicKey,
  );
  const requesterEphemeralPubkeyOffset = LCH.createRequesterEphemeralPubkeyVector(
    builder,
    options.requesterEphemeralPublicKey,
  );
  const requesterDomainOffset = builder.createString(options.requesterDomain);
  const providerPeerIdOffset = builder.createString(options.providerPeerId);
  const root = LCH.createLCH(
    builder,
    licensingChallengeMessageType.Request,
    licensingChallengeRole.Requester,
    reqIdOffset,
    moduleIdOffset,
    moduleVersionOffset,
    requesterPeerIdOffset,
    requesterXpubOffset,
    requesterSigningPubkeyOffset,
    requesterEphemeralPubkeyOffset,
    requesterDomainOffset,
    BigInt(options.requestedTimeoutMs),
    BigInt(options.requestedAtMs),
    0,
    0n,
    providerPeerIdOffset,
    0,
    0,
  );
  LCH.finishLCHBuffer(builder, root);
  return builder.asUint8Array();
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
  const builder = new flatbuffers.Builder(512);
  const reqIdOffset = builder.createString(options.reqId);
  const moduleIdOffset = builder.createString(options.moduleId);
  const moduleVersionOffset = options.moduleVersion ? builder.createString(options.moduleVersion) : 0;
  const requesterPeerIdOffset = builder.createString(options.requesterPeerId);
  const requesterXpubOffset = options.requesterXpub ? builder.createString(options.requesterXpub) : 0;
  const requesterDomainOffset = builder.createString(options.requesterDomain);
  const requesterEphemeralPubkeyOffset = LPF.createRequesterEphemeralPubkeyVector(
    builder,
    options.requesterEphemeralPublicKey,
  );
  const challengeNonceOffset = LPF.createChallengeNonceVector(builder, options.challengeNonce);
  const providerPeerIdOffset = builder.createString(options.providerPeerId);
  const signatureOffset = LPF.createSignatureVector(builder, options.signature);
  const signingPubkeyOffset = LPF.createSigningPubkeyVector(builder, options.requesterSigningPublicKey);
  const root = LPF.createLPF(
    builder,
    licensingProofMessageType.ProofRequest,
    reqIdOffset,
    moduleIdOffset,
    moduleVersionOffset,
    requesterPeerIdOffset,
    requesterXpubOffset,
    requesterDomainOffset,
    BigInt(options.requestedTimeoutMs),
    requesterEphemeralPubkeyOffset,
    challengeNonceOffset,
    BigInt(options.challengeExpiresAtMs),
    providerPeerIdOffset,
    signatureOffset,
    signingPubkeyOffset,
    BigInt(options.timestampMs),
    0,
    0,
  );
  LPF.finishLPFBuffer(builder, root);
  return builder.asUint8Array();
}

function decodeChallengeResponse(bytes: Uint8Array): GrantChallengePayload {
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!LCH.bufferHasIdentifier(bb)) {
    throw new ModuleDeliveryProtocolError('invalid_response', 'invalid licensing challenge identifier');
  }

  const message = LCH.getRootAsLCH(bb);
  const requestId = message.REQUEST_ID();
  const moduleId = message.MODULE_ID();
  if (!requestId || !moduleId) {
    throw new ModuleDeliveryProtocolError('invalid_response', 'challenge response is missing required identifiers');
  }

  if (message.MESSAGE_TYPE() === licensingChallengeMessageType.Error) {
    throw new ModuleDeliveryProtocolError(
      normalizeProtocolCode(message.ERROR_CODE(), 'challenge_rejected'),
      message.ERROR_MESSAGE() || 'licensing challenge rejected',
    );
  }
  if (
    message.MESSAGE_TYPE() !== licensingChallengeMessageType.Response ||
    message.ROLE() !== licensingChallengeRole.Provider
  ) {
    throw new ModuleDeliveryProtocolError('unexpected_response', 'expected licensing challenge response');
  }

  const challengeNonce = cloneOptionalBytes(message.challengeNonceArray());
  if (challengeNonce.length === 0) {
    throw new ModuleDeliveryProtocolError('invalid_response', 'challenge response is missing the challenge nonce');
  }

  return {
    reqId: requestId,
    moduleId,
    moduleVersion: trimOptional(message.MODULE_VERSION()),
    requestedDomain: trimOptional(message.REQUESTED_DOMAIN()),
    requestedTimeoutMs: numberFromUint64(message.REQUESTED_TIMEOUT_MS(), 'challenge.REQUESTED_TIMEOUT_MS'),
    requestedAtMs: numberFromUint64(message.REQUESTED_AT(), 'challenge.REQUESTED_AT'),
    challengeNonce,
    expiresAtMs: numberFromUint64(message.EXPIRES_AT(), 'challenge.EXPIRES_AT'),
    providerPeerId: trimOptional(message.PROVIDER_PEER_ID()) || '',
    rawBytes: cloneBytes(bytes),
  };
}

function decodeGrantResponse(bytes: Uint8Array): GrantResponsePayload {
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!LGR.bufferHasIdentifier(bb)) {
    throw new ModuleDeliveryProtocolError('invalid_response', 'invalid licensing grant identifier');
  }

  const grant = LGR.getRootAsLGR(bb);
  const requestId = grant.REQUEST_ID();
  const moduleId = grant.MODULE_ID();
  if (!requestId || !moduleId) {
    throw new ModuleDeliveryProtocolError('invalid_response', 'grant response is missing required identifiers');
  }

  if (grant.MESSAGE_TYPE() === licensingGrantMessageType.Denied) {
    throw new ModuleDeliveryProtocolError(
      normalizeProtocolCode(grant.GRANT_STATUS(), 'grant_denied'),
      grant.DENIAL_REASON() || 'grant request denied',
    );
  }
  if (grant.MESSAGE_TYPE() !== licensingGrantMessageType.Granted) {
    throw new ModuleDeliveryProtocolError('unexpected_response', 'expected licensing grant response');
  }

  const moduleDescriptor = grant.MODULE_DESCRIPTOR();
  const wrappedContentKey = grant.WRAPPED_CONTENT_KEY();
  if (!moduleDescriptor || !wrappedContentKey) {
    throw new ModuleDeliveryProtocolError(
      'invalid_grant',
      'grant response is missing the module descriptor or wrapped content key',
    );
  }

  const cid = moduleDescriptor.WASM_CID();
  if (!cid) {
    throw new ModuleDeliveryProtocolError('invalid_grant', 'grant response is missing the published CID');
  }

  const contentHash = moduleDescriptor.ENCRYPTED()
    ? cloneOptionalBytes(moduleDescriptor.encryptedWasmHashArray() ?? moduleDescriptor.wasmHashArray())
    : cloneOptionalBytes(moduleDescriptor.wasmHashArray());
  const sizeBytes = moduleDescriptor.ENCRYPTED() && moduleDescriptor.ENCRYPTED_WASM_SIZE() > 0n
    ? numberFromUint64(moduleDescriptor.ENCRYPTED_WASM_SIZE(), 'moduleDescriptor.ENCRYPTED_WASM_SIZE')
    : numberFromUint64(moduleDescriptor.WASM_SIZE(), 'moduleDescriptor.WASM_SIZE');
  const allowedDomains = readAllowedDomains(moduleDescriptor);

  const requesterEphemeralPublicKey = cloneOptionalBytes(wrappedContentKey.requesterEphemeralPubkeyArray());
  const providerEphemeralPublicKey = cloneOptionalBytes(wrappedContentKey.providerEphemeralPubkeyArray());
  const hkdfSalt = cloneOptionalBytes(wrappedContentKey.hkdfSaltArray());
  const iv = cloneOptionalBytes(wrappedContentKey.ivArray());
  const ciphertext = cloneOptionalBytes(wrappedContentKey.ciphertextArray());
  const tag = cloneOptionalBytes(wrappedContentKey.tagArray());
  if (
    requesterEphemeralPublicKey.length === 0 ||
    providerEphemeralPublicKey.length === 0 ||
    hkdfSalt.length === 0 ||
    iv.length === 0 ||
    ciphertext.length === 0 ||
    tag.length === 0
  ) {
    throw new ModuleDeliveryProtocolError('invalid_grant', 'wrapped content key is incomplete');
  }

  return {
    reqId: requestId,
    moduleId,
    moduleVersion: trimOptional(grant.MODULE_VERSION()),
    requestedDomain: trimOptional(grant.REQUESTED_DOMAIN()),
    requestedTimeoutMs: numberFromUint64(grant.REQUESTED_TIMEOUT_MS(), 'grant.REQUESTED_TIMEOUT_MS'),
    grantedDomain: normalizeRequiredString(grant.GRANTED_DOMAIN() || '', 'grant.GRANTED_DOMAIN'),
    grantedTimeoutMs: numberFromUint64(grant.GRANTED_TIMEOUT_MS(), 'grant.GRANTED_TIMEOUT_MS'),
    expiresAtMs: numberFromUint64(grant.EXPIRES_AT(), 'grant.EXPIRES_AT'),
    requiredScope: trimOptional(grant.REQUIRED_SCOPE()),
    grantStatus: trimOptional(grant.GRANT_STATUS()),
    capabilityToken: cloneOptionalBytes(grant.capabilityTokenArray()),
    grantVerifierPublicKey: cloneOptionalBytes(grant.grantVerifierPubkeyArray()),
    providerSignature: cloneOptionalBytes(grant.providerSignatureArray()),
    bundleDescriptor: {
      cid,
      contentHash,
      sizeBytes,
      moduleId: normalizeRequiredString(moduleDescriptor.PLUGIN_ID() || '', 'moduleDescriptor.PLUGIN_ID'),
      moduleVersion: trimOptional(moduleDescriptor.VERSION()),
      keyId: trimOptional(moduleDescriptor.KEY_ID()),
      requiredScope: trimOptional(moduleDescriptor.REQUIRED_SCOPE()),
      allowedDomains,
      encrypted: Boolean(moduleDescriptor.ENCRYPTED()),
    },
    wrappedContentKey: {
      wrappingAlgorithm: wrappedKeyAlgorithmName(wrappedContentKey.ALGORITHM()),
      contentKeyId: trimOptional(wrappedContentKey.CONTENT_KEY_ID()),
      recipientKeyId: trimOptional(wrappedContentKey.RECIPIENT_KEY_ID()),
      requesterEphemeralPublicKey,
      providerEphemeralPublicKey,
      hkdfSalt,
      iv,
      ciphertext,
      tag,
      expiresAtMs: numberFromUint64(wrappedContentKey.EXPIRES_AT(), 'wrappedContentKey.EXPIRES_AT'),
      recipientPublicKey: requesterEphemeralPublicKey,
      ephemeralPublicKey: providerEphemeralPublicKey,
      nonce: iv,
    },
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

function numberFromUint64(value: bigint, name: string): number {
  if (value > BigInt(Number.MAX_SAFE_INTEGER)) {
    throw new ModuleDeliveryProtocolError('invalid_response', `${name} exceeds JavaScript safe integer range`);
  }
  return Number(value);
}

function normalizeProtocolCode(value: string | null | undefined, fallback: string): string {
  const normalized = String(value || '').trim();
  return normalized || fallback;
}

function wrappedKeyAlgorithmName(value: licensingWrappedKeyAlgorithm): string {
  switch (value) {
    case licensingWrappedKeyAlgorithm.X25519_HKDF_SHA256_AES_256_GCM:
      return 'X25519_HKDF_SHA256_AES_256_GCM';
    default:
      return `UNKNOWN_${value}`;
  }
}

function readAllowedDomains(descriptor: PLG): string[] {
  const allowedDomains: string[] = [];
  for (let index = 0; index < descriptor.allowedDomainsLength(); index += 1) {
    const domain = descriptor.ALLOWED_DOMAINS(index);
    if (domain) {
      allowedDomains.push(domain);
    }
  }
  return allowedDomains;
}
