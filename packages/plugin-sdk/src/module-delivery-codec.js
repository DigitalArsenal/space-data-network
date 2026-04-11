import * as flatbuffers from "flatbuffers";
import { BundleDescriptor } from "./generated/space-data-network/module-delivery/v1/bundle-descriptor.js";
import { ErrorResponse } from "./generated/space-data-network/module-delivery/v1/error-response.js";
import { GrantChallenge } from "./generated/space-data-network/module-delivery/v1/grant-challenge.js";
import { GrantProof } from "./generated/space-data-network/module-delivery/v1/grant-proof.js";
import { GrantRequest } from "./generated/space-data-network/module-delivery/v1/grant-request.js";
import { GrantResponse } from "./generated/space-data-network/module-delivery/v1/grant-response.js";
import { ModuleDeliveryMessage } from "./generated/space-data-network/module-delivery/v1/module-delivery-message.js";
import { ModuleDeliveryMessageType } from "./generated/space-data-network/module-delivery/v1/module-delivery-message-type.js";
import { WrappedContentKey } from "./generated/space-data-network/module-delivery/v1/wrapped-content-key.js";

const MESSAGE_TYPE_NAME_TO_ENUM = Object.freeze({
  grant_request: ModuleDeliveryMessageType.GRANT_REQUEST,
  grant_challenge: ModuleDeliveryMessageType.GRANT_CHALLENGE,
  grant_proof: ModuleDeliveryMessageType.GRANT_PROOF,
  grant_response: ModuleDeliveryMessageType.GRANT_RESPONSE,
  error_response: ModuleDeliveryMessageType.ERROR_RESPONSE,
});

const MESSAGE_TYPE_ENUM_TO_NAME = new Map(
  Object.entries(MESSAGE_TYPE_NAME_TO_ENUM).map(([name, value]) => [value, name]),
);

function asUint8Array(value) {
  if (value instanceof Uint8Array) {
    return value;
  }
  if (value instanceof ArrayBuffer) {
    return new Uint8Array(value);
  }
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  }
  return new Uint8Array(0);
}

function cloneBytes(value) {
  const bytes = asUint8Array(value);
  return bytes.length > 0 ? bytes.slice() : new Uint8Array(0);
}

function toBigInt(value, defaultValue = 0n) {
  if (typeof value === "bigint") {
    return value;
  }
  if (typeof value === "number" && Number.isFinite(value)) {
    return BigInt(Math.trunc(value));
  }
  if (typeof value === "string" && value.trim() !== "") {
    return BigInt(value);
  }
  return defaultValue;
}

function toNumber(value) {
  return Number(value || 0n);
}

function requireString(name, value) {
  const normalized = String(value || "").trim();
  if (!normalized) {
    throw new Error(`${name} is required`);
  }
  return normalized;
}

function requireBytes(name, value, minLength = 1) {
  const bytes = asUint8Array(value);
  if (bytes.length < minLength) {
    throw new Error(`${name} must be at least ${minLength} bytes`);
  }
  return bytes;
}

function optionalStringOffset(builder, value) {
  const normalized = String(value || "").trim();
  return normalized ? builder.createString(normalized) : 0;
}

function requiredStringOffset(builder, name, value) {
  return builder.createString(requireString(name, value));
}

function optionalVectorOffset(builder, tableType, creatorName, value) {
  const bytes = asUint8Array(value);
  return bytes.length > 0 ? tableType[creatorName](builder, bytes) : 0;
}

function requiredVectorOffset(builder, tableType, creatorName, name, value, minLength = 1) {
  return tableType[creatorName](builder, requireBytes(name, value, minLength));
}

function createBundleDescriptorOffset(builder, payload = {}) {
  const cidOffset = requiredStringOffset(builder, "contentCid", payload.contentCid || payload.cid);
  const contentHashOffset = requiredVectorOffset(
    builder,
    BundleDescriptor,
    "createContentHashVector",
    "contentHash",
    payload.contentHash,
  );
  const moduleIdOffset = requiredStringOffset(builder, "moduleId", payload.moduleId);
  const moduleVersionOffset = optionalStringOffset(builder, payload.moduleVersion);
  const runtimeOffset = optionalStringOffset(builder, payload.runtime);
  const abiOffset = optionalStringOffset(builder, payload.abi);
  const entrypointOffset = optionalStringOffset(builder, payload.entrypoint);
  const publicationCidOffset = optionalStringOffset(
    builder,
    payload.publicationCid,
  );
  const contentCodecOffset = optionalStringOffset(builder, payload.contentCodec);
  const encryptionCodecOffset = optionalStringOffset(
    builder,
    payload.encryptionCodec,
  );

  BundleDescriptor.startBundleDescriptor(builder);
  BundleDescriptor.addSchemaVersion(builder, Number(payload.schemaVersion || 1));
  BundleDescriptor.addCid(builder, cidOffset);
  BundleDescriptor.addContentHash(builder, contentHashOffset);
  BundleDescriptor.addSizeBytes(builder, toBigInt(payload.sizeBytes));
  BundleDescriptor.addModuleId(builder, moduleIdOffset);
  if (moduleVersionOffset !== 0) {
    BundleDescriptor.addModuleVersion(builder, moduleVersionOffset);
  }
  if (runtimeOffset !== 0) {
    BundleDescriptor.addRuntime(builder, runtimeOffset);
  }
  if (abiOffset !== 0) {
    BundleDescriptor.addAbi(builder, abiOffset);
  }
  if (entrypointOffset !== 0) {
    BundleDescriptor.addEntrypoint(builder, entrypointOffset);
  }
  if (publicationCidOffset !== 0) {
    BundleDescriptor.addPublicationCid(builder, publicationCidOffset);
  }
  if (contentCodecOffset !== 0) {
    BundleDescriptor.addContentCodec(builder, contentCodecOffset);
  }
  if (encryptionCodecOffset !== 0) {
    BundleDescriptor.addEncryptionCodec(builder, encryptionCodecOffset);
  }
  return BundleDescriptor.endBundleDescriptor(builder);
}

function createWrappedContentKeyOffset(builder, payload = {}) {
  const wrappingAlgorithmOffset = requiredStringOffset(
    builder,
    "wrappingAlgorithm",
    payload.wrappingAlgorithm,
  );
  const recipientKeyIdOffset = optionalStringOffset(builder, payload.recipientKeyId);
  const recipientPublicKeyOffset = optionalVectorOffset(
    builder,
    WrappedContentKey,
    "createRecipientPublicKeyVector",
    payload.recipientPublicKey,
  );
  const ephemeralPublicKeyOffset = requiredVectorOffset(
    builder,
    WrappedContentKey,
    "createEphemeralPublicKeyVector",
    "ephemeralPublicKey",
    payload.ephemeralPublicKey,
  );
  const nonceOffset = requiredVectorOffset(
    builder,
    WrappedContentKey,
    "createNonceVector",
    "nonce",
    payload.nonce,
  );
  const ciphertextOffset = requiredVectorOffset(
    builder,
    WrappedContentKey,
    "createCiphertextVector",
    "ciphertext",
    payload.ciphertext,
  );
  const tagOffset = optionalVectorOffset(
    builder,
    WrappedContentKey,
    "createTagVector",
    payload.tag,
  );

  WrappedContentKey.startWrappedContentKey(builder);
  WrappedContentKey.addSchemaVersion(builder, Number(payload.schemaVersion || 1));
  WrappedContentKey.addWrappingAlgorithm(builder, wrappingAlgorithmOffset);
  if (recipientKeyIdOffset !== 0) {
    WrappedContentKey.addRecipientKeyId(builder, recipientKeyIdOffset);
  }
  if (recipientPublicKeyOffset !== 0) {
    WrappedContentKey.addRecipientPublicKey(builder, recipientPublicKeyOffset);
  }
  WrappedContentKey.addEphemeralPublicKey(builder, ephemeralPublicKeyOffset);
  WrappedContentKey.addNonce(builder, nonceOffset);
  WrappedContentKey.addCiphertext(builder, ciphertextOffset);
  if (tagOffset !== 0) {
    WrappedContentKey.addTag(builder, tagOffset);
  }
  return WrappedContentKey.endWrappedContentKey(builder);
}

function createGrantRequestOffset(builder, payload = {}) {
  const reqIdOffset = requiredStringOffset(builder, "reqId", payload.reqId);
  const moduleIdOffset = requiredStringOffset(builder, "moduleId", payload.moduleId);
  const moduleVersionOffset = optionalStringOffset(builder, payload.moduleVersion);
  const moduleVariantOffset = optionalStringOffset(builder, payload.moduleVariant);
  const requesterPeerIdOffset = optionalStringOffset(builder, payload.requesterPeerId);
  const requesterXpubOffset = optionalStringOffset(builder, payload.requesterXpub);
  const requesterSigningPublicKeyOffset = requiredVectorOffset(
    builder,
    GrantRequest,
    "createRequesterSigningPublicKeyVector",
    "requesterSigningPublicKey",
    payload.requesterSigningPublicKey,
  );
  const requesterEncryptionPublicKeyOffset = requiredVectorOffset(
    builder,
    GrantRequest,
    "createRequesterEncryptionPublicKeyVector",
    "requesterEncryptionPublicKey",
    payload.requesterEncryptionPublicKey,
  );

  GrantRequest.startGrantRequest(builder);
  GrantRequest.addSchemaVersion(builder, Number(payload.schemaVersion || 1));
  GrantRequest.addReqId(builder, reqIdOffset);
  GrantRequest.addModuleId(builder, moduleIdOffset);
  if (moduleVersionOffset !== 0) {
    GrantRequest.addModuleVersion(builder, moduleVersionOffset);
  }
  if (moduleVariantOffset !== 0) {
    GrantRequest.addModuleVariant(builder, moduleVariantOffset);
  }
  if (requesterPeerIdOffset !== 0) {
    GrantRequest.addRequesterPeerId(builder, requesterPeerIdOffset);
  }
  if (requesterXpubOffset !== 0) {
    GrantRequest.addRequesterXpub(builder, requesterXpubOffset);
  }
  GrantRequest.addRequesterSigningPublicKey(builder, requesterSigningPublicKeyOffset);
  GrantRequest.addRequesterEncryptionPublicKey(
    builder,
    requesterEncryptionPublicKeyOffset,
  );
  GrantRequest.addRequestedAtMs(builder, toBigInt(payload.requestedAtMs));
  return GrantRequest.endGrantRequest(builder);
}

function createGrantChallengeOffset(builder, payload = {}) {
  const reqIdOffset = requiredStringOffset(builder, "reqId", payload.reqId);
  const challengeOffset = requiredVectorOffset(
    builder,
    GrantChallenge,
    "createChallengeVector",
    "challenge",
    payload.challenge,
  );
  const providerPeerIdOffset = optionalStringOffset(builder, payload.providerPeerId);
  const providerPublicKeyOffset = requiredVectorOffset(
    builder,
    GrantChallenge,
    "createProviderPublicKeyVector",
    "providerPublicKey",
    payload.providerPublicKey,
  );

  GrantChallenge.startGrantChallenge(builder);
  GrantChallenge.addSchemaVersion(builder, Number(payload.schemaVersion || 1));
  GrantChallenge.addReqId(builder, reqIdOffset);
  GrantChallenge.addChallenge(builder, challengeOffset);
  GrantChallenge.addExpiresAtMs(builder, toBigInt(payload.expiresAtMs));
  if (providerPeerIdOffset !== 0) {
    GrantChallenge.addProviderPeerId(builder, providerPeerIdOffset);
  }
  GrantChallenge.addProviderPublicKey(builder, providerPublicKeyOffset);
  return GrantChallenge.endGrantChallenge(builder);
}

function createGrantProofOffset(builder, payload = {}) {
  const reqIdOffset = requiredStringOffset(builder, "reqId", payload.reqId);
  const moduleIdOffset = optionalStringOffset(builder, payload.moduleId);
  const moduleVersionOffset = optionalStringOffset(builder, payload.moduleVersion);
  const requesterPeerIdOffset = optionalStringOffset(builder, payload.requesterPeerId);
  const requesterSigningPublicKeyOffset = requiredVectorOffset(
    builder,
    GrantProof,
    "createRequesterSigningPublicKeyVector",
    "requesterSigningPublicKey",
    payload.requesterSigningPublicKey,
  );
  const requesterEncryptionPublicKeyOffset = requiredVectorOffset(
    builder,
    GrantProof,
    "createRequesterEncryptionPublicKeyVector",
    "requesterEncryptionPublicKey",
    payload.requesterEncryptionPublicKey,
  );
  const challengeOffset = requiredVectorOffset(
    builder,
    GrantProof,
    "createChallengeVector",
    "challenge",
    payload.challenge,
  );
  const signatureOffset = requiredVectorOffset(
    builder,
    GrantProof,
    "createSignatureVector",
    "signature",
    payload.signature,
  );

  GrantProof.startGrantProof(builder);
  GrantProof.addSchemaVersion(builder, Number(payload.schemaVersion || 1));
  GrantProof.addReqId(builder, reqIdOffset);
  if (moduleIdOffset !== 0) {
    GrantProof.addModuleId(builder, moduleIdOffset);
  }
  if (moduleVersionOffset !== 0) {
    GrantProof.addModuleVersion(builder, moduleVersionOffset);
  }
  if (requesterPeerIdOffset !== 0) {
    GrantProof.addRequesterPeerId(builder, requesterPeerIdOffset);
  }
  GrantProof.addRequesterSigningPublicKey(builder, requesterSigningPublicKeyOffset);
  GrantProof.addRequesterEncryptionPublicKey(
    builder,
    requesterEncryptionPublicKeyOffset,
  );
  GrantProof.addChallenge(builder, challengeOffset);
  GrantProof.addSignature(builder, signatureOffset);
  GrantProof.addProvedAtMs(builder, toBigInt(payload.provedAtMs));
  return GrantProof.endGrantProof(builder);
}

function normalizeBundleDescriptorPayload(payload) {
  if (payload?.bundleDescriptorBytes) {
    return decodeBundleDescriptor(payload.bundleDescriptorBytes);
  }
  if (payload?.bundleDescriptor) {
    return payload.bundleDescriptor;
  }
  return payload;
}

function normalizeWrappedContentKeyPayload(payload) {
  if (payload?.wrappedContentKeyBytes) {
    return decodeWrappedContentKey(payload.wrappedContentKeyBytes);
  }
  if (payload?.wrappedContentKey) {
    return payload.wrappedContentKey;
  }
  return payload;
}

function createGrantResponseOffset(builder, payload = {}) {
  const reqIdOffset = requiredStringOffset(builder, "reqId", payload.reqId);
  const entitlementStatusOffset = optionalStringOffset(
    builder,
    payload.entitlementStatus,
  );
  const capabilityTokenOffset = optionalStringOffset(builder, payload.capabilityToken);
  const grantSignatureOffset = optionalVectorOffset(
    builder,
    GrantResponse,
    "createGrantSignatureVector",
    payload.grantSignature,
  );
  const bundleDescriptorOffset = createBundleDescriptorOffset(
    builder,
    normalizeBundleDescriptorPayload(payload.bundleDescriptor || payload),
  );
  const wrappedContentKeyOffset = createWrappedContentKeyOffset(
    builder,
    normalizeWrappedContentKeyPayload(payload.wrappedContentKey || payload),
  );

  GrantResponse.startGrantResponse(builder);
  GrantResponse.addSchemaVersion(builder, Number(payload.schemaVersion || 1));
  GrantResponse.addReqId(builder, reqIdOffset);
  if (entitlementStatusOffset !== 0) {
    GrantResponse.addEntitlementStatus(builder, entitlementStatusOffset);
  }
  if (capabilityTokenOffset !== 0) {
    GrantResponse.addCapabilityToken(builder, capabilityTokenOffset);
  }
  GrantResponse.addExpiresAtMs(builder, toBigInt(payload.expiresAtMs));
  if (grantSignatureOffset !== 0) {
    GrantResponse.addGrantSignature(builder, grantSignatureOffset);
  }
  GrantResponse.addBundleDescriptor(builder, bundleDescriptorOffset);
  GrantResponse.addWrappedContentKey(builder, wrappedContentKeyOffset);
  return GrantResponse.endGrantResponse(builder);
}

function createErrorResponseOffset(builder, payload = {}) {
  const reqIdOffset = optionalStringOffset(builder, payload.reqId);
  const codeOffset = requiredStringOffset(builder, "code", payload.code);
  const messageOffset = requiredStringOffset(builder, "message", payload.message);

  ErrorResponse.startErrorResponse(builder);
  ErrorResponse.addSchemaVersion(builder, Number(payload.schemaVersion || 1));
  if (reqIdOffset !== 0) {
    ErrorResponse.addReqId(builder, reqIdOffset);
  }
  ErrorResponse.addCode(builder, codeOffset);
  ErrorResponse.addMessage(builder, messageOffset);
  ErrorResponse.addRetryable(builder, Boolean(payload.retryable));
  return ErrorResponse.endErrorResponse(builder);
}

function finishBuffer(builder, finishMethod, offset) {
  finishMethod.call(undefined, builder, offset);
  return builder.asUint8Array();
}

function decodeBundleDescriptorFromTable(bundle) {
  return {
    schemaVersion: bundle.schemaVersion(),
    cid: bundle.cid() || "",
    contentCid: bundle.cid() || "",
    contentHash: cloneBytes(bundle.contentHashArray()),
    sizeBytes: toNumber(bundle.sizeBytes()),
    moduleId: bundle.moduleId() || "",
    moduleVersion: bundle.moduleVersion() || "",
    runtime: bundle.runtime() || "",
    abi: bundle.abi() || "",
    entrypoint: bundle.entrypoint() || "",
    publicationCid: bundle.publicationCid() || "",
    contentCodec: bundle.contentCodec() || "",
    encryptionCodec: bundle.encryptionCodec() || "",
  };
}

function decodeWrappedContentKeyFromTable(key) {
  return {
    schemaVersion: key.schemaVersion(),
    wrappingAlgorithm: key.wrappingAlgorithm() || "",
    recipientKeyId: key.recipientKeyId() || "",
    recipientPublicKey: cloneBytes(key.recipientPublicKeyArray()),
    ephemeralPublicKey: cloneBytes(key.ephemeralPublicKeyArray()),
    nonce: cloneBytes(key.nonceArray()),
    ciphertext: cloneBytes(key.ciphertextArray()),
    tag: cloneBytes(key.tagArray()),
  };
}

function decodeGrantRequestFromTable(request) {
  return {
    schemaVersion: request.schemaVersion(),
    reqId: request.reqId() || "",
    moduleId: request.moduleId() || "",
    moduleVersion: request.moduleVersion() || "",
    moduleVariant: request.moduleVariant() || "",
    requesterPeerId: request.requesterPeerId() || "",
    requesterXpub: request.requesterXpub() || "",
    requesterSigningPublicKey: cloneBytes(request.requesterSigningPublicKeyArray()),
    requesterEncryptionPublicKey: cloneBytes(
      request.requesterEncryptionPublicKeyArray(),
    ),
    requestedAtMs: toNumber(request.requestedAtMs()),
  };
}

function decodeGrantChallengeFromTable(challenge) {
  return {
    schemaVersion: challenge.schemaVersion(),
    reqId: challenge.reqId() || "",
    challenge: cloneBytes(challenge.challengeArray()),
    expiresAtMs: toNumber(challenge.expiresAtMs()),
    providerPeerId: challenge.providerPeerId() || "",
    providerPublicKey: cloneBytes(challenge.providerPublicKeyArray()),
  };
}

function decodeGrantProofFromTable(proof) {
  return {
    schemaVersion: proof.schemaVersion(),
    reqId: proof.reqId() || "",
    moduleId: proof.moduleId() || "",
    moduleVersion: proof.moduleVersion() || "",
    requesterPeerId: proof.requesterPeerId() || "",
    requesterSigningPublicKey: cloneBytes(proof.requesterSigningPublicKeyArray()),
    requesterEncryptionPublicKey: cloneBytes(
      proof.requesterEncryptionPublicKeyArray(),
    ),
    challenge: cloneBytes(proof.challengeArray()),
    signature: cloneBytes(proof.signatureArray()),
    provedAtMs: toNumber(proof.provedAtMs()),
  };
}

function decodeGrantResponseFromTable(response) {
  const bundleDescriptor = decodeBundleDescriptorFromTable(
    response.bundleDescriptor(new BundleDescriptor()),
  );
  const wrappedContentKey = decodeWrappedContentKeyFromTable(
    response.wrappedContentKey(new WrappedContentKey()),
  );

  return {
    schemaVersion: response.schemaVersion(),
    reqId: response.reqId() || "",
    entitlementStatus: response.entitlementStatus() || "",
    capabilityToken: response.capabilityToken() || "",
    expiresAtMs: toNumber(response.expiresAtMs()),
    grantSignature: cloneBytes(response.grantSignatureArray()),
    bundleDescriptor,
    bundleDescriptorBytes: encodeBundleDescriptor(bundleDescriptor),
    wrappedContentKey,
    wrappedContentKeyBytes: encodeWrappedContentKey(wrappedContentKey),
  };
}

function decodeErrorResponseFromTable(response) {
  return {
    schemaVersion: response.schemaVersion(),
    reqId: response.reqId() || "",
    code: response.code() || "",
    message: response.message() || "",
    retryable: response.retryable(),
  };
}

function normalizeMessageType(type) {
  const normalized = String(type || "")
    .trim()
    .toLowerCase();
  const value = MESSAGE_TYPE_NAME_TO_ENUM[normalized];
  if (value === undefined) {
    throw new Error(`unknown module-delivery message type: ${type}`);
  }
  return {
    name: normalized,
    value,
  };
}

export function encodeBundleDescriptor(payload = {}) {
  const builder = new flatbuffers.Builder(512);
  const root = createBundleDescriptorOffset(builder, payload);
  return finishBuffer(builder, BundleDescriptor.finishBundleDescriptorBuffer, root);
}

export function decodeBundleDescriptor(messageBytes) {
  const bb = new flatbuffers.ByteBuffer(asUint8Array(messageBytes));
  if (!BundleDescriptor.bufferHasIdentifier(bb)) {
    throw new Error("invalid bundle descriptor identifier");
  }
  return decodeBundleDescriptorFromTable(BundleDescriptor.getRootAsBundleDescriptor(bb));
}

export function encodeWrappedContentKey(payload = {}) {
  const builder = new flatbuffers.Builder(512);
  const root = createWrappedContentKeyOffset(builder, payload);
  return finishBuffer(builder, WrappedContentKey.finishWrappedContentKeyBuffer, root);
}

export function decodeWrappedContentKey(messageBytes) {
  const bb = new flatbuffers.ByteBuffer(asUint8Array(messageBytes));
  if (!WrappedContentKey.bufferHasIdentifier(bb)) {
    throw new Error("invalid wrapped content key identifier");
  }
  return decodeWrappedContentKeyFromTable(WrappedContentKey.getRootAsWrappedContentKey(bb));
}

export function encodeGrantRequest(payload = {}) {
  const builder = new flatbuffers.Builder(512);
  const root = createGrantRequestOffset(builder, payload);
  return finishBuffer(builder, GrantRequest.finishGrantRequestBuffer, root);
}

export function decodeGrantRequest(messageBytes) {
  const bb = new flatbuffers.ByteBuffer(asUint8Array(messageBytes));
  if (!GrantRequest.bufferHasIdentifier(bb)) {
    throw new Error("invalid grant request identifier");
  }
  return decodeGrantRequestFromTable(GrantRequest.getRootAsGrantRequest(bb));
}

export function encodeGrantChallenge(payload = {}) {
  const builder = new flatbuffers.Builder(384);
  const root = createGrantChallengeOffset(builder, payload);
  return finishBuffer(builder, GrantChallenge.finishGrantChallengeBuffer, root);
}

export function decodeGrantChallenge(messageBytes) {
  const bb = new flatbuffers.ByteBuffer(asUint8Array(messageBytes));
  if (!GrantChallenge.bufferHasIdentifier(bb)) {
    throw new Error("invalid grant challenge identifier");
  }
  return decodeGrantChallengeFromTable(GrantChallenge.getRootAsGrantChallenge(bb));
}

export function encodeGrantProof(payload = {}) {
  const builder = new flatbuffers.Builder(512);
  const root = createGrantProofOffset(builder, payload);
  return finishBuffer(builder, GrantProof.finishGrantProofBuffer, root);
}

export function decodeGrantProof(messageBytes) {
  const bb = new flatbuffers.ByteBuffer(asUint8Array(messageBytes));
  if (!GrantProof.bufferHasIdentifier(bb)) {
    throw new Error("invalid grant proof identifier");
  }
  return decodeGrantProofFromTable(GrantProof.getRootAsGrantProof(bb));
}

export function encodeGrantResponse(payload = {}) {
  const builder = new flatbuffers.Builder(768);
  const root = createGrantResponseOffset(builder, payload);
  return finishBuffer(builder, GrantResponse.finishGrantResponseBuffer, root);
}

export function decodeGrantResponse(messageBytes) {
  const bb = new flatbuffers.ByteBuffer(asUint8Array(messageBytes));
  if (!GrantResponse.bufferHasIdentifier(bb)) {
    throw new Error("invalid grant response identifier");
  }
  return decodeGrantResponseFromTable(GrantResponse.getRootAsGrantResponse(bb));
}

export function encodeErrorResponse(payload = {}) {
  const builder = new flatbuffers.Builder(256);
  const root = createErrorResponseOffset(builder, payload);
  return finishBuffer(builder, ErrorResponse.finishErrorResponseBuffer, root);
}

export function decodeErrorResponse(messageBytes) {
  const bb = new flatbuffers.ByteBuffer(asUint8Array(messageBytes));
  if (!ErrorResponse.bufferHasIdentifier(bb)) {
    throw new Error("invalid error response identifier");
  }
  return decodeErrorResponseFromTable(ErrorResponse.getRootAsErrorResponse(bb));
}

export function encodeModuleDeliveryMessage(payload = {}) {
  const { name, value } = normalizeMessageType(payload.type);
  const builder = new flatbuffers.Builder(1024);

  let messageOffset = 0;
  switch (name) {
    case "grant_request":
      messageOffset = createGrantRequestOffset(builder, payload.payload || payload);
      break;
    case "grant_challenge":
      messageOffset = createGrantChallengeOffset(builder, payload.payload || payload);
      break;
    case "grant_proof":
      messageOffset = createGrantProofOffset(builder, payload.payload || payload);
      break;
    case "grant_response":
      messageOffset = createGrantResponseOffset(builder, payload.payload || payload);
      break;
    case "error_response":
      messageOffset = createErrorResponseOffset(builder, payload.payload || payload);
      break;
    default:
      throw new Error(`unknown module-delivery message type: ${payload.type}`);
  }

  ModuleDeliveryMessage.startModuleDeliveryMessage(builder);
  ModuleDeliveryMessage.addSchemaVersion(builder, Number(payload.schemaVersion || 1));
  ModuleDeliveryMessage.addMessageType(builder, value);
  switch (name) {
    case "grant_request":
      ModuleDeliveryMessage.addGrantRequest(builder, messageOffset);
      break;
    case "grant_challenge":
      ModuleDeliveryMessage.addGrantChallenge(builder, messageOffset);
      break;
    case "grant_proof":
      ModuleDeliveryMessage.addGrantProof(builder, messageOffset);
      break;
    case "grant_response":
      ModuleDeliveryMessage.addGrantResponse(builder, messageOffset);
      break;
    case "error_response":
      ModuleDeliveryMessage.addErrorResponse(builder, messageOffset);
      break;
    default:
      break;
  }

  const root = ModuleDeliveryMessage.endModuleDeliveryMessage(builder);
  return finishBuffer(
    builder,
    ModuleDeliveryMessage.finishModuleDeliveryMessageBuffer,
    root,
  );
}

export function decodeModuleDeliveryMessage(messageBytes) {
  const bb = new flatbuffers.ByteBuffer(asUint8Array(messageBytes));
  if (!ModuleDeliveryMessage.bufferHasIdentifier(bb)) {
    throw new Error("invalid module delivery message identifier");
  }

  const message = ModuleDeliveryMessage.getRootAsModuleDeliveryMessage(bb);
  const typeValue = message.messageType();
  const type = MESSAGE_TYPE_ENUM_TO_NAME.get(typeValue);
  if (!type) {
    throw new Error(`unsupported module-delivery message type: ${typeValue}`);
  }

  let payload;
  switch (type) {
    case "grant_request":
      payload = decodeGrantRequestFromTable(message.grantRequest(new GrantRequest()));
      break;
    case "grant_challenge":
      payload = decodeGrantChallengeFromTable(
        message.grantChallenge(new GrantChallenge()),
      );
      break;
    case "grant_proof":
      payload = decodeGrantProofFromTable(message.grantProof(new GrantProof()));
      break;
    case "grant_response":
      payload = decodeGrantResponseFromTable(
        message.grantResponse(new GrantResponse()),
      );
      break;
    case "error_response":
      payload = decodeErrorResponseFromTable(
        message.errorResponse(new ErrorResponse()),
      );
      break;
    default:
      throw new Error(`unsupported module-delivery message type: ${typeValue}`);
  }

  return {
    schemaVersion: message.schemaVersion(),
    type,
    payload,
  };
}

export { ModuleDeliveryMessageType };
