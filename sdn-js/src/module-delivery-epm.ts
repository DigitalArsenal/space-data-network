/**
 * The requester $EPM an allowlisted paid-module grant needs (module-delivery
 * gate, owner 2026-10-01: "Production, wallet-gated"; "Wallet signs grant").
 *
 * The licensing key server proves the requester holds the session Ed25519 key
 * through the challenge signature. For a module with ALLOWED_XPUBS it also
 * needs this EPM: signed by that same session key, carrying the account xpub
 * key, and carrying the account key's own ChainProof over the statement
 * "sdn-module-delivery-key/1" that names the session key, the xpub, the page
 * origin and an expiry (hd-wallet `signModuleDeliveryKey`). A copied xpub
 * therefore cannot pass an allowlist.
 *
 * The signed bytes are the RFC 8785 (JCS) canonical JSON of the EPM content,
 * built exactly as modules/common/epm/epm_content.cpp builds it.
 */
import * as flatbuffers from 'flatbuffers';
import { ChainProofT } from 'spacedatastandards.org/lib/js/EPM/ChainProof.js';
import { CryptoKeyT } from 'spacedatastandards.org/lib/js/EPM/CryptoKey.js';
import { EPMT } from 'spacedatastandards.org/lib/js/EPM/EPM.js';
import { EntityType } from 'spacedatastandards.org/lib/js/EPM/EntityType.js';
import { KeyType } from 'spacedatastandards.org/lib/js/EPM/KeyType.js';
import { sign } from './crypto/hd-wallet';

/** The account key's ChainProof fields, as hd-wallet `signModuleDeliveryKey` returns them. */
export interface ModuleDeliveryKeyProof {
  keyPath: string;
  accountXpub: string;
  publicKeyHex: string;
  signedPayloadHex: string;
  signatureHex: string;
  algorithm: 'secp256k1';
  encoding: 'der';
}

type Json = string | number | Json[] | { [key: string]: Json };

function hex(bytes: Uint8Array): string {
  let out = '';
  for (const byte of bytes) out += byte.toString(16).padStart(2, '0');
  return out;
}

function compareUtf16(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/** RFC 8785 for the values an EPM carries: strings, integers, arrays, objects. */
function canonicalJson(value: Json): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`;
  if (typeof value === 'object') {
    const keys = Object.keys(value).sort(compareUtf16);
    return `{${keys.map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`).join(',')}}`;
  }
  return JSON.stringify(value);
}

interface ContentKey {
  PUBLIC_KEY: string;
  XPUB?: string;
  KEY_TYPE: 'Signing';
  KEY_PATH?: string;
  ALGORITHM: string;
}

function contentKey(key: ContentKey): Json {
  const entry: { [k: string]: Json } = { PUBLIC_KEY: key.PUBLIC_KEY };
  if (key.XPUB) entry.XPUB = key.XPUB;
  entry.KEY_TYPE = key.KEY_TYPE;
  if (key.KEY_PATH) entry.KEY_PATH = key.KEY_PATH;
  entry.ALGORITHM = key.ALGORITHM;
  return entry;
}

/**
 * Build and sign the requester $EPM for a module-delivery challenge.
 * `signingKey` is the session Ed25519 key the challenge is signed with.
 */
export async function buildModuleDeliveryRequesterEpm(options: {
  signingKey: { privateKey: Uint8Array; publicKey: Uint8Array };
  proof: ModuleDeliveryKeyProof;
  timestampSeconds?: number;
}): Promise<Uint8Array> {
  const { signingKey, proof } = options;
  if (proof.algorithm !== 'secp256k1' || proof.encoding !== 'der') {
    throw new Error('module delivery key proof must be a secp256k1 DER signature');
  }
  const timestamp = Math.floor(options.timestampSeconds ?? Date.now() / 1000);
  const sessionKey: ContentKey = {
    PUBLIC_KEY: hex(signingKey.publicKey),
    KEY_TYPE: 'Signing',
    ALGORITHM: 'ed25519',
  };
  const accountKey: ContentKey = {
    PUBLIC_KEY: proof.publicKeyHex,
    XPUB: proof.accountXpub,
    KEY_TYPE: 'Signing',
    KEY_PATH: proof.keyPath,
    ALGORITHM: 'secp256k1',
  };
  const chainProof = {
    PUBLIC_KEY: proof.publicKeyHex,
    KEY_PATH: proof.keyPath,
    SIGNATURE: proof.signatureHex,
    SIGNED_PAYLOAD: proof.signedPayloadHex,
    ALGORITHM: proof.algorithm,
    ENCODING: proof.encoding,
  };
  const content: Json = {
    KEYS: [contentKey(sessionKey), contentKey(accountKey)],
    ENTITY_TYPE: 'User',
    SIGNATURE_TIMESTAMP: timestamp,
    SIGNATURE_ALGORITHM: 'ed25519',
    CHAIN_PROOFS: [chainProof],
  };
  const signature = await sign(
    signingKey.privateKey,
    new TextEncoder().encode(canonicalJson(content)),
  );

  const epm = new EPMT();
  epm.KEYS = [sessionKey, accountKey].map(
    (key) =>
      new CryptoKeyT(
        key.PUBLIC_KEY,
        key.XPUB ?? null,
        null,
        null,
        null,
        null,
        KeyType.Signing,
        key.KEY_PATH ?? null,
        key.ALGORITHM,
        null,
      ),
  );
  epm.ENTITY_TYPE = EntityType.User;
  epm.SIGNATURE_TIMESTAMP = BigInt(timestamp);
  epm.SIGNATURE_ALGORITHM = 'ed25519';
  epm.SIGNATURE = hex(signature);
  epm.CHAIN_PROOFS = [
    new ChainProofT(
      null,
      null,
      chainProof.PUBLIC_KEY,
      chainProof.KEY_PATH,
      chainProof.SIGNATURE,
      chainProof.SIGNED_PAYLOAD,
      chainProof.ALGORITHM,
      chainProof.ENCODING,
    ),
  ];
  const builder = new flatbuffers.Builder(1024);
  builder.finish(epm.pack(builder), '$EPM');
  return builder.asUint8Array().slice();
}
