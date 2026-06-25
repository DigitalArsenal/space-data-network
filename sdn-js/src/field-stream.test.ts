import { describe, expect, it, vi } from 'vitest';
import { createHash } from 'node:crypto';
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
  buildFieldStreamGrantSignaturePayload,
  buildFieldStreamProviderSignaturePayload,
  createFieldStreamReplayGuard,
  decodeFieldStreamMessageSummary,
  decodeFieldStreamPolicySummary,
  resolveFieldStreamMessageView,
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

  it('resolves the same FSM envelope into customer-specific decrypted field views', async () => {
    const messageBytes = encodeMessageFixture({
      subjectId: null,
      encryptedFields: [
        {
          fieldPath: 'position',
          fieldIdPath: [3],
          keyId: 'field-key:position:epoch-7',
          ciphertext: [0x10, 0x11, 0x12],
          decision: 'allow-encrypted',
          releaseTags: ['restricted', 'orbital-state'],
        },
        {
          fieldPath: 'covariance_detail',
          fieldIdPath: [4],
          keyId: 'field-key:covariance:epoch-7',
          ciphertext: [0x20, 0x21, 0x22],
          decision: 'allow-encrypted',
          releaseTags: ['restricted', 'covariance'],
        },
      ],
    });
    const decryptField = vi.fn(async ({ fieldPath, keyBytes }: { fieldPath: string; keyBytes: Uint8Array }) => {
      return textEncoder.encode(`${fieldPath}:${Array.from(keyBytes).join('.')}`);
    });

    const customerA = await resolveFieldStreamMessageView(messageBytes, {
      subjectId: 'customer-alpha-peer',
      providerPeerId: 'provider-peer',
      listingId: 'listing-maneuver-ephemeris',
      streamId: 'maneuver-ephemeris-live',
      schemaCode: 'MPE',
      policyId: 'policy-mpe-alpha',
      policyVersion: 3,
      keyEpoch: 'epoch-7',
      allowedFieldPaths: ['position'],
      fieldKeysById: {
        'field-key:position:epoch-7': new Uint8Array([0xa1]),
      },
    }, { decryptField });

    const customerB = await resolveFieldStreamMessageView(messageBytes, {
      subjectId: 'customer-beta-peer',
      providerPeerId: 'provider-peer',
      listingId: 'listing-maneuver-ephemeris',
      streamId: 'maneuver-ephemeris-live',
      schemaCode: 'MPE',
      policyId: 'policy-mpe-alpha',
      policyVersion: 3,
      keyEpoch: 'epoch-7',
      allowedFieldPaths: ['covariance_detail'],
      fieldKeysById: new Map([
        ['field-key:covariance:epoch-7', new Uint8Array([0xb2])],
      ]),
    }, { decryptField });

    expect(customerA.fields.map((field) => [field.fieldPath, field.visibility])).toEqual([
      ['object_id', 'public'],
      ['position', 'decrypted'],
      ['covariance_detail', 'encrypted'],
      ['maneuver_plan', 'redacted'],
    ]);
    expect(new TextDecoder().decode(customerA.fields[1].plaintext)).toBe('position:161');
    expect(customerA.fields[2].plaintext).toBeUndefined();
    expect(customerA.fields[2].reason).toBe('field_not_granted');

    expect(customerB.fields.map((field) => [field.fieldPath, field.visibility])).toEqual([
      ['object_id', 'public'],
      ['position', 'encrypted'],
      ['covariance_detail', 'decrypted'],
      ['maneuver_plan', 'redacted'],
    ]);
    expect(new TextDecoder().decode(customerB.fields[2].plaintext)).toBe('covariance_detail:178');
    expect(customerB.fields[1].plaintext).toBeUndefined();
    expect(customerB.fields[1].reason).toBe('field_not_granted');
  });

  it('keeps unauthorized observers locked out of encrypted fields without invoking decrypt', async () => {
    const decryptField = vi.fn(async () => textEncoder.encode('should-not-run'));

    const observer = await resolveFieldStreamMessageView(encodeMessageFixture({ subjectId: null }), {
      subjectId: 'observer-peer',
      providerPeerId: 'provider-peer',
      listingId: 'listing-maneuver-ephemeris',
      streamId: 'maneuver-ephemeris-live',
      schemaCode: 'MPE',
      policyId: 'policy-mpe-alpha',
      policyVersion: 3,
      keyEpoch: 'epoch-7',
      allowedFieldPaths: [],
      fieldKeysById: {},
    }, { decryptField });

    expect(observer.fields.filter((field) => field.visibility === 'decrypted')).toEqual([]);
    expect(observer.fields.find((field) => field.fieldPath === 'position')).toMatchObject({
      visibility: 'encrypted',
      reason: 'field_not_granted',
    });
    expect(decryptField).not.toHaveBeenCalled();
  });

  it('rejects grants that do not match envelope policy and key epoch metadata', async () => {
    await expect(resolveFieldStreamMessageView(encodeMessageFixture(), {
      subjectId: 'customer-alpha-peer',
      providerPeerId: 'provider-peer',
      listingId: 'listing-maneuver-ephemeris',
      streamId: 'maneuver-ephemeris-live',
      schemaCode: 'MPE',
      policyId: 'policy-mpe-alpha',
      policyVersion: 2,
      keyEpoch: 'epoch-7',
      allowedFieldPaths: ['position'],
      fieldKeysById: {
        'field-key:alpha:position:epoch-7': new Uint8Array([0xa1]),
      },
    })).rejects.toThrow(/policy version/i);

    await expect(resolveFieldStreamMessageView(encodeMessageFixture(), {
      subjectId: 'customer-alpha-peer',
      providerPeerId: 'provider-peer',
      listingId: 'listing-maneuver-ephemeris',
      streamId: 'maneuver-ephemeris-live',
      schemaCode: 'MPE',
      policyId: 'policy-mpe-alpha',
      policyVersion: 3,
      keyEpoch: 'epoch-6',
      allowedFieldPaths: ['position'],
      fieldKeysById: {
        'field-key:alpha:position:epoch-7': new Uint8Array([0xa1]),
      },
    })).rejects.toThrow(/key epoch/i);
  });

  it('fails closed when an authorized encrypted field cannot be decrypted', async () => {
    await expect(resolveFieldStreamMessageView(encodeMessageFixture(), {
      subjectId: 'customer-alpha-peer',
      providerPeerId: 'provider-peer',
      listingId: 'listing-maneuver-ephemeris',
      streamId: 'maneuver-ephemeris-live',
      schemaCode: 'MPE',
      policyId: 'policy-mpe-alpha',
      policyVersion: 3,
      keyEpoch: 'epoch-7',
      allowedFieldPaths: ['position'],
      fieldKeysById: {
        'field-key:alpha:position:epoch-7': new Uint8Array([0xa1]),
      },
    }, {
      decryptField: async () => {
        throw new Error('authentication failed');
      },
    })).rejects.toThrow(/failed to decrypt field position/i);
  });

  it('rejects replayed or out-of-order FSM sequences before decrypting fields', async () => {
    const replayGuard = createFieldStreamReplayGuard();
    const decryptField = vi.fn(async () => textEncoder.encode('decrypted-position'));
    const grant = {
      subjectId: 'customer-alpha-peer',
      providerPeerId: 'provider-peer',
      listingId: 'listing-maneuver-ephemeris',
      streamId: 'maneuver-ephemeris-live',
      schemaCode: 'MPE',
      policyId: 'policy-mpe-alpha',
      policyVersion: 3,
      keyEpoch: 'epoch-7',
      allowedFieldPaths: ['position'],
      fieldKeysById: {
        'field-key:alpha:position:epoch-7': new Uint8Array([0xa1]),
      },
    };

    await resolveFieldStreamMessageView(
      encodeMessageFixture({ sequence: 1n }),
      grant,
      { decryptField, replayGuard },
    );
    await expect(resolveFieldStreamMessageView(
      encodeMessageFixture({ sequence: 1n }),
      grant,
      { decryptField, replayGuard },
    )).rejects.toThrow(/replayed field stream sequence/i);
    await expect(resolveFieldStreamMessageView(
      encodeMessageFixture({ sequence: 0n }),
      grant,
      { decryptField, replayGuard },
    )).rejects.toThrow(/replayed field stream sequence/i);

    await resolveFieldStreamMessageView(
      encodeMessageFixture({ sequence: 2n }),
      grant,
      { decryptField, replayGuard },
    );
    expect(decryptField).toHaveBeenCalledTimes(2);
  });

  it('rejects expired grants and expired FSM envelopes before decrypting fields', async () => {
    const decryptField = vi.fn(async () => textEncoder.encode('decrypted-position'));
    const grant = {
      subjectId: 'customer-alpha-peer',
      providerPeerId: 'provider-peer',
      listingId: 'listing-maneuver-ephemeris',
      streamId: 'maneuver-ephemeris-live',
      schemaCode: 'MPE',
      policyId: 'policy-mpe-alpha',
      policyVersion: 3,
      keyEpoch: 'epoch-7',
      allowedFieldPaths: ['position'],
      fieldKeysById: {
        'field-key:alpha:position:epoch-7': new Uint8Array([0xa1]),
      },
    };

    await expect(resolveFieldStreamMessageView(
      encodeMessageFixture(),
      { ...grant, expiresAt: 1_800_000_000_000n },
      { decryptField, now: 1_800_000_000_001n },
    )).rejects.toThrow(/grant expired/i);

    await expect(resolveFieldStreamMessageView(
      encodeMessageFixture(),
      { ...grant, revokedAt: 1_800_000_000_000n, revocationReason: 'key epoch rotated' },
      { decryptField, now: 1_800_000_000_001n },
    )).rejects.toThrow(/grant revoked: key epoch rotated/i);

    await expect(resolveFieldStreamMessageView(
      encodeMessageFixture({ expiresAt: 1_800_000_150_000n }),
      grant,
      { decryptField, now: 1_800_000_150_001n },
    )).rejects.toThrow(/message expired/i);

    expect(decryptField).not.toHaveBeenCalled();
  });

  it('does not advance replay state when field decryption fails', async () => {
    const replayGuard = createFieldStreamReplayGuard();
    const grant = {
      subjectId: 'customer-alpha-peer',
      providerPeerId: 'provider-peer',
      listingId: 'listing-maneuver-ephemeris',
      streamId: 'maneuver-ephemeris-live',
      schemaCode: 'MPE',
      policyId: 'policy-mpe-alpha',
      policyVersion: 3,
      keyEpoch: 'epoch-7',
      allowedFieldPaths: ['position'],
      fieldKeysById: {
        'field-key:alpha:position:epoch-7': new Uint8Array([0xa1]),
      },
    };

    await expect(resolveFieldStreamMessageView(
      encodeMessageFixture({ sequence: 9n }),
      grant,
      {
        replayGuard,
        decryptField: async () => {
          throw new Error('authentication failed');
        },
      },
    )).rejects.toThrow(/failed to decrypt field position/i);

    const recovered = await resolveFieldStreamMessageView(
      encodeMessageFixture({ sequence: 9n }),
      grant,
      {
        replayGuard,
        decryptField: async () => textEncoder.encode('decrypted-position'),
      },
    );
    expect(recovered.sequence).toBe(9n);
    expect(recovered.fields.find((field) => field.fieldPath === 'position')).toMatchObject({
      visibility: 'decrypted',
    });
  });

  it('rejects invalid provider signatures before decrypting fields', async () => {
    const decryptField = vi.fn(async () => textEncoder.encode('decrypted-position'));
    const verifyProviderSignature = vi.fn(async () => false);

    await expect(resolveFieldStreamMessageView(
      encodeMessageFixture(),
      customerAlphaGrant(),
      { decryptField, verifyProviderSignature },
    )).rejects.toThrow(/provider signature/i);

    expect(verifyProviderSignature).toHaveBeenCalledTimes(1);
    expect(decryptField).not.toHaveBeenCalled();
  });

  it('rejects invalid grant signatures before decrypting fields', async () => {
    const decryptField = vi.fn(async () => textEncoder.encode('decrypted-position'));
    const verifyGrantSignature = vi.fn(async () => false);

    await expect(resolveFieldStreamMessageView(
      encodeMessageFixture(),
      {
        ...customerAlphaGrant(),
        providerSignature: new Uint8Array(64).fill(0x9a),
        signaturePayload: textEncoder.encode('grant-signature-payload'),
      },
      { decryptField, verifyGrantSignature },
    )).rejects.toThrow(/grant signature/i);

    expect(verifyGrantSignature).toHaveBeenCalledTimes(1);
    expect(decryptField).not.toHaveBeenCalled();
  });

  it('passes authenticated field AAD into decrypt only when the AAD hash matches', async () => {
    const aad = textEncoder.encode('field-aad:position:epoch-7');
    const decryptField = vi.fn(async ({ aad: passedAad }: { aad: Uint8Array }) => {
      expect(new TextDecoder().decode(passedAad)).toBe('field-aad:position:epoch-7');
      return textEncoder.encode('decrypted-position');
    });

    const view = await resolveFieldStreamMessageView(
      encodeMessageFixture({
        encryptedFields: [{
          fieldPath: 'position',
          fieldIdPath: [3],
          keyId: 'field-key:alpha:position:epoch-7',
          ciphertext: [0xde, 0xad, 0xbe, 0xef],
          aadHash: sha256Fixture(aad),
          decision: 'allow-encrypted',
          releaseTags: ['restricted', 'customer-alpha'],
        }],
      }),
      customerAlphaGrant(),
      {
        decryptField,
        aadForField: () => aad,
      },
    );

    expect(view.fields.find((field) => field.fieldPath === 'position')).toMatchObject({
      visibility: 'decrypted',
    });
    expect(decryptField).toHaveBeenCalledTimes(1);
  });

  it('rejects field AAD hash mismatches before decrypting fields', async () => {
    const decryptField = vi.fn(async () => textEncoder.encode('decrypted-position'));

    await expect(resolveFieldStreamMessageView(
      encodeMessageFixture({
        encryptedFields: [{
          fieldPath: 'position',
          fieldIdPath: [3],
          keyId: 'field-key:alpha:position:epoch-7',
          ciphertext: [0xde, 0xad, 0xbe, 0xef],
          aadHash: sha256Fixture(textEncoder.encode('expected-field-aad')),
          decision: 'allow-encrypted',
          releaseTags: ['restricted', 'customer-alpha'],
        }],
      }),
      customerAlphaGrant(),
      {
        decryptField,
        aadForField: () => textEncoder.encode('tampered-field-aad'),
      },
    )).rejects.toThrow(/aad hash/i);

    expect(decryptField).not.toHaveBeenCalled();
  });

  it('builds a stable provider signature payload that excludes only the provider signature', () => {
    const message = decodeFieldStreamMessageSummary(encodeMessageFixture());
    const payload = buildFieldStreamProviderSignaturePayload(message);
    const parsed = JSON.parse(new TextDecoder().decode(payload));

    expect(parsed.provider_signature).toBeUndefined();
    expect(parsed).toMatchObject({
      message_id: 'fsm-mpe-alpha-000001',
      provider_peer_id: 'provider-peer',
      listing_id: 'listing-maneuver-ephemeris',
      stream_id: 'maneuver-ephemeris-live',
      schema_code: 'MPE',
      policy_id: 'policy-mpe-alpha',
      policy_version: 3,
      key_epoch: 'epoch-7',
      sequence: '1',
      subject_id: 'customer-alpha-peer',
    });
    expect(parsed.fields[1]).toMatchObject({
      field_path: 'position',
      ciphertext: 'deadbeef',
      aad_hash: '2323232323232323232323232323232323232323232323232323232323232323',
    });

    const changedSignaturePayload = buildFieldStreamProviderSignaturePayload({
      ...message,
      providerSignature: new Uint8Array(64).fill(0xff),
    });
    expect(new TextDecoder().decode(changedSignaturePayload)).toBe(new TextDecoder().decode(payload));

    const changedCiphertextPayload = buildFieldStreamProviderSignaturePayload({
      ...message,
      fields: message.fields.map((field) => field.fieldPath === 'position'
        ? { ...field, ciphertext: new Uint8Array([0xde, 0xad, 0xbe, 0x00]), ciphertextLength: 4 }
        : field),
    });
    expect(new TextDecoder().decode(changedCiphertextPayload)).not.toBe(new TextDecoder().decode(payload));
  });

  it('builds grant signature payloads matching the storefront field policy binding', () => {
    const payload = new TextDecoder().decode(buildFieldStreamGrantSignaturePayload({
      ...customerAlphaGrant(),
      grantId: 'grant-alpha-1',
      grantedAt: new Date(1_800_000_100_000),
      expiresAt: new Date(1_800_000_160_000),
      deliveryTopic: '/space-data-network/field-stream/listing-maneuver-ephemeris/customer-alpha-peer',
      redactedFieldPaths: ['maneuver_plan'],
      grantScope: 'stream:listing-maneuver-ephemeris:maneuver-ephemeris-live',
      allowedOperations: ['Subscribe', 'Decrypt'],
    }));

    expect(payload).toBe([
      'grant-alpha-1',
      'listing-maneuver-ephemeris',
      'customer-alpha-peer',
      'provider-peer',
      '1800000100',
      '1800000160',
      '/space-data-network/field-stream/listing-maneuver-ephemeris/customer-alpha-peer',
      '{"policy_id":"policy-mpe-alpha","policy_version":3,"stream_id":"maneuver-ephemeris-live","schema_code":"MPE","allowed_field_paths":["position"],"redacted_field_paths":["maneuver_plan"],"key_epoch":"epoch-7","grant_scope":"stream:listing-maneuver-ephemeris:maneuver-ephemeris-live","allowed_operations":["Subscribe","Decrypt"]}',
    ].join('\x1f'));
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

interface EncryptedFieldFixture {
  fieldPath: string;
  fieldIdPath: number[];
  keyId: string;
  ciphertext: number[];
  aadHash?: number[];
  decision: string;
  releaseTags: string[];
}

function encodeMessageFixture(options: {
  subjectId?: string | null;
  encryptedFields?: EncryptedFieldFixture[];
  sequence?: bigint;
  expiresAt?: bigint;
} = {}): Uint8Array {
  const encryptedFields = options.encryptedFields ?? [
    {
      fieldPath: 'position',
      fieldIdPath: [3],
      keyId: 'field-key:alpha:position:epoch-7',
      ciphertext: [0xde, 0xad, 0xbe, 0xef],
      decision: 'allow-encrypted',
      releaseTags: ['restricted', 'customer-alpha'],
    },
  ];
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
    options.sequence ?? 1n,
    1_800_000_100_000n,
    options.expiresAt ?? 1_800_000_160_000n,
    options.subjectId === null ? null : options.subjectId ?? 'customer-alpha-peer',
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
      ...encryptedFields.map((field) => new FieldStreamValueT(
        field.fieldPath,
        field.fieldIdPath,
        fieldStreamValueStateCategory.Encrypted,
        fieldStreamValueEncodingCategory.FlatBuffer,
        [],
        field.ciphertext,
        Array.from(new Uint8Array(12).fill(0x21)),
        Array.from(new Uint8Array(16).fill(0x22)),
        field.keyId,
        field.aadHash ?? Array.from(new Uint8Array(32).fill(0x23)),
        field.releaseTags,
        field.decision,
      )),
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

function customerAlphaGrant() {
  return {
    subjectId: 'customer-alpha-peer',
    providerPeerId: 'provider-peer',
    listingId: 'listing-maneuver-ephemeris',
    streamId: 'maneuver-ephemeris-live',
    schemaCode: 'MPE',
    policyId: 'policy-mpe-alpha',
    policyVersion: 3,
    keyEpoch: 'epoch-7',
    allowedFieldPaths: ['position'],
    fieldKeysById: {
      'field-key:alpha:position:epoch-7': new Uint8Array([0xa1]),
    },
  };
}

function sha256Fixture(bytes: Uint8Array): number[] {
  return Array.from(createHash('sha256').update(bytes).digest());
}
