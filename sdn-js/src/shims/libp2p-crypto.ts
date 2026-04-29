import * as keys from './libp2p-crypto-keys';
import { pbkdf2Sha256 } from '../crypto/hd-wallet';

export { keys };

export function randomBytes(length: number): Uint8Array {
  if (!Number.isFinite(length) || length <= 0) {
    throw new Error('random bytes length must be a positive number');
  }
  const bytes = new Uint8Array(Math.trunc(length));
  globalThis.crypto.getRandomValues(bytes);
  return bytes;
}

export function pbkdf2(
  password: Uint8Array,
  salt: Uint8Array,
  iterations: number,
  keySize: number,
  hash: string,
): Promise<string> {
  if (hash !== 'sha2-256') {
    throw new Error(`Unsupported PBKDF2 hash '${hash}' in the SDN browser bundle`);
  }
  return pbkdf2Sha256(password, salt, iterations, keySize).then((bytes) =>
    base64Encode(bytes),
  );
}

export const hmac = {
  create(): never {
    throw new Error('HMAC must run through the SDN native crypto boundary');
  },
};

function base64Encode(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  if (typeof btoa === 'function') {
    return btoa(binary);
  }
  throw new Error('base64 encoding requires btoa()');
}
