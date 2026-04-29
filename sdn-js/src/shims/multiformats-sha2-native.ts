import { from } from 'multiformats/hashes/hasher';

import { sha256 as nativeSha256, sha512 as nativeSha512 } from '../crypto/hd-wallet';

export const sha256 = from({
  name: 'sha2-256',
  code: 0x12,
  encode: (input: Uint8Array) => nativeSha256(input),
});

export const sha512 = from({
  name: 'sha2-512',
  code: 0x13,
  encode: (input: Uint8Array) => nativeSha512(input),
});
