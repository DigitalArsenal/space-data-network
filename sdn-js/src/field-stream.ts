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

import {
  decryptPublicAesGcm,
  publicSha256,
  verifyPublicEd25519Signature,
} from './crypto/public-runtime';

type GeneratedEnum = Record<string, string | number>;
type FieldKeyMap = Record<string, Uint8Array> | Map<string, Uint8Array>;
const textEncoder = new TextEncoder();

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
  grantId?: string;
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
  grantedAt?: bigint | number | string | Date;
  expiresAt?: bigint | number | string | Date;
  revokedAt?: bigint | number | string | Date;
  revocationReason?: string;
  deliveryTopic?: string;
  grantScope?: string;
  allowedOperations?: string[];
  fieldStreamPolicy?: FieldStreamGrantPolicyPayload;
  providerSignature?: Uint8Array;
  signaturePayload?: Uint8Array;
}

export interface FieldStreamGrantPolicyPayload {
  policyId?: string;
  policyVersion?: number;
  streamId?: string;
  schemaCode?: string;
  allowedFieldPaths?: string[];
  redactedFieldPaths?: string[];
  keyEpoch?: string;
  grantScope?: string;
  allowedOperations?: string[];
}

export interface FieldStreamProviderSignatureInput {
  message: FieldStreamMessageSummary;
  signature: Uint8Array;
  signaturePayload: Uint8Array;
}

export interface FieldStreamGrantSignatureInput {
  grant: FieldStreamAccessGrant;
  signature: Uint8Array;
  signaturePayload: Uint8Array;
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
  verifyProviderSignature?: (input: FieldStreamProviderSignatureInput) => Promise<boolean> | boolean;
  verifyGrantSignature?: (input: FieldStreamGrantSignatureInput) => Promise<boolean> | boolean;
  replayGuard?: FieldStreamReplayGuard;
  auditEvent?: (event: FieldStreamAuditEvent) => void;
  aadForField?: (
    message: FieldStreamMessageSummary,
    field: FieldStreamFieldSummary,
    grant: FieldStreamAccessGrant,
  ) => Uint8Array | undefined;
}

export type FieldStreamAuditEventType =
  | 'field_stream.decrypt_success'
  | 'field_stream.decrypt_denied'
  | 'field_stream.key_epoch_mismatch';

export interface FieldStreamAuditEvent {
  type: FieldStreamAuditEventType;
  messageId: string;
  providerPeerId: string;
  listingId: string;
  streamId: string;
  schemaCode: string;
  policyId: string;
  policyVersion: number;
  keyEpoch?: string;
  sequence: string;
  grantId?: string;
  grantSubjectId: string;
  fieldPath?: string;
  reason?: string;
  messageKeyEpoch?: string;
  grantKeyEpoch?: string;
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

export interface FieldStreamReplayGuard {
  check(message: FieldStreamMessageSummary): void;
  accept(message: FieldStreamMessageSummary): void;
  reset(): void;
}

export function createFieldStreamReplayGuard(): FieldStreamReplayGuard {
  const latestSequenceByScope = new Map<string, bigint>();
  return {
    check(message: FieldStreamMessageSummary): void {
      const scope = fieldStreamReplayScope(message);
      const previous = latestSequenceByScope.get(scope);
      if (previous !== undefined && message.sequence <= previous) {
        throw new Error(
          `replayed field stream sequence ${message.sequence.toString()} for ${scope}; latest accepted sequence is ${previous.toString()}`,
        );
      }
    },
    accept(message: FieldStreamMessageSummary): void {
      this.check(message);
      const scope = fieldStreamReplayScope(message);
      latestSequenceByScope.set(scope, message.sequence);
    },
    reset(): void {
      latestSequenceByScope.clear();
    },
  };
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

  if ((message.keyEpoch ?? '') !== (grant.keyEpoch ?? '')) {
    emitFieldStreamAuditEvent(options, message, grant, {
      type: 'field_stream.key_epoch_mismatch',
      reason: 'key_epoch_mismatch',
      messageKeyEpoch: message.keyEpoch ?? '',
      grantKeyEpoch: grant.keyEpoch ?? '',
    });
  }
  validateGrantForMessage(message, grant, options.now);
  await verifyMessageProviderSignature(message, options);
  await verifyAccessGrantSignature(grant, options);
  options.replayGuard?.check(message);

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
      emitFieldStreamAuditEvent(options, message, grant, {
        type: 'field_stream.decrypt_denied',
        fieldPath: field.fieldPath,
        reason: 'field_not_granted',
      });
      fields.push({
        ...baseResolvedField(field),
        visibility: 'encrypted',
        reason: 'field_not_granted',
      });
      continue;
    }

    const keyId = field.keyId;
    if (!keyId) {
      emitFieldStreamAuditEvent(options, message, grant, {
        type: 'field_stream.decrypt_denied',
        fieldPath: field.fieldPath,
        reason: 'missing_key_id',
      });
      fields.push({
        ...baseResolvedField(field),
        visibility: 'encrypted',
        reason: 'missing_key_id',
      });
      continue;
    }

    const keyBytes = fieldKeyBytes(grant.fieldKeysById, keyId);
    if (!keyBytes) {
      emitFieldStreamAuditEvent(options, message, grant, {
        type: 'field_stream.decrypt_denied',
        fieldPath: field.fieldPath,
        reason: 'missing_field_key',
      });
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
    if (options.aadForField) {
      await verifyFieldAadHash(field, aad);
    }
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
    emitFieldStreamAuditEvent(options, message, grant, {
      type: 'field_stream.decrypt_success',
      fieldPath: field.fieldPath,
    });
  }

  const view = {
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
  options.replayGuard?.accept(message);
  return view;
}

export function buildFieldStreamProviderSignaturePayload(message: FieldStreamMessageSummary): Uint8Array {
  return textEncoder.encode(JSON.stringify({
    message_id: message.messageId,
    provider_peer_id: message.providerPeerId,
    listing_id: message.listingId,
    stream_id: message.streamId,
    schema_code: message.schemaCode,
    schema_hash: bytesToHexOrNull(message.schemaHash),
    policy_id: message.policyId,
    policy_version: message.policyVersion,
    key_epoch: message.keyEpoch ?? '',
    sequence: message.sequence.toString(),
    produced_at: message.producedAt?.toString() ?? '0',
    expires_at: message.expiresAt?.toString() ?? '0',
    subject_id: message.subjectId ?? '',
    fields: message.fields.map((field) => ({
      field_path: field.fieldPath,
      field_id_path: [...field.fieldIdPath],
      state: field.state,
      encoding: field.encoding,
      value: bytesToHexOrNull(field.value),
      ciphertext: bytesToHexOrNull(field.ciphertext),
      nonce: bytesToHexOrNull(field.nonce),
      tag: bytesToHexOrNull(field.tag),
      key_id: field.keyId ?? '',
      aad_hash: bytesToHexOrNull(field.aadHash),
      release_tags: [...field.releaseTags],
      decision: field.decision ?? '',
    })),
    payload_hash: bytesToHexOrNull(message.payloadHash),
    previous_message_hash: bytesToHexOrNull(message.previousMessageHash),
  }));
}

export function buildFieldStreamGrantSignaturePayload(grant: FieldStreamAccessGrant): Uint8Array {
  return textEncoder.encode([
    grant.grantId ?? '',
    grant.listingId,
    grant.subjectId,
    grant.providerPeerId,
    unixSecondsForSignature(grant.grantedAt),
    unixSecondsForSignature(grant.expiresAt),
    grant.deliveryTopic ?? '',
    canonicalGrantFieldStreamPolicy(grant),
  ].join('\x1f'));
}

export async function verifyFieldStreamProviderSignature(
  message: FieldStreamMessageSummary,
  providerPublicKey: Uint8Array,
): Promise<boolean> {
  if (!message.providerSignature || message.providerSignature.length === 0) {
    return false;
  }
  try {
    return verifyPublicEd25519Signature(
      cloneBytes(providerPublicKey),
      buildFieldStreamProviderSignaturePayload(message),
      cloneBytes(message.providerSignature),
    );
  } catch {
    return false;
  }
}

export async function verifyFieldStreamGrantSignature(
  grant: FieldStreamAccessGrant,
  providerPublicKey: Uint8Array,
): Promise<boolean> {
  if (!grant.providerSignature || grant.providerSignature.length === 0) {
    return false;
  }
  const signaturePayload = cloneOptionalBytes(grant.signaturePayload)
    ?? buildFieldStreamGrantSignaturePayload(grant);
  try {
    return verifyPublicEd25519Signature(
      cloneBytes(providerPublicKey),
      signaturePayload,
      cloneBytes(grant.providerSignature),
    );
  } catch {
    return false;
  }
}

async function verifyMessageProviderSignature(
  message: FieldStreamMessageSummary,
  options: ResolveFieldStreamMessageViewOptions,
): Promise<void> {
  if (!options.verifyProviderSignature) {
    return;
  }
  const signature = requiredBytes(message.providerSignature, 'field stream provider signature');
  const ok = await options.verifyProviderSignature({
    message,
    signature: cloneBytes(signature),
    signaturePayload: buildFieldStreamProviderSignaturePayload(message),
  });
  if (!ok) {
    throw new Error('field stream provider signature invalid');
  }
}

async function verifyAccessGrantSignature(
  grant: FieldStreamAccessGrant,
  options: ResolveFieldStreamMessageViewOptions,
): Promise<void> {
  if (!options.verifyGrantSignature) {
    return;
  }
  const signature = requiredBytes(grant.providerSignature, 'field stream grant signature');
  const signaturePayload = cloneOptionalBytes(grant.signaturePayload)
    ?? buildFieldStreamGrantSignaturePayload(grant);
  const ok = await options.verifyGrantSignature({
    grant,
    signature: cloneBytes(signature),
    signaturePayload,
  });
  if (!ok) {
    throw new Error('field stream grant signature invalid');
  }
}

function canonicalGrantFieldStreamPolicy(grant: FieldStreamAccessGrant): string {
  const policy = grant.fieldStreamPolicy;
  const canonical = {
    policy_id: trimmed(policy?.policyId ?? grant.policyId),
    policy_version: policy?.policyVersion ?? grant.policyVersion,
    stream_id: trimmed(policy?.streamId ?? grant.streamId),
    schema_code: trimmed(policy?.schemaCode ?? grant.schemaCode),
    allowed_field_paths: grantStringVector(policy?.allowedFieldPaths ?? grant.allowedFieldPaths),
    redacted_field_paths: grantStringVector(policy?.redactedFieldPaths ?? grant.redactedFieldPaths),
    key_epoch: trimmed(policy?.keyEpoch ?? grant.keyEpoch),
    grant_scope: trimmed(policy?.grantScope ?? grant.grantScope),
    allowed_operations: grantStringVector(policy?.allowedOperations ?? grant.allowedOperations),
  };
  return JSON.stringify(canonical);
}

async function verifyFieldAadHash(field: FieldStreamFieldSummary, aad: Uint8Array): Promise<void> {
  const expected = requiredBytes(field.aadHash, `field ${field.fieldPath} aad hash`);
  const actual = publicSha256(aad);
  if (!bytesEqual(actual, expected)) {
    throw new Error(`field ${field.fieldPath} aad hash mismatch`);
  }
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
  return decryptPublicAesGcm(
    cloneBytes(input.keyBytes),
    ciphertextAndTag,
    cloneBytes(input.nonce),
    cloneBytes(input.aad),
  );
}

function emitFieldStreamAuditEvent(
  options: ResolveFieldStreamMessageViewOptions,
  message: FieldStreamMessageSummary,
  grant: FieldStreamAccessGrant,
  event: Pick<FieldStreamAuditEvent, 'type'> & Partial<FieldStreamAuditEvent>,
): void {
  options.auditEvent?.({
    type: event.type,
    messageId: message.messageId,
    providerPeerId: message.providerPeerId,
    listingId: message.listingId,
    streamId: message.streamId,
    schemaCode: message.schemaCode,
    policyId: message.policyId,
    policyVersion: message.policyVersion,
    keyEpoch: message.keyEpoch,
    sequence: message.sequence.toString(),
    grantId: grant.grantId,
    grantSubjectId: grant.subjectId,
    fieldPath: event.fieldPath,
    reason: event.reason,
    messageKeyEpoch: event.messageKeyEpoch,
    grantKeyEpoch: event.grantKeyEpoch,
  });
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
  const now = toOptionalBigInt(nowInput) ?? BigInt(Date.now());
  const expiresAt = toOptionalBigInt(grant.expiresAt);
  if (expiresAt !== undefined && now > expiresAt) {
    throw new Error('field stream grant expired');
  }
  if (message.expiresAt !== undefined && now > message.expiresAt) {
    throw new Error('field stream message expired');
  }
}

function assertEqual(actual: string | undefined, expected: string | undefined, label: string): void {
  if ((actual ?? '') !== (expected ?? '')) {
    throw new Error(`field stream grant ${label} mismatch: message=${actual ?? ''} grant=${expected ?? ''}`);
  }
}

function fieldStreamReplayScope(message: FieldStreamMessageSummary): string {
  return [
    message.providerPeerId,
    message.listingId,
    message.streamId,
    message.schemaCode,
    message.policyId,
    String(message.policyVersion),
    message.keyEpoch ?? '',
    message.subjectId ?? '*',
  ].join('\x1f');
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

function bytesEqual(left: Uint8Array, right: Uint8Array): boolean {
  if (left.length !== right.length) {
    return false;
  }
  let diff = 0;
  for (let i = 0; i < left.length; i += 1) {
    diff |= left[i] ^ right[i];
  }
  return diff === 0;
}

function bytesToHexOrNull(bytes: Uint8Array | undefined): string | null {
  if (!bytes || bytes.length === 0) {
    return null;
  }
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}

function grantStringVector(values: string[] | undefined): string[] | null {
  if (!values || values.length === 0) {
    return null;
  }
  return values
    .map((value) => value.trim())
    .filter((value) => value.length > 0);
}

function trimmed(value: string | undefined): string {
  return value?.trim() ?? '';
}

function unixSecondsForSignature(value: bigint | number | string | Date | undefined): string {
  if (value === undefined || value === null) {
    return '0';
  }
  if (value instanceof Date) {
    return String(Math.trunc(value.getTime() / 1000));
  }
  let numeric: bigint;
  if (typeof value === 'bigint') {
    numeric = value;
  } else if (typeof value === 'number') {
    if (!Number.isFinite(value)) {
      throw new Error('field stream signature timestamp must be finite');
    }
    numeric = BigInt(Math.trunc(value));
  } else {
    const trimmedValue = value.trim();
    if (/^-?\d+$/.test(trimmedValue)) {
      numeric = BigInt(trimmedValue);
    } else {
      const parsed = Date.parse(trimmedValue);
      if (!Number.isFinite(parsed)) {
        throw new Error(`field stream signature timestamp is invalid: ${value}`);
      }
      return String(Math.trunc(parsed / 1000));
    }
  }
  const absolute = numeric < 0n ? -numeric : numeric;
  return (absolute > 10_000_000_000n ? numeric / 1000n : numeric).toString();
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
