import * as flatbuffers from 'flatbuffers';
import {
  FSP,
  fieldStreamAudienceCategory,
  fieldStreamDecisionCategory,
  fieldStreamOperationCategory,
  fieldStreamRevocationCategory,
} from 'spacedatastandards.org/lib/js/FSP/main.js';
import {
  FSM,
  fieldStreamValueEncodingCategory,
  fieldStreamValueStateCategory,
} from 'spacedatastandards.org/lib/js/FSM/main.js';

import { aesGcmDecryptWithIv } from './crypto/hd-wallet';

type GeneratedEnum = Record<string, string | number>;
type FieldKeyMap = Record<string, Uint8Array> | Map<string, Uint8Array>;

export interface FieldStreamPolicySummary {
  policyId: string;
  policyVersion: number;
  providerPeerId: string;
  listingId: string;
  streamId: string;
  schemaCode: string;
  schemaHash?: Uint8Array;
  audiences: FieldStreamAudienceSummary[];
  rules: FieldStreamRuleSummary[];
  allowedOperations: string[];
  keyScope?: string;
  keyEpoch?: string;
  validFrom?: bigint;
  expiresAt?: bigint;
  revocationStatus: string;
  revokedAt?: bigint;
  revocationReason?: string;
  providerSignature?: Uint8Array;
}

export interface FieldStreamAudienceSummary {
  audienceType: string;
  subjectId?: string;
  subjectEpmCid?: string;
  subjectKeyId?: string;
}

export interface FieldStreamRuleSummary {
  fieldPath: string;
  fieldIdPath: number[];
  decision: string;
  tags: string[];
  requiredAttributes: string[];
  keyId?: string;
}

export interface FieldStreamMessageSummary {
  messageId: string;
  providerPeerId: string;
  listingId: string;
  streamId: string;
  schemaCode: string;
  schemaHash?: Uint8Array;
  policyId: string;
  policyVersion: number;
  keyEpoch?: string;
  sequence: bigint;
  producedAt?: bigint;
  expiresAt?: bigint;
  subjectId?: string;
  fields: FieldStreamFieldSummary[];
  payloadHash?: Uint8Array;
  previousMessageHash?: Uint8Array;
  providerSignature?: Uint8Array;
}

export interface FieldStreamFieldSummary {
  fieldPath: string;
  fieldIdPath: number[];
  state: string;
  encoding: string;
  value?: Uint8Array;
  ciphertext?: Uint8Array;
  nonce?: Uint8Array;
  tag?: Uint8Array;
  keyId?: string;
  aadHash?: Uint8Array;
  releaseTags: string[];
  decision?: string;
  ciphertextLength: number;
}

export type FieldStreamFieldVisibility =
  | 'public'
  | 'decrypted'
  | 'encrypted'
  | 'redacted'
  | 'unavailable';

export interface FieldStreamAccessGrant {
  subjectId: string;
  providerPeerId: string;
  listingId: string;
  streamId: string;
  schemaCode?: string;
  policyId: string;
  policyVersion: number;
  keyEpoch?: string;
  allowedFieldPaths: string[];
  redactedFieldPaths?: string[];
  fieldKeysById?: FieldKeyMap;
  expiresAt?: bigint | number | string | Date;
  revokedAt?: bigint | number | string | Date;
  revocationReason?: string;
}

export interface FieldStreamDecryptFieldInput {
  message: FieldStreamMessageSummary;
  field: FieldStreamFieldSummary;
  grant: FieldStreamAccessGrant;
  fieldPath: string;
  keyId: string;
  keyBytes: Uint8Array;
  ciphertext: Uint8Array;
  tag: Uint8Array;
  nonce: Uint8Array;
  aad: Uint8Array;
}

export interface ResolveFieldStreamMessageViewOptions {
  now?: bigint | number | string | Date;
  decryptField?: (input: FieldStreamDecryptFieldInput) => Promise<Uint8Array> | Uint8Array;
  aadForField?: (
    message: FieldStreamMessageSummary,
    field: FieldStreamFieldSummary,
    grant: FieldStreamAccessGrant,
  ) => Uint8Array | undefined;
}

export interface FieldStreamResolvedField {
  fieldPath: string;
  fieldIdPath: number[];
  state: string;
  visibility: FieldStreamFieldVisibility;
  encoding: string;
  value?: Uint8Array;
  plaintext?: Uint8Array;
  keyId?: string;
  releaseTags: string[];
  decision?: string;
  reason?: string;
  ciphertextLength: number;
}

export interface FieldStreamMessageView {
  messageId: string;
  providerPeerId: string;
  listingId: string;
  streamId: string;
  schemaCode: string;
  policyId: string;
  policyVersion: number;
  keyEpoch?: string;
  sequence: bigint;
  subjectId?: string;
  grantSubjectId: string;
  fields: FieldStreamResolvedField[];
}

export function decodeFieldStreamPolicySummary(bytes: Uint8Array): FieldStreamPolicySummary {
  if (bytes.length === 0) {
    throw new Error('field stream policy bytes are empty');
  }
  if (!FSP.bufferHasIdentifier(new flatbuffers.ByteBuffer(bytes))) {
    throw new Error('field stream policy identifier mismatch');
  }
  const policy = FSP.getRootAsFSP(new flatbuffers.ByteBuffer(bytes));
  const policyId = policy.POLICY_ID();
  if (!policyId) {
    throw new Error('field stream policy id is required');
  }
  const audiences: FieldStreamAudienceSummary[] = [];
  for (let i = 0; i < policy.audiencesLength(); i += 1) {
    const audience = policy.AUDIENCES(i);
    if (audience) {
      audiences.push({
        audienceType: enumName(fieldStreamAudienceCategory, audience.AUDIENCE_TYPE()),
        subjectId: emptyToUndefined(audience.SUBJECT_ID()),
        subjectEpmCid: emptyToUndefined(audience.SUBJECT_EPM_CID()),
        subjectKeyId: emptyToUndefined(audience.SUBJECT_KEY_ID()),
      });
    }
  }
  const rules: FieldStreamRuleSummary[] = [];
  for (let i = 0; i < policy.rulesLength(); i += 1) {
    const rule = policy.RULES(i);
    if (rule) {
      rules.push({
        fieldPath: rule.FIELD_PATH(),
        fieldIdPath: Array.from(rule.fieldIdPathArray() ?? []),
        decision: enumName(fieldStreamDecisionCategory, rule.DECISION()),
        tags: stringVector(rule.tagsLength(), (index) => rule.TAGS(index)),
        requiredAttributes: stringVector(rule.requiredAttributesLength(), (index) => rule.REQUIRED_ATTRIBUTES(index)),
        keyId: emptyToUndefined(rule.KEY_ID()),
      });
    }
  }
  return {
    policyId,
    policyVersion: policy.POLICY_VERSION(),
    providerPeerId: policy.PROVIDER_PEER_ID(),
    listingId: policy.LISTING_ID(),
    streamId: policy.STREAM_ID(),
    schemaCode: policy.SCHEMA_CODE(),
    schemaHash: copyBytes(policy.schemaHashArray()),
    audiences,
    rules,
    allowedOperations: Array.from(policy.allowedOperationsArray() ?? []).map((value) =>
      enumName(fieldStreamOperationCategory, value),
    ),
    keyScope: emptyToUndefined(policy.KEY_SCOPE()),
    keyEpoch: emptyToUndefined(policy.KEY_EPOCH()),
    validFrom: zeroToUndefined(policy.VALID_FROM()),
    expiresAt: zeroToUndefined(policy.EXPIRES_AT()),
    revocationStatus: enumName(fieldStreamRevocationCategory, policy.REVOCATION_STATUS()),
    revokedAt: zeroToUndefined(policy.REVOKED_AT()),
    revocationReason: emptyToUndefined(policy.REVOCATION_REASON()),
    providerSignature: copyBytes(policy.providerSignatureArray()),
  };
}

export function decodeFieldStreamMessageSummary(bytes: Uint8Array): FieldStreamMessageSummary {
  if (bytes.length === 0) {
    throw new Error('field stream message bytes are empty');
  }
  if (!FSM.bufferHasIdentifier(new flatbuffers.ByteBuffer(bytes))) {
    throw new Error('field stream message identifier mismatch');
  }
  const message = FSM.getRootAsFSM(new flatbuffers.ByteBuffer(bytes));
  const messageId = message.MESSAGE_ID();
  if (!messageId) {
    throw new Error('field stream message id is required');
  }
  const fields: FieldStreamFieldSummary[] = [];
  for (let i = 0; i < message.fieldsLength(); i += 1) {
    const field = message.FIELDS(i);
    if (field) {
      const ciphertext = copyBytes(field.ciphertextArray());
      fields.push({
        fieldPath: field.FIELD_PATH(),
        fieldIdPath: Array.from(field.fieldIdPathArray() ?? []),
        state: enumName(fieldStreamValueStateCategory, field.STATE()),
        encoding: enumName(fieldStreamValueEncodingCategory, field.ENCODING()),
        value: copyBytes(field.valueArray()),
        ciphertext,
        nonce: copyBytes(field.nonceArray()),
        tag: copyBytes(field.tagArray()),
        keyId: emptyToUndefined(field.KEY_ID()),
        aadHash: copyBytes(field.aadHashArray()),
        releaseTags: stringVector(field.releaseTagsLength(), (index) => field.RELEASE_TAGS(index)),
        decision: emptyToUndefined(field.DECISION()),
        ciphertextLength: ciphertext?.length ?? 0,
      });
    }
  }
  return {
    messageId,
    providerPeerId: message.PROVIDER_PEER_ID(),
    listingId: message.LISTING_ID(),
    streamId: message.STREAM_ID(),
    schemaCode: message.SCHEMA_CODE(),
    schemaHash: copyBytes(message.schemaHashArray()),
    policyId: message.POLICY_ID(),
    policyVersion: message.POLICY_VERSION(),
    keyEpoch: emptyToUndefined(message.KEY_EPOCH()),
    sequence: message.SEQUENCE(),
    producedAt: zeroToUndefined(message.PRODUCED_AT()),
    expiresAt: zeroToUndefined(message.EXPIRES_AT()),
    subjectId: emptyToUndefined(message.SUBJECT_ID()),
    fields,
    payloadHash: copyBytes(message.payloadHashArray()),
    previousMessageHash: copyBytes(message.previousMessageHashArray()),
    providerSignature: copyBytes(message.providerSignatureArray()),
  };
}

export async function resolveFieldStreamMessageView(
  bytesOrSummary: Uint8Array | FieldStreamMessageSummary,
  grant: FieldStreamAccessGrant,
  options: ResolveFieldStreamMessageViewOptions = {},
): Promise<FieldStreamMessageView> {
  const message = bytesOrSummary instanceof Uint8Array
    ? decodeFieldStreamMessageSummary(bytesOrSummary)
    : bytesOrSummary;

  validateGrantForMessage(message, grant, options.now);

  const allowedFieldPaths = new Set(grant.allowedFieldPaths);
  const redactedFieldPaths = new Set(grant.redactedFieldPaths ?? []);
  const decryptField = options.decryptField ?? defaultDecryptField;
  const fields: FieldStreamResolvedField[] = [];

  for (const field of message.fields) {
    if (field.state === 'Public') {
      fields.push({
        ...baseResolvedField(field),
        visibility: 'public',
        value: cloneOptionalBytes(field.value),
        plaintext: cloneOptionalBytes(field.value),
      });
      continue;
    }

    if (field.state === 'Redacted' || redactedFieldPaths.has(field.fieldPath)) {
      fields.push({
        ...baseResolvedField(field),
        visibility: 'redacted',
        reason: field.decision ?? 'redacted',
      });
      continue;
    }

    if (field.state === 'Unavailable') {
      fields.push({
        ...baseResolvedField(field),
        visibility: 'unavailable',
        reason: field.decision ?? 'unavailable',
      });
      continue;
    }

    if (field.state !== 'Encrypted') {
      fields.push({
        ...baseResolvedField(field),
        visibility: 'unavailable',
        reason: `unsupported_field_state:${field.state}`,
      });
      continue;
    }

    if (!allowedFieldPaths.has(field.fieldPath)) {
      fields.push({
        ...baseResolvedField(field),
        visibility: 'encrypted',
        reason: 'field_not_granted',
      });
      continue;
    }

    const keyId = field.keyId;
    if (!keyId) {
      fields.push({
        ...baseResolvedField(field),
        visibility: 'encrypted',
        reason: 'missing_key_id',
      });
      continue;
    }

    const keyBytes = fieldKeyBytes(grant.fieldKeysById, keyId);
    if (!keyBytes) {
      fields.push({
        ...baseResolvedField(field),
        visibility: 'encrypted',
        reason: 'missing_field_key',
      });
      continue;
    }

    const ciphertext = requiredBytes(field.ciphertext, `field ${field.fieldPath} ciphertext`);
    const tag = requiredBytes(field.tag, `field ${field.fieldPath} authentication tag`);
    const nonce = requiredBytes(field.nonce, `field ${field.fieldPath} nonce`);
    const aad = options.aadForField?.(message, field, grant) ?? new Uint8Array(0);
    let plaintext: Uint8Array;
    try {
      plaintext = await decryptField({
        message,
        field,
        grant,
        fieldPath: field.fieldPath,
        keyId,
        keyBytes: cloneBytes(keyBytes),
        ciphertext: cloneBytes(ciphertext),
        tag: cloneBytes(tag),
        nonce: cloneBytes(nonce),
        aad: cloneBytes(aad),
      });
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error);
      throw new Error(`failed to decrypt field ${field.fieldPath}: ${reason}`);
    }

    fields.push({
      ...baseResolvedField(field),
      visibility: 'decrypted',
      plaintext: cloneBytes(plaintext),
    });
  }

  return {
    messageId: message.messageId,
    providerPeerId: message.providerPeerId,
    listingId: message.listingId,
    streamId: message.streamId,
    schemaCode: message.schemaCode,
    policyId: message.policyId,
    policyVersion: message.policyVersion,
    keyEpoch: message.keyEpoch,
    sequence: message.sequence,
    subjectId: message.subjectId,
    grantSubjectId: grant.subjectId,
    fields,
  };
}

function enumName(enumMap: GeneratedEnum, value: number | null | undefined): string {
  if (value === null || value === undefined) {
    return '';
  }
  const name = enumMap[value];
  return typeof name === 'string' ? name : String(value);
}

function stringVector(length: number, getter: (index: number) => string | Uint8Array | null): string[] {
  const values: string[] = [];
  for (let i = 0; i < length; i += 1) {
    const value = getter(i);
    if (value instanceof Uint8Array) {
      values.push(new TextDecoder().decode(value));
    } else if (value) {
      values.push(value);
    }
  }
  return values;
}

function copyBytes(bytes: Uint8Array | Int8Array | null): Uint8Array | undefined {
  if (!bytes || bytes.length === 0) {
    return undefined;
  }
  return new Uint8Array(bytes);
}

function emptyToUndefined(value: string | Uint8Array | null): string | undefined {
  if (value instanceof Uint8Array) {
    return value.length === 0 ? undefined : new TextDecoder().decode(value);
  }
  return value && value.length > 0 ? value : undefined;
}

function zeroToUndefined(value: bigint): bigint | undefined {
  return value === 0n ? undefined : value;
}

async function defaultDecryptField(input: FieldStreamDecryptFieldInput): Promise<Uint8Array> {
  const ciphertextAndTag = concatBytes(input.ciphertext, input.tag);
  return aesGcmDecryptWithIv(
    cloneBytes(input.keyBytes),
    ciphertextAndTag,
    cloneBytes(input.nonce),
    cloneBytes(input.aad),
  );
}

function validateGrantForMessage(
  message: FieldStreamMessageSummary,
  grant: FieldStreamAccessGrant,
  nowInput?: bigint | number | string | Date,
): void {
  assertEqual(message.providerPeerId, grant.providerPeerId, 'provider peer id');
  assertEqual(message.listingId, grant.listingId, 'listing id');
  assertEqual(message.streamId, grant.streamId, 'stream id');
  if (grant.schemaCode) {
    assertEqual(message.schemaCode, grant.schemaCode, 'schema code');
  }
  assertEqual(message.policyId, grant.policyId, 'policy id');
  if (message.policyVersion !== grant.policyVersion) {
    throw new Error(`field stream grant policy version mismatch: message=${message.policyVersion} grant=${grant.policyVersion}`);
  }
  if ((message.keyEpoch ?? '') !== (grant.keyEpoch ?? '')) {
    throw new Error(`field stream grant key epoch mismatch: message=${message.keyEpoch ?? ''} grant=${grant.keyEpoch ?? ''}`);
  }
  if (message.subjectId && message.subjectId !== grant.subjectId) {
    throw new Error(`field stream grant subject mismatch: message=${message.subjectId} grant=${grant.subjectId}`);
  }
  if (grant.revokedAt !== undefined && grant.revokedAt !== null) {
    throw new Error(`field stream grant revoked${grant.revocationReason ? `: ${grant.revocationReason}` : ''}`);
  }
  const expiresAt = toOptionalBigInt(grant.expiresAt);
  if (expiresAt !== undefined) {
    const now = toOptionalBigInt(nowInput) ?? BigInt(Date.now());
    if (now > expiresAt) {
      throw new Error('field stream grant expired');
    }
  }
}

function assertEqual(actual: string | undefined, expected: string | undefined, label: string): void {
  if ((actual ?? '') !== (expected ?? '')) {
    throw new Error(`field stream grant ${label} mismatch: message=${actual ?? ''} grant=${expected ?? ''}`);
  }
}

function baseResolvedField(field: FieldStreamFieldSummary): Omit<FieldStreamResolvedField, 'visibility'> {
  return {
    fieldPath: field.fieldPath,
    fieldIdPath: [...field.fieldIdPath],
    state: field.state,
    encoding: field.encoding,
    keyId: field.keyId,
    releaseTags: [...field.releaseTags],
    decision: field.decision,
    ciphertextLength: field.ciphertextLength,
  };
}

function fieldKeyBytes(keys: FieldKeyMap | undefined, keyId: string): Uint8Array | undefined {
  if (!keys) {
    return undefined;
  }
  const bytes = keys instanceof Map ? keys.get(keyId) : keys[keyId];
  return bytes && bytes.length > 0 ? bytes : undefined;
}

function requiredBytes(bytes: Uint8Array | undefined, label: string): Uint8Array {
  if (!bytes || bytes.length === 0) {
    throw new Error(`${label} missing`);
  }
  return bytes;
}

function cloneOptionalBytes(bytes: Uint8Array | undefined): Uint8Array | undefined {
  return bytes ? cloneBytes(bytes) : undefined;
}

function cloneBytes(bytes: Uint8Array): Uint8Array<ArrayBuffer> {
  return new Uint8Array(bytes);
}

function concatBytes(...chunks: Uint8Array[]): Uint8Array<ArrayBuffer> {
  const length = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const out = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

function toOptionalBigInt(value: bigint | number | string | Date | undefined): bigint | undefined {
  if (value === undefined || value === null) {
    return undefined;
  }
  if (typeof value === 'bigint') {
    return value;
  }
  if (value instanceof Date) {
    return BigInt(value.getTime());
  }
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) {
      throw new Error('field stream timestamp must be finite');
    }
    return BigInt(Math.trunc(value));
  }
  const trimmed = value.trim();
  if (/^-?\d+$/.test(trimmed)) {
    return BigInt(trimmed);
  }
  const parsed = Date.parse(trimmed);
  if (!Number.isFinite(parsed)) {
    throw new Error(`field stream timestamp is invalid: ${value}`);
  }
  return BigInt(parsed);
}
