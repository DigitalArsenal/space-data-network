import { from } from 'multiformats/hashes/hasher';

import { sha256 as nativeSha256, sha512 as nativeSha512 } from '../crypto/hd-wallet';

// multiformats 14 narrows a hasher's output to Uint8Array<ArrayBuffer>; the
// native digests come back as Uint8Array<ArrayBufferLike>, which is the same
// bytes with a wider buffer type.
export const sha256 = from({
  name: 'sha2-256',
  code: 0x12,
  encode: (input: Uint8Array) =>
    nativeSha256(input) as Promise<Uint8Array<ArrayBuffer>>,
});

export const sha512 = from({
  name: 'sha2-512',
  code: 0x13,
  encode: (input: Uint8Array) =>
    nativeSha512(input) as Promise<Uint8Array<ArrayBuffer>>,
});
