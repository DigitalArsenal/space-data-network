/**
 * The $RPC sealed admin transport, browser side (Go twin: sdn-server
 * internal/sealed). A command is sealed to the node's advertised encryption key
 * and signed by the browser's session key; the node's reply is sealed to a
 * one-use reply key and signed by the node. Encrypt, then sign: $ENC is
 * AES-256-CTR with no MAC, so the signatures carry the integrity.
 *
 *   SIGNATURE                 Ed25519 over the size-prefixed RPC buffer with
 *                             both signature vectors zeroed in place
 *   CANONICAL_JSON_SIGNATURE  Ed25519 over canonicalJson(envelope)
 */

import * as flatbuffers from 'flatbuffers';
import { RPC } from 'spacedatastandards.org/lib/js/RPC/RPC.js';
import { RPCBody } from 'spacedatastandards.org/lib/js/RPC/RPCBody.js';
import { ENC } from 'spacedatastandards.org/lib/js/RPC/ENC.js';
import { EciesKeyExchange, eciesOpenField, eciesSealField, type EciesFieldHeader } from './ecies';
import { randomBytes, sha256, sign, verify, x25519PublicKey } from './crypto/hd-wallet';

export const RPC_ROUTE = '/api/rpc';
export const RPC_CONTENT_TYPE = 'application/x-sds-rpc';
export const CONTEXT_REQUEST = 'RPC/1|request';
export const CONTEXT_RESPONSE = 'RPC/1|response';
export const CIPHERTEXT_FIELD = 7;
export const NONCE_SIZE = 16;
export const SIGNATURE_TYPE = 'Ed25519';
export const REVOKE_ROUTE = '/api/auth/delegate/revoke';

const KEY_EXCHANGE_NAMES = ['X25519', 'Secp256k1', 'P256'];

export interface RpcBody {
  method?: string;
  route?: string;
  body?: Uint8Array;
  bodyFileId?: string;
  status?: number;
  signerKeyId?: Uint8Array;
  replyKey?: Uint8Array;
  requestNonce?: Uint8Array;
  requestDigest?: Uint8Array;
}

export interface RpcEnvelope {
  response: boolean;
  header: EciesFieldHeader;
  sessionId: string;
  senderKeyId: Uint8Array;
  timestampMs: number;
  nonce: Uint8Array;
  ciphertext: Uint8Array;
  signer: Uint8Array;
  raw: Uint8Array;
}

/** An Ed25519 key that signs envelopes: the browser's delegated session key. */
export interface RpcSigner {
  publicKey: Uint8Array;
  sign(message: Uint8Array): Promise<Uint8Array>;
}

function b64(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s);
}

function equal(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i];
  return diff === 0;
}

/**
 * The text CANONICAL_JSON_SIGNATURE signs, byte-identical to Go's
 * sealed.CanonicalJSON: IDL order and capitalization, signature fields
 * omitted, byte vectors as padded base64, the direction by name, no
 * whitespace, empty optional fields omitted.
 */
export function canonicalJson(env: Omit<RpcEnvelope, 'raw'>): Uint8Array {
  const encryption: Record<string, unknown> = {
    VERSION: 1,
    KEY_EXCHANGE: KEY_EXCHANGE_NAMES[env.header.keyExchange],
    SYMMETRIC: 'AES_256_CTR',
    KEY_DERIVATION: 'HKDF_SHA256',
    EPHEMERAL_PUBLIC_KEY: b64(env.header.ephemeralPublicKey),
    NONCE_START: b64(env.header.nonceStart),
  };
  if (env.header.recipientKeyId?.length) encryption.RECIPIENT_KEY_ID = b64(env.header.recipientKeyId);
  encryption.CONTEXT = env.header.context;
  const doc: Record<string, unknown> = {
    VERSION: 1,
    DIRECTION: env.response ? 'Response' : 'Request',
    ENCRYPTION: encryption,
  };
  if (env.sessionId) doc.SESSION_ID = env.sessionId;
  doc.SENDER_KEY_ID = b64(env.senderKeyId);
  doc.TIMESTAMP = env.timestampMs;
  doc.NONCE = b64(env.nonce);
  doc.CIPHERTEXT = b64(env.ciphertext);
  doc.SIGNER_PUBLIC_KEY = b64(env.signer);
  doc.SIGNATURE_TYPE = SIGNATURE_TYPE;
  return new TextEncoder().encode(JSON.stringify(doc));
}

function encodeBody(body: RpcBody): Uint8Array {
  const b = new flatbuffers.Builder(256 + (body.body?.length ?? 0));
  const str = (s?: string) => (s ? b.createString(s) : 0);
  const vec = (v?: Uint8Array) => (v && v.length ? b.createByteVector(v) : 0);
  const method = str(body.method), route = str(body.route), bytes = vec(body.body), fileId = str(body.bodyFileId);
  const signer = vec(body.signerKeyId), reply = vec(body.replyKey), reqNonce = vec(body.requestNonce), reqDigest = vec(body.requestDigest);
  RPCBody.startRPCBody(b);
  if (method) RPCBody.addMethod(b, method);
  if (route) RPCBody.addRoute(b, route);
  if (bytes) RPCBody.addBody(b, bytes);
  if (fileId) RPCBody.addBodyFileId(b, fileId);
  RPCBody.addStatus(b, body.status ?? 0);
  if (signer) RPCBody.addSignerKeyId(b, signer);
  if (reply) RPCBody.addReplyKey(b, reply);
  if (reqNonce) RPCBody.addRequestNonce(b, reqNonce);
  if (reqDigest) RPCBody.addRequestDigest(b, reqDigest);
  b.finish(RPCBody.endRPCBody(b), undefined, true);
  return b.asUint8Array().slice();
}

function decodeBody(buf: Uint8Array): RpcBody {
  const r = RPCBody.getSizePrefixedRootAsRPCBody(new flatbuffers.ByteBuffer(buf));
  const copy = (v: Uint8Array | null) => (v ? v.slice() : undefined);
  return {
    method: r.METHOD() ?? undefined,
    route: r.ROUTE() ?? undefined,
    body: copy(r.bodyArray()),
    bodyFileId: r.BODY_FILE_ID() ?? undefined,
    status: r.STATUS(),
    signerKeyId: copy(r.signerKeyIdArray()),
    replyKey: copy(r.replyKeyArray()),
    requestNonce: copy(r.requestNonceArray()),
    requestDigest: copy(r.requestDigestArray()),
  };
}

function encodeEnvelope(env: Omit<RpcEnvelope, 'raw'>): Uint8Array {
  const b = new flatbuffers.Builder(512 + env.ciphertext.length);
  const eph = ENC.createEphemeralPublicKeyVector(b, env.header.ephemeralPublicKey);
  const nonceStart = ENC.createNonceStartVector(b, env.header.nonceStart);
  const rid = env.header.recipientKeyId?.length ? ENC.createRecipientKeyIdVector(b, env.header.recipientKeyId) : 0;
  const ctx = b.createString(env.header.context);
  ENC.startENC(b);
  ENC.addVersion(b, 1);
  ENC.addKeyExchange(b, env.header.keyExchange as number);
  ENC.addSymmetric(b, 0);
  ENC.addKeyDerivation(b, 0);
  ENC.addEphemeralPublicKey(b, eph);
  ENC.addNonceStart(b, nonceStart);
  if (rid) ENC.addRecipientKeyId(b, rid);
  ENC.addContext(b, ctx);
  const encOff = ENC.endENC(b);

  const session = env.sessionId ? b.createString(env.sessionId) : 0;
  const sender = RPC.createSenderKeyIdVector(b, env.senderKeyId);
  const nonce = RPC.createNonceVector(b, env.nonce);
  const ciphertext = RPC.createCiphertextVector(b, env.ciphertext);
  const signer = RPC.createSignerPublicKeyVector(b, env.signer);
  const sigType = b.createString(SIGNATURE_TYPE);
  const sig = RPC.createSignatureVector(b, new Uint8Array(64));
  const jsonSig = RPC.createCanonicalJsonSignatureVector(b, new Uint8Array(64));
  RPC.startRPC(b);
  RPC.addVersion(b, 1);
  RPC.addDirection(b, env.response ? 1 : 0);
  RPC.addEncryption(b, encOff);
  if (session) RPC.addSessionId(b, session);
  RPC.addSenderKeyId(b, sender);
  RPC.addTimestamp(b, BigInt(env.timestampMs));
  RPC.addNonce(b, nonce);
  RPC.addCiphertext(b, ciphertext);
  RPC.addSignerPublicKey(b, signer);
  RPC.addSignatureType(b, sigType);
  RPC.addSignature(b, sig);
  RPC.addCanonicalJsonSignature(b, jsonSig);
  RPC.finishSizePrefixedRPCBuffer(b, RPC.endRPC(b));
  return b.asUint8Array().slice();
}

function rootOf(buf: Uint8Array): RPC {
  return RPC.getSizePrefixedRootAsRPC(new flatbuffers.ByteBuffer(buf));
}

export interface SealOptions {
  response?: boolean;
  recipientPublicKey: Uint8Array;
  keyExchange: EciesKeyExchange;
  signer: RpcSigner;
  sessionId?: string;
  nowMs?: number;
}

/** Encrypt body for the recipient, then sign the envelope. */
export async function seal(body: RpcBody, options: SealOptions): Promise<Uint8Array> {
  const signerPub = options.signer.publicKey;
  const plaintext = encodeBody({ ...body, signerKeyId: signerPub });
  const { header, ciphertext } = await eciesSealField(options.recipientPublicKey, plaintext, CIPHERTEXT_FIELD, {
    keyExchange: options.keyExchange,
    context: options.response ? CONTEXT_RESPONSE : CONTEXT_REQUEST,
  });
  const env = {
    response: Boolean(options.response),
    header,
    sessionId: options.sessionId ?? '',
    senderKeyId: signerPub,
    timestampMs: Math.floor(options.nowMs ?? Date.now()),
    nonce: randomBytes(NONCE_SIZE),
    ciphertext,
    signer: signerPub,
  };
  const buf = encodeEnvelope(env);
  const fbSig = await options.signer.sign(buf);
  const jsonSig = await options.signer.sign(canonicalJson(env));
  const root = rootOf(buf);
  root.signatureArray().set(fbSig);
  root.canonicalJsonSignatureArray().set(jsonSig);
  return buf;
}

/** Parse an envelope and verify both signatures. Does not decrypt. */
export async function open(raw: Uint8Array): Promise<RpcEnvelope> {
  const buf = raw.slice();
  const root = rootOf(buf);
  if (root.VERSION() !== 1) throw new Error(`sealed: unsupported RPC version ${root.VERSION()}`);
  if (root.SIGNATURE_TYPE() !== SIGNATURE_TYPE) throw new Error('sealed: unsupported signature type');
  const enc = root.ENCRYPTION();
  if (!enc) throw new Error('sealed: missing encryption header');
  const header: EciesFieldHeader = {
    keyExchange: enc.KEY_EXCHANGE() as number as EciesKeyExchange,
    ephemeralPublicKey: (enc.ephemeralPublicKeyArray() ?? new Uint8Array()).slice(),
    nonceStart: (enc.nonceStartArray() ?? new Uint8Array()).slice(),
    context: enc.CONTEXT() ?? '',
    recipientKeyId: enc.recipientKeyIdArray()?.slice(),
  };
  const env = {
    response: root.DIRECTION() === 1,
    header,
    sessionId: root.SESSION_ID() ?? '',
    senderKeyId: (root.senderKeyIdArray() ?? new Uint8Array()).slice(),
    timestampMs: Number(root.TIMESTAMP()),
    nonce: (root.nonceArray() ?? new Uint8Array()).slice(),
    ciphertext: root.ciphertextArray().slice(),
    signer: root.signerPublicKeyArray().slice(),
  };
  const fbSigView = root.signatureArray();
  const jsonSigView = root.canonicalJsonSignatureArray();
  const fbSig = fbSigView.slice();
  const jsonSig = jsonSigView.slice();
  if (env.signer.length !== 32 || fbSig.length !== 64 || jsonSig.length !== 64) {
    throw new Error('sealed: signer key or signature has the wrong length');
  }
  if (env.nonce.length !== NONCE_SIZE || env.ciphertext.length === 0) throw new Error('sealed: nonce or ciphertext missing');
  fbSigView.fill(0);
  jsonSigView.fill(0);
  if (!(await verify(env.signer, buf, fbSig))) throw new Error('sealed: FlatBuffer signature does not verify');
  if (!(await verify(env.signer, canonicalJson(env), jsonSig))) throw new Error('sealed: canonical JSON signature does not verify');
  return { ...env, raw };
}

/** Decrypt an envelope; the body must name the envelope's signer. */
export async function decrypt(env: RpcEnvelope, recipientPrivateKey: Uint8Array): Promise<RpcBody> {
  const plaintext = await eciesOpenField(
    recipientPrivateKey,
    env.header,
    env.ciphertext,
    CIPHERTEXT_FIELD,
    env.response ? CONTEXT_RESPONSE : CONTEXT_REQUEST,
  );
  const body = decodeBody(plaintext);
  if (!body.signerKeyId || !equal(body.signerKeyId, env.signer)) {
    throw new Error('sealed: the sealed body names a different signer than the envelope');
  }
  return body;
}

/** An in-memory Ed25519 session key (never leaves this page). */
export async function createSessionSigner(): Promise<RpcSigner & { seed: Uint8Array }> {
  const seed = randomBytes(32);
  const { ed25519PublicKey } = await import('./crypto/hd-wallet');
  const publicKey = await ed25519PublicKey(seed);
  return { seed, publicKey, sign: (message) => sign(seed, message) };
}

export interface NodeTransport {
  encryptionKey: Uint8Array;
  signingKey: Uint8Array;
  fingerprint: string;
}

export interface RpcResult {
  status: number;
  contentType: string;
  body: Uint8Array;
}

/**
 * Send one sealed command and open its reply. The reply must be signed by
 * the node's advertised signing key and bound to this exact request.
 */
export async function call(
  origin: string,
  node: NodeTransport,
  signer: RpcSigner,
  request: { method: string; route: string; body?: Uint8Array; contentType?: string },
  options: { sessionId?: string; fetchImpl?: typeof fetch } = {},
): Promise<RpcResult> {
  const replyPriv = randomBytes(32);
  const replyPub = await x25519PublicKey(replyPriv);
  const envelope = await seal(
    { method: request.method, route: request.route, body: request.body, bodyFileId: request.contentType, replyKey: replyPub },
    { recipientPublicKey: node.encryptionKey, keyExchange: EciesKeyExchange.Secp256k1, signer, sessionId: options.sessionId },
  );
  const sent = await open(envelope);
  const response = await (options.fetchImpl ?? fetch)(`${origin}${RPC_ROUTE}`, {
    method: 'POST',
    credentials: 'omit',
    cache: 'no-store',
    headers: { 'Content-Type': RPC_CONTENT_TYPE },
    body: envelope,
  });
  if (!response.ok) {
    return { status: response.status, contentType: response.headers.get('Content-Type') ?? 'text/plain', body: new Uint8Array(await response.arrayBuffer()) };
  }
  const reply = await open(new Uint8Array(await response.arrayBuffer()));
  if (!reply.response || !equal(reply.signer, node.signingKey)) throw new Error('sealed: reply is not signed by this node');
  const body = await decrypt(reply, replyPriv);
  if (!body.requestNonce || !equal(body.requestNonce, sent.nonce)) throw new Error('sealed: reply answers a different request');
  if (!body.requestDigest || !equal(body.requestDigest, await sha256(envelope))) throw new Error('sealed: reply answers a different request');
  return { status: body.status ?? 0, contentType: body.bodyFileId ?? '', body: body.body ?? new Uint8Array() };
}
