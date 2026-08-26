export type HostedEpmKind = 'node-self' | 'hosted';

export interface HostedEpmRecord {
  id: string;
  kind: HostedEpmKind;
  label: string;
  peerId: string;
  epmCid?: string;
  epmJson: Record<string, unknown>;
  source?: string;
  updatedAt?: number;
}

const SECRET_KEYS = new Set([
  'private_key',
  'PRIVATE_KEY',
  'xpriv',
  'XPRIV',
  'mnemonic',
  'seed',
  'core',
  'privateKey',
  'secret',
  'encrypted_core',
  'encryptedCore',
]);
const NORMALIZED_SECRET_KEYS = new Set([
  'core',
  'encryptedcore',
  'mnemonic',
  'seed',
  'xpriv',
  'walletprivate',
  'walletprivatekey',
  'walletprivatematerial',
]);
const PUBLIC_KEY_FIELDS = [
  'public_key',
  'PUBLIC_KEY',
  'publicKey',
];
const SIGNING_PUBLIC_KEY_FIELDS = ['signing_public_key', 'signingPublicKey', 'signing_pubkey_hex', 'signingPubkeyHex'];
const ENCRYPTION_PUBLIC_KEY_FIELDS = ['encryption_public_key', 'encryptionPublicKey', 'encryption_pubkey_hex', 'encryptionPubkeyHex'];
const XPUB_FIELDS = ['xpub', 'XPUB', 'extended_public_key', 'extendedPublicKey', 'hd_xpub', 'hdXpub'];
const KEY_DERIVATION_PATH_FIELDS = ['derivation_path', 'derivationPath', 'key_address', 'keyAddress', 'KEY_ADDRESS'];
const SIGNING_DERIVATION_PATH_FIELDS = ['signing_derivation_path', 'signingDerivationPath', 'signing_key_path', 'signingKeyPath'];
const ENCRYPTION_DERIVATION_PATH_FIELDS = ['encryption_derivation_path', 'encryptionDerivationPath', 'encryption_key_path', 'encryptionKeyPath'];
const IDENTITY_EMAIL_DOMAINS = {
  signing: 'signing.spacedatanetwork.org',
  encryption: 'encryption.spacedatanetwork.org',
  bitcoin: 'bitcoin.spacedatanetwork.org',
  ethereum: 'ethereum.spacedatanetwork.org',
  solana: 'solana.spacedatanetwork.org',
} as const;
const PEER_ID_ALIAS_DOMAIN = 'peerid.spacedatanetwork.org';
const XPUB_ALIAS_DOMAIN = 'xpub.spacedatanetwork.org';
// §21 (owner ruling 2026-08-19): sign/encrypt aliases carry b64url(literal key
// bytes), not derivation paths. The xpub alias is RETIRED.
const SIGN_ALIAS_DOMAIN = 'sign.spacedatanetwork.org';
const ENCRYPT_ALIAS_DOMAIN = 'encrypt.spacedatanetwork.org';

type IdentityAliasType = keyof typeof IDENTITY_EMAIL_DOMAINS;
export type IdentityPublicKeyType = 'signing' | 'encryption';

export interface IdentityPublicKeyDetails {
  publicKey: string;
  derivationPath?: string;
}

export function normalizeHostedEpmRecord(input: Record<string, unknown>): HostedEpmRecord {
  const epmJson = normalizeRecord(input.epm_json ?? input.epmJson ?? input);
  const id = pickString(input, ['id', 'epm_id', 'epmId']) || pickString(epmJson, ['peer_id', 'peerId']) || 'self';
  const kind = input.kind === 'node-self' ? 'node-self' : 'hosted';
  return {
    id,
    kind,
    label: pickString(epmJson, ['dn', 'DN', 'displayName', 'name', 'legal_name', 'legalName']) || id,
    peerId: pickString(epmJson, ['peer_id', 'peerId', 'PeerID']) || '',
    epmCid: pickString(input, ['epm_cid', 'epmCid']) || pickString(epmJson, ['epm_cid', 'epmCid']),
    epmJson: createPublicEpmExport(epmJson),
    source: pickString(input, ['source']),
    updatedAt: pickNumber(input, ['updated_at', 'updatedAt']),
  };
}

export function createPublicEpmExport(input: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(input)) {
    if (isSecretEpmKey(key)) continue;
    if (Array.isArray(value)) {
      out[key] = value.map((item) => (isRecord(item) ? createPublicEpmExport(item) : item));
    } else if (isRecord(value)) {
      out[key] = createPublicEpmExport(value);
    } else {
      out[key] = value;
    }
  }
  return out;
}

export function createVCardQrPayload(input: Record<string, unknown> | HostedEpmRecord): string {
  const record = isHostedEpmRecord(input) ? input : normalizeHostedEpmRecord(input);
  const epm = createPublicEpmExport(record.epmJson);
  const displayName = pickString(epm, ['dn', 'DN', 'displayName', 'name', 'legal_name', 'legalName', 'organization', 'org'])
    || record.label
    || 'Space Data Network';
  // §21 (owner ruling 2026-08-19): the QR card carries the literal sign +
  // encrypt public keys as EMAIL aliases (b64url of the hex-decoded key bytes),
  // not the xpub. The xpub is private; the card's aliases ARE the verification
  // material. A card without a verifiable signing key is not a servable QR.
  const signKey = identityPublicKeyValue(epm, 'signing');
  const encryptKey = identityPublicKeyValue(epm, 'encryption');
  const signAlias = hexKeyToB64UrlAlias(signKey);
  const encryptAlias = hexKeyToB64UrlAlias(encryptKey);
  if (!signAlias) {
    throw new Error('a verifiable signing public key is required for identity QR');
  }
  const lines = ['BEGIN:VCARD', 'VERSION:3.0'];

  addVCardStructuredName(lines, epm, displayName);
  addVCardLine(lines, 'FN', displayName);
  addVCardLine(lines, 'EMAIL', pickString(epm, ['email']));
  addVCardLine(lines, 'TEL', pickString(epm, ['telephone', 'phone', 'tel']));
  addVCardAddressLine(lines, epm);
  addCompactIdentityEmailLine(lines, 'sign', signAlias, SIGN_ALIAS_DOMAIN);
  if (encryptAlias) {
    addCompactIdentityEmailLine(lines, 'encrypt', encryptAlias, ENCRYPT_ALIAS_DOMAIN);
  }
  lines.push('END:VCARD');
  return `${lines.map(foldVCardLine).join('\r\n')}\r\n`;
}

export function createVCardQrPayloadFromVCard(vcard: string): string {
  const lines = vcardLines(vcard);
  const name = vcardStructuredValue(lines, 'N');
  const address = vcardStructuredValue(lines, 'ADR');
  const displayName = vcardValue(lines, 'FN')
    || [name[1], name[0]].filter(Boolean).join(' ')
    || 'Space Data Network';
  // §21: extract the sign/encrypt literal-key aliases from the source card
  // (b64url local parts at sign./encrypt. domains), not the xpub.
  const signAlias = vcardEmailAlias(lines, SIGN_ALIAS_DOMAIN, 'sign').trim();
  const encryptAlias = vcardEmailAlias(lines, ENCRYPT_ALIAS_DOMAIN, 'encrypt').trim();
  if (!signAlias || !isSafeEmailLocalPart(signAlias)) {
    throw new Error('a verifiable signing public key is required for identity QR');
  }
  const epm: Record<string, unknown> = {
    dn: displayName,
    email: vcardContactEmail(lines),
    telephone: vcardValue(lines, 'TEL'),
  };

  const [familyName, givenName, additionalName, honorificPrefix, honorificSuffix] = name;
  Object.assign(epm, {
    family_name: familyName,
    given_name: givenName,
    additional_name: additionalName,
    honorific_prefix: honorificPrefix,
    honorific_suffix: honorificSuffix,
  });

  if (address.some(Boolean)) {
    epm.address = {
      po_box: address[0],
      street: address[2],
      locality: address[3],
      region: address[4],
      postal_code: address[5],
      country: address[6],
    };
  }

  const compactLines = ['BEGIN:VCARD', 'VERSION:3.0'];
  addVCardStructuredName(compactLines, epm, displayName);
  addVCardLine(compactLines, 'FN', displayName);
  addVCardLine(compactLines, 'EMAIL', pickString(epm, ['email']));
  addVCardLine(compactLines, 'TEL', pickString(epm, ['telephone']));
  addVCardAddressLine(compactLines, epm);
  addCompactIdentityEmailLine(compactLines, 'sign', signAlias, SIGN_ALIAS_DOMAIN);
  if (encryptAlias) {
    addCompactIdentityEmailLine(compactLines, 'encrypt', encryptAlias, ENCRYPT_ALIAS_DOMAIN);
  }
  compactLines.push('END:VCARD');
  return `${compactLines.map(foldVCardLine).join('\r\n')}\r\n`;
}

export function publicKeyEmailAddress(publicKey: string | undefined): string | undefined {
  const localPart = publicKey?.trim().replace(/\s+/g, '').replace(/[^A-Za-z0-9._%+-]/g, '');
  return localPart ? `${localPart}@spacedatanetwork.org` : undefined;
}

export function identityPublicKeyValue(
  epm: Record<string, unknown>,
  type?: IdentityPublicKeyType,
): string | undefined {
  if (type === 'signing') {
    return identityPublicKeyDetails(epm, 'signing')?.publicKey;
  }
  if (type === 'encryption') {
    return identityPublicKeyDetails(epm, 'encryption')?.publicKey;
  }
  return pickString(epm, PUBLIC_KEY_FIELDS);
}

export function identityPublicKeyDetails(
  epm: Record<string, unknown>,
  type: IdentityPublicKeyType,
): IdentityPublicKeyDetails | undefined {
  const directPublicKey = pickString(epm, type === 'signing' ? SIGNING_PUBLIC_KEY_FIELDS : ENCRYPTION_PUBLIC_KEY_FIELDS);
  const keyRecord = findIdentityKeyDetails(epm, type, directPublicKey);
  if (keyRecord) return keyRecord;
  if (!directPublicKey) return undefined;
  const derivationPath = pickString(epm, type === 'signing' ? SIGNING_DERIVATION_PATH_FIELDS : ENCRYPTION_DERIVATION_PATH_FIELDS);
  return derivationPath ? { publicKey: directPublicKey, derivationPath } : { publicKey: directPublicKey };
}

export function epmJsonFromVCard(text: string): Record<string, unknown> {
  const fields: Record<string, unknown> = {};
  const lines = vcardLines(text);
  fields.dn = vcardValue(lines, 'FN');
  fields.legal_name = vcardValue(lines, 'ORG');
  fields.job_title = vcardValue(lines, 'TITLE');
  fields.email = vcardContactEmail(lines);
  fields.telephone = vcardValue(lines, 'TEL');
  fields.peer_id = vcardValue(lines, 'X-SDN-PEER-ID') || vcardEmailAlias(lines, PEER_ID_ALIAS_DOMAIN, 'peerid');
  fields.epm_cid = vcardValue(lines, 'X-SDN-EPM-CID');
  // §21: the xpub alias is retired; the sign/encrypt aliases carry b64url(key
  // bytes) at sign./encrypt. domains. Decode the b64url local part back to hex
  // for the JSON mirror. The legacy X-SDN-* and signing./encryption. domains
  // are read as fallbacks for pre-flip cards.
  fields.public_key = vcardValue(lines, 'X-SDN-PUBLIC-KEY') || vcardEmailAlias(lines, 'spacedatanetwork.org');
  const signAliasLocal = vcardEmailAlias(lines, SIGN_ALIAS_DOMAIN, 'sign');
  fields.signing_public_key = vcardValue(lines, 'X-SDN-SIGNING-PUBLIC-KEY')
    || (signAliasLocal ? b64UrlAliasToHex(signAliasLocal) : vcardIdentityEmailAlias(lines, 'signing'));
  const encryptAliasLocal = vcardEmailAlias(lines, ENCRYPT_ALIAS_DOMAIN, 'encrypt');
  fields.encryption_public_key = vcardValue(lines, 'X-SDN-ENCRYPTION-PUBLIC-KEY')
    || (encryptAliasLocal ? b64UrlAliasToHex(encryptAliasLocal) : vcardIdentityEmailAlias(lines, 'encryption'));
  for (const key of Object.keys(fields)) {
    if (!fields[key]) delete fields[key];
  }
  return fields;
}

export function identityXpubValue(epm: Record<string, unknown>): string | undefined {
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

function addCompactIdentityEmailLine(
  lines: string[],
  type: 'peerid' | 'xpub' | 'sign' | 'encrypt',
  value: string | undefined,
  domain: string,
): void {
  const trimmed = value?.trim();
  if (!trimmed || !isSafeEmailLocalPart(trimmed)) return;
  lines.push(`EMAIL;TYPE=INTERNET;TYPE=${type}:${trimmed}@${domain}`);
}

/**
 * §21: convert a hex-encoded public key to a b64url email-safe local part —
 * the form the sign/encrypt aliases carry. Returns undefined when the hex is
 * absent or undecodable, so the caller skips the alias rather than emitting a
 * dead row.
 */
function hexKeyToB64UrlAlias(hexKey: string | undefined): string | undefined {
  const trimmed = hexKey?.trim();
  if (!trimmed) return undefined;
  try {
    const raw = hexToBytes(trimmed);
    if (!raw.length) return undefined;
    return bytesToB64Url(raw);
  } catch {
    return undefined;
  }
}

function hexToBytes(hex: string): Uint8Array {
  const clean = hex.startsWith('0x') ? hex.slice(2) : hex;
  // Strict like Go's hex.DecodeString: non-hex characters refuse, so a key
  // whose bytes cannot be decoded yields NO alias — the Go side skips, and
  // the mirror must not emit garbage b64url rows.
  if (!/^[0-9a-fA-F]+$/.test(clean) || clean.length % 2 !== 0) throw new Error('invalid hex');
  const out = new Uint8Array(clean.length / 2);
  for (let i = 0; i < out.length; i += 1) {
    out[i] = parseInt(clean.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

function bytesToB64Url(bytes: Uint8Array): string {
  // base64url without padding (RFC 4648 §5), matching Go's RawURLEncoding.
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
  let out = '';
  for (let i = 0; i < bytes.length; i += 3) {
    const b0 = bytes[i];
    const b1 = i + 1 < bytes.length ? bytes[i + 1] : 0;
    const b2 = i + 2 < bytes.length ? bytes[i + 2] : 0;
    out += chars[b0 >> 2];
    out += chars[((b0 & 0x03) << 4) | (b1 >> 4)];
    if (i + 1 < bytes.length) out += chars[((b1 & 0x0f) << 2) | (b2 >> 6)];
    if (i + 2 < bytes.length) out += chars[b2 & 0x3f];
  }
  return out;
}

/**
 * §21: inverse of hexKeyToB64UrlAlias — decode a b64url alias local part back
 * to the hex-encoded public key the record carries. Returns '' on decode
 * failure so the caller's falsy-skip drops it.
 */
function b64UrlAliasToHex(b64url: string): string {
  try {
    const raw = b64UrlToBytes(b64url);
    return Array.from(raw, (b) => b.toString(16).padStart(2, '0')).join('');
  } catch {
    return '';
  }
}

function b64UrlToBytes(b64url: string): Uint8Array {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
  const map = new Map<string, number>();
  for (let i = 0; i < chars.length; i += 1) map.set(chars[i], i);
  const clean = b64url.replace(/=+$/, '');
  const out: number[] = [];
  for (let i = 0; i < clean.length; i += 4) {
    const c0 = map.get(clean[i]) ?? 0;
    const c1 = i + 1 < clean.length ? (map.get(clean[i + 1]) ?? 0) : 0;
    const c2 = i + 2 < clean.length ? (map.get(clean[i + 2]) ?? 0) : 0;
    const c3 = i + 3 < clean.length ? (map.get(clean[i + 3]) ?? 0) : 0;
    out.push((c0 << 2) | (c1 >> 4));
    if (i + 1 < clean.length) out.push(((c1 & 0x0f) << 4) | (c2 >> 2));
    if (i + 2 < clean.length) out.push(((c2 & 0x03) << 6) | c3);
  }
  return new Uint8Array(out);
}

function addVCardStructuredName(lines: string[], epm: Record<string, unknown>, displayName: string): void {
  const familyName = pickString(epm, ['family_name', 'familyName']);
  let givenName = pickString(epm, ['given_name', 'givenName']);
  const additionalName = pickString(epm, ['additional_name', 'additionalName']);
  const honorificPrefix = pickString(epm, ['honorific_prefix', 'honorificPrefix']);
  const honorificSuffix = pickString(epm, ['honorific_suffix', 'honorificSuffix']);
  if (!familyName && !givenName && !additionalName && !honorificPrefix && !honorificSuffix) {
    givenName = displayName;
  }
  lines.push(`N:${[familyName, givenName, additionalName, honorificPrefix, honorificSuffix].map(escapeVCardValue).join(';')}`);
}

function addVCardAddressLine(lines: string[], epm: Record<string, unknown>): void {
  const address = isRecord(epm.address) ? epm.address : epm;
  const parts = [
    pickString(address, ['po_box', 'poBox']),
    '',
    pickString(address, ['street', 'street_address', 'streetAddress']),
    pickString(address, ['locality', 'city']),
    pickString(address, ['region', 'state', 'province']),
    pickString(address, ['postal_code', 'postalCode', 'zip']),
    pickString(address, ['country', 'country_name', 'countryName']),
  ];
  if (parts.some(Boolean)) {
    lines.push(`ADR;TYPE=WORK:${parts.map(escapeVCardValue).join(';')}`);
  }
}

function findIdentityKeyDetails(
  epm: Record<string, unknown>,
  type: IdentityPublicKeyType,
  preferredPublicKey?: string,
): IdentityPublicKeyDetails | undefined {
  const keys = Array.isArray(epm.keys) ? epm.keys : [];
  let fallback: IdentityPublicKeyDetails | undefined;
  for (const key of keys) {
    if (!isRecord(key)) continue;
    const publicKey = pickString(key, ['public_key', 'PUBLIC_KEY', 'publicKey']);
    if (!publicKey) continue;
    const keyType = (pickString(key, ['key_type', 'KEY_TYPE', 'keyType']) || '').toLowerCase();
    const addressType = (pickString(key, ['address_type', 'ADDRESS_TYPE', 'addressType']) || '').toLowerCase();
    const xpub = pickString(key, XPUB_FIELDS);
    const keyPath = pickString(key, KEY_DERIVATION_PATH_FIELDS);
    const matches = isIdentityRoleKey(type, keyType, addressType, keyPath, xpub);
    if (!matches) continue;
    const details = keyPath ? { publicKey, derivationPath: keyPath } : { publicKey };
    if (preferredPublicKey && publicKey === preferredPublicKey) return details;
    fallback ??= details;
  }
  return fallback;
}

function isIdentityRoleKey(
  type: IdentityPublicKeyType,
  keyType: string,
  addressType: string,
  keyPath: string | undefined,
  xpub: string | undefined,
): boolean {
  // §21 (owner ruling 2026-08-19): a key is a sign/encrypt identity key if it
  // has the right KEY_TYPE and carries a PUBLIC_KEY — no xpub or derivation
  // path is required. The xpub and path are private; the PUBLIC_KEY is the
  // published verification material. The legacy xpub/path matching is
  // retained as a fallback for pre-flip records that carry no KEY_TYPE.
  if (keyType === type) return true;
  if (type === 'encryption' && addressType === 'x25519') return true;
  // Pre-flip fallback: a key with an xpub and a documented path still
  // qualifies (it was published under the old paradigm).
  if (xpub && isDocumentedIdentityPath(type, keyPath)) return true;
  return false;
}

function isDocumentedIdentityPath(type: IdentityPublicKeyType, path: string | undefined): boolean {
  const match = /^m\/44'\/0'\/\d+'\/([01])\/\d+$/.exec(path ?? '');
  if (!match) return false;
  return type === 'signing' ? match[1] === '0' : match[1] === '1';
}

function addVCardLine(lines: string[], key: string, value: string | undefined): void {
  if (value?.trim()) lines.push(`${key}:${escapeVCardValue(value)}`);
}

function foldVCardLine(line: string): string {
  if (line.length <= 74) return line;
  const chunks: string[] = [];
  for (let offset = 0; offset < line.length; offset += 74) {
    chunks.push(line.slice(offset, offset + 74));
  }
  return chunks.join('\r\n ');
}

function escapeVCardValue(value: string | undefined): string {
  return String(value ?? '')
    .replace(/\\/g, '\\\\')
    .replace(/\r?\n/g, '\\n')
    .replace(/,/g, '\\,')
    .replace(/;/g, '\\;');
}

function isSafeEmailLocalPart(value: string): boolean {
  return /^[A-Za-z0-9._+-]+$/.test(value.trim());
}

function vcardLines(vcard: string): string[] {
  return unfoldedVCard(vcard).split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

function unfoldedVCard(vcard: string): string {
  return String(vcard ?? '').replace(/\r?\n[ \t]/g, '');
}

function vcardValue(lines: string[], fieldName: string): string {
  const normalizedField = fieldName.toUpperCase();
  for (const line of lines) {
    const parsed = parseVCardLine(line);
    if (parsed?.name === normalizedField) return unescapeVCardValue(parsed.value);
  }
  return '';
}

function vcardStructuredValue(lines: string[], fieldName: string): string[] {
  const normalizedField = fieldName.toUpperCase();
  for (const line of lines) {
    const parsed = parseVCardLine(line);
    if (parsed?.name !== normalizedField) continue;
    return splitVCardStructuredValue(parsed.value).map(unescapeVCardValue);
  }
  return [];
}

function splitVCardStructuredValue(value: string): string[] {
  const parts: string[] = [];
  let current = '';
  let escaped = false;
  for (const character of value) {
    if (escaped) {
      current += `\\${character}`;
      escaped = false;
      continue;
    }
    if (character === '\\') {
      escaped = true;
      continue;
    }
    if (character === ';') {
      parts.push(current);
      current = '';
      continue;
    }
    current += character;
  }
  if (escaped) current += '\\';
  parts.push(current);
  return parts;
}

function vcardContactEmail(lines: string[]): string {
  for (const email of vcardEmailEntries(lines)) {
    if (!isSdnAliasEmail(email.value, email.params)) return unescapeVCardValue(email.value);
  }
  return '';
}

function vcardIdentityEmailAlias(lines: string[], type: IdentityAliasType): string {
  const domain = IDENTITY_EMAIL_DOMAINS[type];
  const suffix = `@${domain}`.toLowerCase();
  for (const email of vcardEmailEntries(lines)) {
    const value = unescapeVCardValue(email.value);
    const lower = value.toLowerCase();
    if (!lower.endsWith(suffix)) continue;
    if (!vcardEmailHasType(email.params, type)) continue;
    return value.slice(0, -suffix.length);
  }
  return '';
}

function vcardEmailAlias(lines: string[], domain: string, type?: string): string {
  const suffix = `@${domain}`.toLowerCase();
  for (const email of vcardEmailEntries(lines)) {
    const value = unescapeVCardValue(email.value);
    if (type && !vcardEmailHasType(email.params, type)) continue;
    if (value.toLowerCase().endsWith(suffix)) return value.slice(0, -suffix.length);
  }
  return '';
}

function vcardEmailEntries(lines: string[]): Array<{ params: string[]; value: string }> {
  return lines
    .map(parseVCardLine)
    .filter((line): line is { name: string; params: string[]; value: string } => line?.name === 'EMAIL');
}

function parseVCardLine(line: string): { name: string; params: string[]; value: string } | null {
  const colon = line.indexOf(':');
  if (colon < 0) return null;
  const left = line.slice(0, colon);
  const [name = '', ...params] = left.split(';');
  return {
    name: name.toUpperCase(),
    params,
    value: line.slice(colon + 1),
  };
}

function vcardEmailHasType(params: string[], type: string): boolean {
  const wanted = type.toLowerCase();
  return params.some((param) => {
    const normalized = param.trim().toLowerCase();
    if (normalized === wanted) return true;
    const [, rawValue = normalized] = normalized.split('=', 2);
    return rawValue.split(',').map((part) => part.trim()).includes(wanted);
  });
}

function isSdnAliasEmail(value: string, params: string[]): boolean {
  const normalized = unescapeVCardValue(value).toLowerCase();
  if (normalized.endsWith('@spacedatanetwork.org')) return true;
  if (normalized.endsWith(`@${PEER_ID_ALIAS_DOMAIN}`)) return true;
  if (normalized.endsWith(`@${XPUB_ALIAS_DOMAIN}`)) return true;
  // §21: the sign/encrypt literal-key alias domains.
  if (normalized.endsWith(`@${SIGN_ALIAS_DOMAIN}`)) return true;
  if (normalized.endsWith(`@${ENCRYPT_ALIAS_DOMAIN}`)) return true;
  if (Object.values(IDENTITY_EMAIL_DOMAINS).some((domain) => normalized.endsWith(`@${domain}`))) return true;
  return Object.keys(IDENTITY_EMAIL_DOMAINS).some((type) => vcardEmailHasType(params, type));
}

function unescapeVCardValue(value: string): string {
  return String(value ?? '')
    .replace(/\\n/g, '\n')
    .replace(/\\,/g, ',')
    .replace(/\\;/g, ';')
    .replace(/\\\\/g, '\\')
    .trim();
}

function normalizeRecord(value: unknown): Record<string, unknown> {
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value);
      return isRecord(parsed) ? parsed : {};
    } catch {
      return {};
    }
  }
  return isRecord(value) ? { ...value } : {};
}

function pickString(input: Record<string, unknown>, keys: string[]): string | undefined {
  for (const key of keys) {
    const value = input[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
    if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  }
  return undefined;
}

function pickNumber(input: Record<string, unknown>, keys: string[]): number | undefined {
  for (const key of keys) {
    const value = input[key];
    if (typeof value === 'number' && Number.isFinite(value)) return value;
  }
  return undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function isSecretEpmKey(key: string): boolean {
  if (SECRET_KEYS.has(key)) return true;
  const normalized = key.replace(/[^a-z0-9]/gi, '').toLowerCase();
  return (
    NORMALIZED_SECRET_KEYS.has(normalized) ||
    normalized.includes('encryptedcore') ||
    normalized.includes('walletprivate') ||
    normalized.includes('private') ||
    normalized.includes('secret') ||
    normalized.includes('mnemonic') ||
    normalized.includes('xpriv') ||
    normalized === 'seed' ||
    normalized.endsWith('seed')
  );
}

function isHostedEpmRecord(value: Record<string, unknown> | HostedEpmRecord): value is HostedEpmRecord {
  return (
    typeof value.id === 'string' &&
    (value.kind === 'node-self' || value.kind === 'hosted') &&
    typeof value.label === 'string' &&
    typeof value.peerId === 'string' &&
    isRecord(value.epmJson)
  );
}
