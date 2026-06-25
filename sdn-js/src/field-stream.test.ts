import { describe, expect, it } from 'vitest';
import * as flatbuffers from 'flatbuffers';
import {
  FSP,
  FSPT,
  FieldStreamAudienceT,
  FieldStreamRuleT,
  fieldStreamAudienceCategory,
  fieldStreamDecisionCategory,
  fieldStreamOperationCategory,
  fieldStreamRevocationCategory,
} from 'spacedatastandards.org/lib/js/FSP/main.js';
import {
  FSM,
  FSMT,
  FieldStreamValueT,
  fieldStreamValueEncodingCategory,
  fieldStreamValueStateCategory,
} from 'spacedatastandards.org/lib/js/FSM/main.js';

import {
  decodeFieldStreamMessageSummary,
  decodeFieldStreamPolicySummary,
} from './field-stream';

const textEncoder = new TextEncoder();

describe('field stream marketplace envelopes', () => {
  it('summarizes customer-specific FSP policy rules', () => {
    const summary = decodeFieldStreamPolicySummary(encodePolicyFixture());

    expect(summary.policyId).toBe('policy-mpe-alpha');
    expect(summary.providerPeerId).toBe('provider-peer');
    expect(summary.listingId).toBe('listing-maneuver-ephemeris');
    expect(summary.streamId).toBe('maneuver-ephemeris-live');
    expect(summary.schemaCode).toBe('MPE');
    expect(summary.policyVersion).toBe(3);
    expect(summary.keyEpoch).toBe('epoch-7');
    expect(summary.revocationStatus).toBe('Active');
    expect(summary.audiences).toEqual([
      {
        audienceType: 'Customer',
        subjectId: 'customer-alpha-peer',
        subjectEpmCid: 'bafy-alpha-epm',
        subjectKeyId: 'x25519:alpha:2026-06-25',
      },
    ]);
    expect(summary.allowedOperations).toEqual(['Subscribe', 'Decrypt']);
    expect(summary.rules).toMatchObject([
      {
        fieldPath: 'object_id',
        fieldIdPath: [1],
        decision: 'AllowPublic',
        tags: ['releasable'],
      },
      {
        fieldPath: 'position',
        fieldIdPath: [3],
        decision: 'AllowEncrypted',
        tags: ['restricted', 'orbital-state'],
        requiredAttributes: ['customer=alpha', 'enclave=secret'],
        keyId: 'field-key:alpha:position:epoch-7',
      },
    ]);
    expect(summary.providerSignature?.length).toBe(64);
  });

  it('summarizes explicit FSM field visibility states', () => {
    const summary = decodeFieldStreamMessageSummary(encodeMessageFixture());

    expect(summary.messageId).toBe('fsm-mpe-alpha-000001');
    expect(summary.policyId).toBe('policy-mpe-alpha');
    expect(summary.policyVersion).toBe(3);
    expect(summary.keyEpoch).toBe('epoch-7');
    expect(summary.sequence).toBe(1n);
    expect(summary.subjectId).toBe('customer-alpha-peer');
    expect(summary.fields).toHaveLength(3);
    expect(summary.fields[0]).toMatchObject({
      fieldPath: 'object_id',
      fieldIdPath: [1],
      state: 'Public',
      encoding: 'TextUtf8',
      decision: 'allow-public',
      releaseTags: ['releasable'],
      ciphertextLength: 0,
    });
    expect(new TextDecoder().decode(summary.fields[0].value)).toBe('SAT-042');
    expect(summary.fields[1]).toMatchObject({
      fieldPath: 'position',
      fieldIdPath: [3],
      state: 'Encrypted',
      encoding: 'FlatBuffer',
      keyId: 'field-key:alpha:position:epoch-7',
      decision: 'allow-encrypted',
      releaseTags: ['restricted', 'customer-alpha'],
      ciphertextLength: 4,
    });
    expect(Array.from(summary.fields[1].ciphertext ?? [])).toEqual([0xde, 0xad, 0xbe, 0xef]);
    expect(summary.fields[2]).toMatchObject({
      fieldPath: 'maneuver_plan',
      state: 'Redacted',
      decision: 'redacted:not-granted',
      ciphertextLength: 0,
    });
    expect(summary.payloadHash?.length).toBe(32);
    expect(summary.previousMessageHash?.length).toBe(32);
    expect(summary.providerSignature?.length).toBe(64);
  });

  it('rejects buffers with the wrong field stream identifier', () => {
    expect(() => decodeFieldStreamPolicySummary(encodeMessageFixture())).toThrow(/identifier mismatch/i);
    expect(() => decodeFieldStreamMessageSummary(encodePolicyFixture())).toThrow(/identifier mismatch/i);
  });
});

function encodePolicyFixture(): Uint8Array {
  const policy = new FSPT(
    'policy-mpe-alpha',
    3,
    'provider-peer',
    'listing-maneuver-ephemeris',
    'maneuver-ephemeris-live',
    'MPE',
    Array.from(new Uint8Array(32).fill(0x61)),
    [
      new FieldStreamAudienceT(
        fieldStreamAudienceCategory.Customer,
        'customer-alpha-peer',
        'bafy-alpha-epm',
        'x25519:alpha:2026-06-25',
      ),
    ],
    [
      new FieldStreamRuleT(
        'object_id',
        [1],
        fieldStreamDecisionCategory.AllowPublic,
        ['releasable'],
        [],
        null,
      ),
      new FieldStreamRuleT(
        'position',
        [3],
        fieldStreamDecisionCategory.AllowEncrypted,
        ['restricted', 'orbital-state'],
        ['customer=alpha', 'enclave=secret'],
        'field-key:alpha:position:epoch-7',
      ),
    ],
    [fieldStreamOperationCategory.Subscribe, fieldStreamOperationCategory.Decrypt],
    'stream:listing-maneuver-ephemeris:maneuver-ephemeris-live',
    'epoch-7',
    1_800_000_000_000n,
    1_800_086_400_000n,
    fieldStreamRevocationCategory.Active,
    0n,
    null,
    Array.from(new Uint8Array(64).fill(0xa1)),
  );
  const builder = new flatbuffers.Builder(1024);
  const root = policy.pack(builder);
  FSP.finishFSPBuffer(builder, root);
  return builder.asUint8Array();
}

function encodeMessageFixture(): Uint8Array {
  const message = new FSMT(
    'fsm-mpe-alpha-000001',
    'provider-peer',
    'listing-maneuver-ephemeris',
    'maneuver-ephemeris-live',
    'MPE',
    Array.from(new Uint8Array(32).fill(0x61)),
    'policy-mpe-alpha',
    3,
    'epoch-7',
    1n,
    1_800_000_100_000n,
    1_800_000_160_000n,
    'customer-alpha-peer',
    [
      new FieldStreamValueT(
        'object_id',
        [1],
        fieldStreamValueStateCategory.Public,
        fieldStreamValueEncodingCategory.TextUtf8,
        Array.from(textEncoder.encode('SAT-042')),
        [],
        [],
        [],
        null,
        [],
        ['releasable'],
        'allow-public',
      ),
      new FieldStreamValueT(
        'position',
        [3],
        fieldStreamValueStateCategory.Encrypted,
        fieldStreamValueEncodingCategory.FlatBuffer,
        [],
        [0xde, 0xad, 0xbe, 0xef],
        Array.from(new Uint8Array(12).fill(0x21)),
        Array.from(new Uint8Array(16).fill(0x22)),
        'field-key:alpha:position:epoch-7',
        Array.from(new Uint8Array(32).fill(0x23)),
        ['restricted', 'customer-alpha'],
        'allow-encrypted',
      ),
      new FieldStreamValueT(
        'maneuver_plan',
        [7],
        fieldStreamValueStateCategory.Redacted,
        fieldStreamValueEncodingCategory.JsonUtf8,
        [],
        [],
        [],
        [],
        null,
        [],
        ['maneuver', 'not-granted'],
        'redacted:not-granted',
      ),
    ],
    Array.from(new Uint8Array(32).fill(0x31)),
    Array.from(new Uint8Array(32).fill(0x30)),
    Array.from(new Uint8Array(64).fill(0xa1)),
  );
  const builder = new flatbuffers.Builder(1024);
  const root = message.pack(builder);
  FSM.finishFSMBuffer(builder, root);
  return builder.asUint8Array();
}
