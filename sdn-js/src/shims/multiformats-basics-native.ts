import * as base10 from 'multiformats/bases/base10';
import * as base16 from 'multiformats/bases/base16';
import * as base2 from 'multiformats/bases/base2';
import * as base256emoji from 'multiformats/bases/base256emoji';
import * as base32 from 'multiformats/bases/base32';
import * as base36 from 'multiformats/bases/base36';
import * as base58 from 'multiformats/bases/base58';
import * as base64 from 'multiformats/bases/base64';
import * as base8 from 'multiformats/bases/base8';
import * as identityBase from 'multiformats/bases/identity';
import * as json from 'multiformats/codecs/json';
import * as raw from 'multiformats/codecs/raw';
import * as identityHash from 'multiformats/hashes/identity';
import { CID, bytes, digest, hasher, varint } from 'multiformats';

import * as sha2 from './multiformats-sha2-native';

export const bases: Record<string, any> = {
  ...identityBase,
  ...base2,
  ...base8,
  ...base10,
  ...base16,
  ...base32,
  ...base36,
  ...base58,
  ...base64,
  ...base256emoji,
};

export const hashes: Record<string, any> = {
  ...sha2,
  ...identityHash,
};

export const codecs: Record<string, any> = { raw, json };

export { CID, hasher, digest, varint, bytes };
