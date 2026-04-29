import { from } from 'multiformats/hashes/hasher';

export const sha1 = from({
  name: 'sha-1',
  code: 0x11,
  encode(): never {
    throw new Error('SHA-1 is disabled in the SDN browser bundle');
  },
});
