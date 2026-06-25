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

type GeneratedEnum = Record<string, string | number>;

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
