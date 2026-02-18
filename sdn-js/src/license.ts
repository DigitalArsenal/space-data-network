import { ed25519PublicKey, sign, deriveSecp256k1Key, derivePeerIdFromPublicKey, derivePeerIdFromXpub } from './crypto/hd-wallet';
import { buildIdentityPath } from './crypto/types';

export const LICENSE_PROTOCOL_ID = '/orbpro/license/1.0.0';

type LicenseMessageType =
  | 'challenge_request'
  | 'challenge_response'
  | 'proof_request'
  | 'grant_response'
  | 'error_response';

export interface LicenseChallengeRequest {
  type: 'challenge_request';
  req_id: string;
  xpub: string;
  peer_id: string;
  client_pubkey_hex: string;
  ts: number;
}

export interface LicenseChallengeResponse {
  type: 'challenge_response';
  req_id: string;
  challenge: string;
  expires_at: number;
  server_peer_id: string;
}

export interface LicenseProofRequest {
  type: 'proof_request';
  req_id: string;
  xpub: string;
  peer_id: string;
  challenge: string;
  signature_hex: string;
  ts: number;
}

export interface LicenseEntitlement {
  xpub: string;
  peer_id?: string;
  plan: string;
  status: string;
  stripe_customer_id?: string;
  stripe_subscription_id?: string;
  expires_at?: number;
  updated_at: number;
}

export interface LicenseGrantResponse {
  type: 'grant_response';
  req_id: string;
  entitlement: LicenseEntitlement;
  capability_token: string;
  expires_at: number;
}

export interface LicenseErrorResponse {
  type: 'error_response';
  code: string;
  message: string;
}

export type LicenseResponse = LicenseChallengeResponse | LicenseGrantResponse | LicenseErrorResponse;

export interface LicenseTransport {
  dialProtocolThroughRelay(
    relayAddr: string,
    targetPeerId: string,
    protocolId: string,
    payload: Uint8Array | string
  ): Promise<Uint8Array>;
}

export interface LicenseGrantRequestOptions {
  relayAddr: string;
  licensePeerId: string;
  xpub: string;
  signingPrivateKey: Uint8Array;
  peerId?: string;
  reqId?: string;
  now?: number;
}

export interface LicenseGrantResult {
  peerId: string;
  clientPublicKeyHex: string;
  response: LicenseGrantResponse;
}

export class LicenseProtocolError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = 'LicenseProtocolError';
    this.code = code;
  }
}

/**
 * Derive PeerID from a 64-byte BIP-39 seed using secp256k1 at m/44'/0'/0'.
 */
export async function derivePeerIdFromSeed(seed: Uint8Array, account: number = 0): Promise<string> {
  const identityKey = await deriveSecp256k1Key(seed, buildIdentityPath(account));
  return derivePeerIdFromPublicKey(identityKey.publicKey);
}

/**
 * @deprecated Use derivePeerIdFromSeed() instead. This function is kept for backward compatibility.
 */
export async function derivePeerIdFromEd25519Seed(privateKey: Uint8Array): Promise<string> {
  // Legacy: this used to derive from Ed25519 seed. Now we need a full 64-byte seed
  // for secp256k1 derivation. If given a 32-byte seed, we can't do BIP-32 derivation.
  // Fall back to requiring the caller to pass the full seed.
  if (privateKey.length === 64) {
    return derivePeerIdFromSeed(privateKey);
  }
  throw new Error('derivePeerIdFromEd25519Seed is deprecated. Use derivePeerIdFromSeed() with a 64-byte BIP-39 seed.');
}

export async function requestLicenseGrantViaRelay(
  transport: LicenseTransport,
  options: LicenseGrantRequestOptions,
): Promise<LicenseGrantResult> {
  const reqId = options.reqId?.trim() || createReqId();
  const now = options.now ?? nowUnix();
  const xpub = options.xpub.trim();
  if (!xpub) {
    throw new Error('xpub is required');
  }

  const seed = normalizeSeed(options.signingPrivateKey);
  const publicKey = await ed25519PublicKey(seed);
  const clientPublicKeyHex = toHex(publicKey);
  const peerId = options.peerId?.trim() || derivePeerIdFromXpub(xpub);

  const challengeReq: LicenseChallengeRequest = {
    type: 'challenge_request',
    req_id: reqId,
    xpub,
    peer_id: peerId,
    client_pubkey_hex: clientPublicKeyHex,
    ts: now,
  };

  const challengeRaw = await sendLicenseRequest(transport, options, challengeReq);
  if (challengeRaw.type === 'error_response') {
    throw new LicenseProtocolError(challengeRaw.code, challengeRaw.message);
  }
  if (challengeRaw.type !== 'challenge_response') {
    throw new LicenseProtocolError('unexpected_response', `Expected challenge_response, got ${challengeRaw.type}`);
  }
  if (challengeRaw.req_id !== reqId) {
    throw new LicenseProtocolError('request_mismatch', 'Challenge response request id mismatch');
  }

  const challengeBytes = decodeBase64Raw(challengeRaw.challenge);
  const signature = await sign(seed, challengeBytes);

  const proofReq: LicenseProofRequest = {
    type: 'proof_request',
    req_id: reqId,
    xpub,
    peer_id: peerId,
    challenge: challengeRaw.challenge,
    signature_hex: toHex(signature),
    ts: nowUnix(),
  };

  const grantRaw = await sendLicenseRequest(transport, options, proofReq);
  if (grantRaw.type === 'error_response') {
    throw new LicenseProtocolError(grantRaw.code, grantRaw.message);
  }
  if (grantRaw.type !== 'grant_response') {
    throw new LicenseProtocolError('unexpected_response', `Expected grant_response, got ${grantRaw.type}`);
  }
  if (grantRaw.req_id !== reqId) {
    throw new LicenseProtocolError('request_mismatch', 'Grant response request id mismatch');
  }

  return {
    peerId,
    clientPublicKeyHex,
    response: grantRaw,
  };
}

function normalizeSeed(privateKey: Uint8Array): Uint8Array {
  if (privateKey.length === 64) {
    return privateKey.slice(0, 32);
  }
  if (privateKey.length !== 32) {
    throw new Error('Invalid signing key length - expected 32-byte seed or 64-byte private key');
  }
  return privateKey;
}

async function sendLicenseRequest(
  transport: LicenseTransport,
  options: LicenseGrantRequestOptions,
  payload: { type: LicenseMessageType }
): Promise<LicenseResponse> {
  const requestBody = `${JSON.stringify(payload)}\n`;
  const bytes = await transport.dialProtocolThroughRelay(
    options.relayAddr,
    options.licensePeerId,
    LICENSE_PROTOCOL_ID,
    requestBody,
  );
  return parseLicenseResponse(bytes);
}

export function parseLicenseResponse(bytes: Uint8Array): LicenseResponse {
  const raw = new TextDecoder().decode(bytes).trim();
  if (!raw) {
    throw new LicenseProtocolError('empty_response', 'License service returned empty response');
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (error) {
    throw new LicenseProtocolError(
      'invalid_response_json',
      `License service returned invalid JSON: ${error instanceof Error ? error.message : String(error)}`,
    );
  }

  const message = parsed as Partial<LicenseResponse>;
  if (typeof message.type !== 'string') {
    throw new LicenseProtocolError('invalid_response', 'License response missing type');
  }

  if (message.type === 'error_response') {
    return {
      type: 'error_response',
      code: String(message.code ?? 'unknown_error'),
      message: String(message.message ?? 'License service error'),
    };
  }

  return message as LicenseResponse;
}

function toHex(bytes: Uint8Array): string {
  return Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}

function decodeBase64Raw(input: string): Uint8Array {
  let normalized = input.replace(/-/g, '+').replace(/_/g, '/');
  const padRemainder = normalized.length % 4;
  if (padRemainder === 1) {
    throw new LicenseProtocolError('invalid_challenge', 'Challenge has invalid base64 length');
  }
  if (padRemainder > 0) {
    normalized = normalized + '='.repeat(4 - padRemainder);
  }

  const decode = globalThis.atob;
  if (typeof decode !== 'function') {
    throw new LicenseProtocolError('missing_base64_decoder', 'atob is not available in this runtime');
  }

  const binary = decode(normalized);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    out[i] = binary.charCodeAt(i);
  }
  return out;
}

function createReqId(): string {
  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return globalThis.crypto.randomUUID();
  }

  const rnd = new Uint8Array(16);
  if (typeof globalThis.crypto?.getRandomValues === 'function') {
    globalThis.crypto.getRandomValues(rnd);
  } else {
    for (let i = 0; i < rnd.length; i++) {
      rnd[i] = Math.floor(Math.random() * 256);
    }
  }

  return toHex(rnd);
}

function nowUnix(): number {
  return Math.floor(Date.now() / 1000);
}
