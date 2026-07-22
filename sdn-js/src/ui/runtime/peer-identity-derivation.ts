import { derivePublicIdentityKeysFromXpub } from '../../crypto/hd-wallet';
import type { HostedEpmRecord } from './identity';
import { createPublicEpmExport } from './identity-vcard';

const XPUB_FIELDS = ['xpub', 'XPUB', 'extended_public_key', 'extendedPublicKey', 'hd_xpub', 'hdXpub'];
const DEFAULT_ACCOUNT = 0;

/**
 * Protected local-node helper for deriving public child keys from an xpub.
 * Public web presenters intentionally display only keys already published in
 * an EPM and never import this module.
 */
export async function deriveHostedEpmRecordKeysFromXpub(record: HostedEpmRecord): Promise<HostedEpmRecord> {
  const epmJson = await deriveEpmJsonKeysFromXpub(record.epmJson);
  return {
    ...record,
    epmJson,
  };
}

export async function deriveEpmJsonKeysFromXpub(input: Record<string, unknown>): Promise<Record<string, unknown>> {
  const epm = createPublicEpmExport(input);
  const xpub = identityXpubValue(epm);
  if (!xpub) return epm;

  const account = identityAccount(epm);
  epm.xpub = xpub;

  let derived;
  try {
    derived = await derivePublicIdentityKeysFromXpub(xpub, account);
  } catch {
    return epm;
  }

  const preservedKeys = (Array.isArray(epm.keys) ? epm.keys : [])
    .filter((item) => !isIdentityKeyType(item, 'signing') && !isIdentityKeyType(item, 'encryption'))
    .map((item) => (isRecord(item) ? { ...item } : item));

  epm.signing_public_key = derived.signingPublicKey;
  epm.encryption_public_key = derived.encryptionPublicKey;
  epm.keys = [
    {
      key_type: 'signing',
      address_type: 'secp256k1',
      public_key: derived.signingPublicKey,
      derivation_path: derived.signingKeyPath,
      xpub,
    },
    {
      key_type: 'encryption',
      address_type: 'secp256k1',
      public_key: derived.encryptionPublicKey,
      derivation_path: derived.encryptionKeyPath,
      xpub,
    },
    ...preservedKeys,
  ];

  return epm;
}

function identityXpubValue(epm: Record<string, unknown>): string | undefined {
  const direct = pickString(epm, XPUB_FIELDS);
  if (direct) return direct;
  const keys = Array.isArray(epm.keys) ? epm.keys : [];
  for (const key of keys) {
    if (!isRecord(key)) continue;
    const xpub = pickString(key, XPUB_FIELDS);
    if (xpub) return xpub;
  }
  return undefined;
}

function identityAccount(epm: Record<string, unknown>): number {
  const account = pickNumber(epm, ['account', 'wallet_account', 'walletAccount']);
  if (typeof account === 'number' && Number.isInteger(account) && account >= 0) return account;
  return DEFAULT_ACCOUNT;
}

function isIdentityKeyType(value: unknown, type: 'signing' | 'encryption'): boolean {
  if (!isRecord(value)) return false;
  const keyType = (pickString(value, ['key_type', 'KEY_TYPE', 'keyType']) || '').toLowerCase();
  const addressType = (pickString(value, ['address_type', 'ADDRESS_TYPE', 'addressType']) || '').toLowerCase();
  if (type === 'encryption') return keyType === 'encryption' || addressType === 'x25519';
  return keyType === 'signing' || (addressType !== '' && addressType !== 'x25519');
}

function pickString(record: Record<string, unknown>, keys: string[]): string | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return undefined;
}

function pickNumber(record: Record<string, unknown>, keys: string[]): number | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'number' && Number.isFinite(value)) return value;
  }
  return undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}
