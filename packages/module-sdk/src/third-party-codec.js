import * as flatbuffers from "flatbuffers";
import { ThirdPartyClientLicenseRequest } from "./generated/orbpro/thirdparty/v1/third-party-client-license-request.js";
import { ThirdPartyClientLicenseResponse } from "./generated/orbpro/thirdparty/v1/third-party-client-license-response.js";
import { ThirdPartyServerPluginRegistration } from "./generated/orbpro/thirdparty/v1/third-party-server-plugin-registration.js";
import { ThirdPartyServerPluginGrant } from "./generated/orbpro/thirdparty/v1/third-party-server-plugin-grant.js";

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

function assertMinLength(name, bytes, min) {
  if (bytes.length < min) {
    throw new Error(`${name} must be at least ${min} bytes`);
  }
}

function createStringVector(builder, values) {
  if (!Array.isArray(values) || values.length === 0) {
    return 0;
  }
  const offsets = values.map((value) => builder.createString(String(value)));
  return ThirdPartyServerPluginRegistration.createCapabilitiesVector(
    builder,
    offsets,
  );
}

function createAllowedAccountsVector(builder, values) {
  if (!Array.isArray(values) || values.length === 0) {
    return 0;
  }
  const offsets = values.map((value) => builder.createString(String(value)));
  return ThirdPartyServerPluginGrant.createAllowedAccountsVector(builder, offsets);
}

export function encodeThirdPartyClientLicenseRequest(payload = {}) {
  const pluginId = String(payload.pluginId || "").trim();
  const pluginVersion = String(payload.pluginVersion || "").trim();
  const accountIdHash = asUint8Array(payload.accountIdHash);
  const requestNonce = asUint8Array(payload.requestNonce);
  const ephemeralPublicKey = asUint8Array(payload.ephemeralPublicKey);
  const challengeToken =
    payload.challengeToken !== undefined && payload.challengeToken !== null
      ? String(payload.challengeToken)
      : "";
  const schemaVersion = Number(payload.schemaVersion || 1);

  if (!pluginId) {
    throw new Error("pluginId is required");
  }
  if (!pluginVersion) {
    throw new Error("pluginVersion is required");
  }
  assertMinLength("accountIdHash", accountIdHash, 16);
  assertMinLength("requestNonce", requestNonce, 8);
  assertMinLength("ephemeralPublicKey", ephemeralPublicKey, 16);

  const builder = new flatbuffers.Builder(512);

  const pluginIdOffset = builder.createString(pluginId);
  const pluginVersionOffset = builder.createString(pluginVersion);
  const accountIdHashOffset =
    ThirdPartyClientLicenseRequest.createAccountIdHashVector(builder, accountIdHash);
  const requestNonceOffset =
    ThirdPartyClientLicenseRequest.createRequestNonceVector(builder, requestNonce);
  const ephemeralPublicKeyOffset =
    ThirdPartyClientLicenseRequest.createEphemeralPublicKeyVector(
      builder,
      ephemeralPublicKey,
    );
  const challengeTokenOffset = challengeToken
    ? builder.createString(challengeToken)
    : 0;

  ThirdPartyClientLicenseRequest.startThirdPartyClientLicenseRequest(builder);
  ThirdPartyClientLicenseRequest.addSchemaVersion(builder, schemaVersion);
  ThirdPartyClientLicenseRequest.addPluginId(builder, pluginIdOffset);
  ThirdPartyClientLicenseRequest.addPluginVersion(builder, pluginVersionOffset);
  ThirdPartyClientLicenseRequest.addAccountIdHash(builder, accountIdHashOffset);
  ThirdPartyClientLicenseRequest.addRequestNonce(builder, requestNonceOffset);
  ThirdPartyClientLicenseRequest.addEphemeralPublicKey(builder, ephemeralPublicKeyOffset);
  if (challengeTokenOffset !== 0) {
    ThirdPartyClientLicenseRequest.addChallengeToken(builder, challengeTokenOffset);
  }
  const root = ThirdPartyClientLicenseRequest.endThirdPartyClientLicenseRequest(builder);
  ThirdPartyClientLicenseRequest.finishThirdPartyClientLicenseRequestBuffer(builder, root);
  return builder.asUint8Array();
}

export function decodeThirdPartyClientLicenseRequest(messageBytes) {
  const bytes = asUint8Array(messageBytes);
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!ThirdPartyClientLicenseRequest.bufferHasIdentifier(bb)) {
    throw new Error("invalid third-party client license request identifier");
  }

  const request = ThirdPartyClientLicenseRequest.getRootAsThirdPartyClientLicenseRequest(bb);
  return {
    schemaVersion: request.schemaVersion(),
    pluginId: request.pluginId() || "",
    pluginVersion: request.pluginVersion() || "",
    accountIdHash: request.accountIdHashArray() || new Uint8Array(0),
    requestNonce: request.requestNonceArray() || new Uint8Array(0),
    ephemeralPublicKey: request.ephemeralPublicKeyArray() || new Uint8Array(0),
    challengeToken: request.challengeToken() || "",
  };
}

export function encodeThirdPartyClientLicenseResponse(payload = {}) {
  const status = Number(payload.status || 0) >>> 0;
  const keyVersion = Number(payload.keyVersion || 0) >>> 0;
  const expiresAtMs = BigInt(payload.expiresAtMs || 0);
  const wrappedKey = asUint8Array(payload.wrappedKey);
  const challengeId = asUint8Array(payload.challengeId);

  const builder = new flatbuffers.Builder(384);
  const wrappedKeyOffset = wrappedKey.length
    ? ThirdPartyClientLicenseResponse.createWrappedKeyVector(builder, wrappedKey)
    : 0;
  const challengeIdOffset = challengeId.length
    ? ThirdPartyClientLicenseResponse.createChallengeIdVector(builder, challengeId)
    : 0;

  ThirdPartyClientLicenseResponse.startThirdPartyClientLicenseResponse(builder);
  ThirdPartyClientLicenseResponse.addStatus(builder, status);
  ThirdPartyClientLicenseResponse.addKeyVersion(builder, keyVersion);
  ThirdPartyClientLicenseResponse.addExpiresAtMs(builder, expiresAtMs);
  if (wrappedKeyOffset !== 0) {
    ThirdPartyClientLicenseResponse.addWrappedKey(builder, wrappedKeyOffset);
  }
  if (challengeIdOffset !== 0) {
    ThirdPartyClientLicenseResponse.addChallengeId(builder, challengeIdOffset);
  }

  const root = ThirdPartyClientLicenseResponse.endThirdPartyClientLicenseResponse(builder);
  ThirdPartyClientLicenseResponse.finishThirdPartyClientLicenseResponseBuffer(builder, root);
  return builder.asUint8Array();
}

export function decodeThirdPartyClientLicenseResponse(messageBytes) {
  const bytes = asUint8Array(messageBytes);
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!ThirdPartyClientLicenseResponse.bufferHasIdentifier(bb)) {
    throw new Error("invalid third-party client license response identifier");
  }

  const response =
    ThirdPartyClientLicenseResponse.getRootAsThirdPartyClientLicenseResponse(bb);

  return {
    status: response.status() >>> 0,
    keyVersion: response.keyVersion() >>> 0,
    expiresAtMs: Number(response.expiresAtMs()),
    wrappedKey: response.wrappedKeyArray() || new Uint8Array(0),
    challengeId: response.challengeIdArray() || new Uint8Array(0),
  };
}

export function encodeThirdPartyServerPluginRegistration(payload = {}) {
  const schemaVersion = Number(payload.schemaVersion || 1);
  const pluginId = String(payload.pluginId || "").trim();
  const pluginVersion = String(payload.pluginVersion || "").trim();
  const vendorId = String(payload.vendorId || "").trim();
  const signingPublicKey = asUint8Array(payload.signingPublicKey);
  const capabilities = Array.isArray(payload.capabilities)
    ? payload.capabilities.map((entry) => String(entry))
    : [];
  const manifestHash = asUint8Array(payload.manifestHash);

  if (!pluginId) {
    throw new Error("pluginId is required");
  }
  if (!pluginVersion) {
    throw new Error("pluginVersion is required");
  }
  if (!vendorId) {
    throw new Error("vendorId is required");
  }
  assertMinLength("signingPublicKey", signingPublicKey, 16);
  assertMinLength("manifestHash", manifestHash, 16);

  const builder = new flatbuffers.Builder(512);

  const pluginIdOffset = builder.createString(pluginId);
  const pluginVersionOffset = builder.createString(pluginVersion);
  const vendorIdOffset = builder.createString(vendorId);
  const signingPublicKeyOffset =
    ThirdPartyServerPluginRegistration.createSigningPublicKeyVector(
      builder,
      signingPublicKey,
    );
  const capabilitiesOffset = createStringVector(builder, capabilities);
  const manifestHashOffset =
    ThirdPartyServerPluginRegistration.createManifestHashVector(builder, manifestHash);

  ThirdPartyServerPluginRegistration.startThirdPartyServerPluginRegistration(builder);
  ThirdPartyServerPluginRegistration.addSchemaVersion(builder, schemaVersion);
  ThirdPartyServerPluginRegistration.addPluginId(builder, pluginIdOffset);
  ThirdPartyServerPluginRegistration.addPluginVersion(builder, pluginVersionOffset);
  ThirdPartyServerPluginRegistration.addVendorId(builder, vendorIdOffset);
  ThirdPartyServerPluginRegistration.addSigningPublicKey(
    builder,
    signingPublicKeyOffset,
  );
  if (capabilitiesOffset !== 0) {
    ThirdPartyServerPluginRegistration.addCapabilities(builder, capabilitiesOffset);
  }
  ThirdPartyServerPluginRegistration.addManifestHash(builder, manifestHashOffset);

  const root =
    ThirdPartyServerPluginRegistration.endThirdPartyServerPluginRegistration(builder);
  ThirdPartyServerPluginRegistration.finishThirdPartyServerPluginRegistrationBuffer(
    builder,
    root,
  );
  return builder.asUint8Array();
}

export function decodeThirdPartyServerPluginRegistration(messageBytes) {
  const bytes = asUint8Array(messageBytes);
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!ThirdPartyServerPluginRegistration.bufferHasIdentifier(bb)) {
    throw new Error("invalid third-party server plugin registration identifier");
  }

  const registration =
    ThirdPartyServerPluginRegistration.getRootAsThirdPartyServerPluginRegistration(
      bb,
    );
  const capabilitiesLength = registration.capabilitiesLength();
  const capabilities = [];
  for (let i = 0; i < capabilitiesLength; i++) {
    const value = registration.capabilities(i);
    if (value) {
      capabilities.push(value);
    }
  }

  return {
    schemaVersion: registration.schemaVersion(),
    pluginId: registration.pluginId() || "",
    pluginVersion: registration.pluginVersion() || "",
    vendorId: registration.vendorId() || "",
    signingPublicKey: registration.signingPublicKeyArray() || new Uint8Array(0),
    capabilities,
    manifestHash: registration.manifestHashArray() || new Uint8Array(0),
  };
}

export function encodeThirdPartyServerPluginGrant(payload = {}) {
  const status = Number(payload.status || 0) >>> 0;
  const grantId = String(payload.grantId || "");
  const issuedAtMs = BigInt(payload.issuedAtMs || 0);
  const expiresAtMs = BigInt(payload.expiresAtMs || 0);
  const allowedAccounts = Array.isArray(payload.allowedAccounts)
    ? payload.allowedAccounts.map((entry) => String(entry))
    : [];
  const policyHash = asUint8Array(payload.policyHash);

  const builder = new flatbuffers.Builder(512);

  const grantIdOffset = grantId ? builder.createString(grantId) : 0;
  const allowedAccountsOffset = createAllowedAccountsVector(builder, allowedAccounts);
  const policyHashOffset = policyHash.length
    ? ThirdPartyServerPluginGrant.createPolicyHashVector(builder, policyHash)
    : 0;

  ThirdPartyServerPluginGrant.startThirdPartyServerPluginGrant(builder);
  ThirdPartyServerPluginGrant.addStatus(builder, status);
  if (grantIdOffset !== 0) {
    ThirdPartyServerPluginGrant.addGrantId(builder, grantIdOffset);
  }
  ThirdPartyServerPluginGrant.addIssuedAtMs(builder, issuedAtMs);
  ThirdPartyServerPluginGrant.addExpiresAtMs(builder, expiresAtMs);
  if (allowedAccountsOffset !== 0) {
    ThirdPartyServerPluginGrant.addAllowedAccounts(builder, allowedAccountsOffset);
  }
  if (policyHashOffset !== 0) {
    ThirdPartyServerPluginGrant.addPolicyHash(builder, policyHashOffset);
  }

  const root = ThirdPartyServerPluginGrant.endThirdPartyServerPluginGrant(builder);
  ThirdPartyServerPluginGrant.finishThirdPartyServerPluginGrantBuffer(builder, root);
  return builder.asUint8Array();
}

export function decodeThirdPartyServerPluginGrant(messageBytes) {
  const bytes = asUint8Array(messageBytes);
  const bb = new flatbuffers.ByteBuffer(bytes);
  if (!ThirdPartyServerPluginGrant.bufferHasIdentifier(bb)) {
    throw new Error("invalid third-party server plugin grant identifier");
  }

  const grant = ThirdPartyServerPluginGrant.getRootAsThirdPartyServerPluginGrant(bb);
  const allowedAccountsLength = grant.allowedAccountsLength();
  const allowedAccounts = [];
  for (let i = 0; i < allowedAccountsLength; i++) {
    const value = grant.allowedAccounts(i);
    if (value) {
      allowedAccounts.push(value);
    }
  }

  return {
    status: grant.status() >>> 0,
    grantId: grant.grantId() || "",
    issuedAtMs: Number(grant.issuedAtMs()),
    expiresAtMs: Number(grant.expiresAtMs()),
    allowedAccounts,
    policyHash: grant.policyHashArray() || new Uint8Array(0),
  };
}
