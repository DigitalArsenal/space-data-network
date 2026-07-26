/*
 * Unit coverage for the dashboard's operator surfaces (graph task
 * nst-node-edit-permissions-ui): the sign-in wire encodings, the THIS NODE
 * profile form serialization, and the permission gating. Every expectation
 * here is quoted from graph/tasks/nst-node-admin-contract.md
 * "## Contract (final)" — if one of these fails, the UI and the node
 * disagree about the wire.
 */
import { describe, expect, it } from 'vitest';

import {
  accountPaths,
  toHex,
  fromHex,
  decodeChallengeB64,
  challengeRequestBody,
  verifyRequestBody,
  attestationPreimage,
  rfc3339Seconds,
  xpubFingerprint,
  fingerprintMatches,
  shortFingerprint,
  WALLET_ENTRY,
} from './wallet.js';
import {
  PROFILE_FIELDS,
  ADDRESS_FIELDS,
  emptyProfileForm,
  profileFormFromJson,
  profileFormToBody,
  profileFormDirty,
  parseAlternateNames,
} from './profile.js';
import {
  canEditNodeProfile,
  canManagePermissions,
  canAttest,
  assignableUserTiers,
  assignablePeerTiers,
  tiersHighestFirst,
  classifyPeerInput,
  peerFromVCardText,
  buildAddPeerBody,
  buildAddUserBody,
  userNeedsKeyProof,
} from './permissions.js';

describe('wallet wire encodings (contract §1–§4, §7)', () => {
  it('derives the contract §2 paths for account N (hardened, SLIP-10)', () => {
    expect(accountPaths(0)).toEqual({
      identity: "m/44'/0'/0'",
      signing: "m/44'/0'/0'/0'/0'",
      encryption: "m/44'/0'/0'/1'/0'",
    });
    expect(accountPaths(7).signing).toBe("m/44'/0'/7'/0'/0'");
    // Garbage account indices fall back to 0 rather than building a bad path.
    expect(accountPaths(-3).identity).toBe("m/44'/0'/0'");
    expect(accountPaths(1.5).identity).toBe("m/44'/0'/0'");
  });

  it('loads the wallet from the node itself, never a CDN (§1)', () => {
    expect(WALLET_ENTRY).toBe('/wallet-wasm/runtime/index.mjs');
    expect(WALLET_ENTRY.startsWith('/')).toBe(true);
  });

  it('round-trips hex', () => {
    const bytes = Uint8Array.from([0x00, 0x0d, 0x80, 0xff]);
    expect(toHex(bytes)).toBe('000d80ff');
    expect(Array.from(fromHex('0x000D80FF'))).toEqual([0, 13, 128, 255]);
    expect(fromHex('abc')).toBeNull();
    expect(fromHex('zz')).toBeNull();
  });

  it('decodes an UNPADDED RawStdEncoding challenge (§4 gotcha)', () => {
    const raw = Uint8Array.from({ length: 32 }, (_, i) => i);
    const unpadded = Buffer.from(raw).toString('base64').replace(/=+$/, '');
    expect(unpadded).toHaveLength(43);
    expect(unpadded.endsWith('=')).toBe(false);
    expect(Array.from(decodeChallengeB64(unpadded))).toEqual(Array.from(raw));
    expect(decodeChallengeB64('')).toBeNull();
  });

  it('echoes the challenge back VERBATIM and unpadded (§4 gotcha)', () => {
    const challenge = 'AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8';
    const body = verifyRequestBody({
      challengeId: 'ff00',
      clientPubKeyHex: '0D80E1FD',
      challenge,
      signatureHex: 'AABB',
      xpub: ' xpub661 ',
    });
    expect(body.challenge).toBe(challenge);
    expect(body.challenge).not.toContain('=');
    expect(body.client_pubkey_hex).toBe('0d80e1fd');
    expect(body.signature_hex).toBe('aabb');
    expect(body.xpub).toBe('xpub661');
  });

  it('sends ts in SECONDS and always carries the xpub (§4, §5.1)', () => {
    const body = challengeRequestBody({
      clientPubKeyHex: 'AB',
      xpub: 'xpub661MyMwAqRbcF',
      nowMs: 1_785_000_000_123,
    });
    expect(body.ts).toBe(1_785_000_000);
    expect(body.xpub).toBe('xpub661MyMwAqRbcF');
    expect(body.client_pubkey_hex).toBe('ab');
    // No xpub to send ⇒ the key is simply absent, never an empty string
    // (the node caps it at 256 chars and resolves by signing key instead).
    expect('xpub' in challengeRequestBody({ clientPubKeyHex: 'ab', xpub: '' })).toBe(false);
  });

  it('builds the length-prefixed attestation preimage (§7, big-endian)', () => {
    const nonce = Uint8Array.from([0xde, 0xad]);
    const pre = attestationPreimage({
      xpub: 'AB',
      claim: 'self',
      issuedAtSeconds: 2,
      nonce,
    });
    const view = new DataView(pre.buffer);
    expect(view.getUint32(0, false)).toBe(2); // len("AB")
    expect(String.fromCharCode(pre[4], pre[5])).toBe('AB');
    expect(view.getUint32(6, false)).toBe(4); // len("self")
    expect(view.getBigInt64(14, false)).toBe(2_000_000_000n); // UnixNano
    expect(view.getUint32(22, false)).toBe(2); // len(nonce)
    expect(Array.from(pre.slice(26))).toEqual([0xde, 0xad]);
    expect(pre).toHaveLength(4 + 2 + 4 + 4 + 8 + 4 + 2);
  });

  it('formats issued_at as RFC3339 with second precision (§7)', () => {
    expect(rfc3339Seconds(1_785_000_000)).toBe('2026-07-25T17:20:00Z');
    expect(rfc3339Seconds(1_785_000_000)).not.toContain('.');
    // Sub-second input is truncated, not rounded — the signed UnixNano must
    // match the second the node re-parses out of issued_at.
    expect(rfc3339Seconds(1_785_000_000.9)).toBe('2026-07-25T17:20:00Z');
  });
});

describe('xpub_fingerprint — naming the signed-in key (contract §4, §6b)', () => {
  // The node's own xpub fixture (internal/epm/service_test.go / the JS
  // cross-check in sdn-js/verify-identity.mjs). auth.XPubFingerprint =
  // first 8 bytes of SHA-256 over the exact xpub string, lowercase hex.
  const XPUB =
    'xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa';

  it('matches the node fingerprint byte for byte', async () => {
    const fp = await xpubFingerprint(XPUB);
    expect(fp).toBe('bbaaf6d6889092c8');
    expect(fp).toHaveLength(16);
    expect(fp).toMatch(/^[0-9a-f]{16}$/);
  });

  it('is stable, and a different xpub gives a different fingerprint', async () => {
    expect(await xpubFingerprint(XPUB)).toBe(await xpubFingerprint(` ${XPUB} `));
    expect(await xpubFingerprint(`${XPUB}x`)).not.toBe(await xpubFingerprint(XPUB));
    expect(await xpubFingerprint('')).toBe('');
  });

  it('never returns anything from which the xpub could be read back', async () => {
    const fp = await xpubFingerprint(XPUB);
    expect(XPUB).not.toContain(fp);
    expect(fp.length * 4).toBeLessThan(XPUB.length * 4); // strictly lossy
  });

  it('treats UNVERIFIABLE as "not a mismatch" (no SubtleCrypto, older node)', () => {
    expect(fingerprintMatches('bbaaf6d6889092c8', 'bbaaf6d6889092c8')).toBe(true);
    expect(fingerprintMatches('BBAAF6D6889092C8', 'bbaaf6d6889092c8')).toBe(true);
    expect(fingerprintMatches('', 'bbaaf6d6889092c8')).toBe(true); // no local key held
    expect(fingerprintMatches('bbaaf6d6889092c8', '')).toBe(true); // node did not say
    expect(fingerprintMatches('', '')).toBe(true);
    // Two present, differing values: the cookie is a DIFFERENT identity.
    expect(fingerprintMatches('bbaaf6d6889092c8', '0000000000000000')).toBe(false);
  });

  it('shortens to the chip form', () => {
    expect(shortFingerprint('bbaaf6d6889092c8')).toBe('bbaaf6d6…');
    expect(shortFingerprint('abc')).toBe('abc');
    expect(shortFingerprint('')).toBe('');
  });
});

describe('THIS NODE profile form (contract §6)', () => {
  const CURRENT = {
    dn: 'CelesTrak Ops',
    email: 'ops@example.org',
    address: { locality: 'Boulder', country: 'US' },
    alternate_names: ['celestrak', 'ct-ops'],
    photo_data_url: 'data:image/png;base64,AAAA',
    // read-only extras the node also returns — must not leak into the body
    keys: [{ public_key: 'ab' }],
    signature: 'deadbeef',
  };

  it('starts from CURRENT values only — absent fields stay EMPTY', () => {
    const form = profileFormFromJson(CURRENT);
    expect(form.dn).toBe('CelesTrak Ops');
    expect(form.email).toBe('ops@example.org');
    expect(form.address.locality).toBe('Boulder');
    expect(form.alternate_names).toBe('celestrak\nct-ops');
    // Never invented: every unreported field is empty.
    for (const f of PROFILE_FIELDS) {
      if (!(f.key in CURRENT)) expect(form[f.key]).toBe('');
    }
    for (const f of ADDRESS_FIELDS) {
      if (!(f.key in CURRENT.address)) expect(form.address[f.key]).toBe('');
    }
  });

  it('an absent/garbage profile yields an all-empty form', () => {
    for (const input of [null, undefined, {}, 'nope', 42]) {
      const form = profileFormFromJson(input);
      expect(form).toEqual(emptyProfileForm());
    }
  });

  it('serializes EVERY editable key, so clearing a field clears it', () => {
    const form = profileFormFromJson(CURRENT);
    form.email = '';
    form.address.locality = '  ';
    const body = profileFormToBody(form);
    for (const f of PROFILE_FIELDS) expect(body).toHaveProperty(f.key);
    for (const f of ADDRESS_FIELDS) expect(body.address).toHaveProperty(f.key);
    expect(body.email).toBe('');
    expect(body.address.locality).toBe('');
    expect(body.alternate_names).toEqual(['celestrak', 'ct-ops']);
  });

  it('round-trips photo_data_url so saving a name never deletes a photo', () => {
    const body = profileFormToBody(profileFormFromJson(CURRENT));
    expect(body.photo_data_url).toBe('data:image/png;base64,AAAA');
  });

  it('never forwards the node-owned read-only fields', () => {
    const body = profileFormToBody(profileFormFromJson(CURRENT));
    expect(body).not.toHaveProperty('keys');
    expect(body).not.toHaveProperty('signature');
    expect(body).not.toHaveProperty('multiformat_address');
  });

  it('trims values and drops blank alternate-name lines', () => {
    const form = emptyProfileForm();
    form.dn = '  Node  ';
    form.alternate_names = 'one\n\n  two  \n\n';
    const body = profileFormToBody(form);
    expect(body.dn).toBe('Node');
    expect(body.alternate_names).toEqual(['one', 'two']);
    expect(parseAlternateNames('')).toEqual([]);
  });

  it('reports dirtiness against the loaded baseline', () => {
    const baseline = profileFormFromJson(CURRENT);
    const form = profileFormFromJson(CURRENT);
    expect(profileFormDirty(form, baseline)).toBe(false);
    form.job_title = 'Operator';
    expect(profileFormDirty(form, baseline)).toBe(true);
  });
});

describe('permission gating (contract §7, §9.4)', () => {
  it('gates the node-identity edit and the registries at Admin', () => {
    for (const tier of ['admin', 'ultimate']) {
      expect(canEditNodeProfile(tier)).toBe(true);
      expect(canManagePermissions(tier)).toBe(true);
    }
    for (const tier of ['full', 'standard', 'marginal', 'unknown', 'never', '']) {
      expect(canEditNodeProfile(tier)).toBe(false);
      expect(canManagePermissions(tier)).toBe(false);
    }
  });

  it('gates attestation at Standard (the endpoint is below the admin wall)', () => {
    expect(canAttest('standard')).toBe(true);
    expect(canAttest('full')).toBe(true);
    expect(canAttest('marginal')).toBe(false);
  });

  it('caps operator grants at ADMIN and refuses never/ultimate (§7)', () => {
    const tiers = assignableUserTiers('admin');
    expect(tiers).toEqual(['unknown', 'marginal', 'standard', 'full', 'admin']);
    expect(tiers).not.toContain('ultimate');
    expect(tiers).not.toContain('never');
    // An ultimate session still cannot grant above admin.
    expect(assignableUserTiers('ultimate')).toEqual(tiers);
    // Below admin nothing is assignable at all.
    expect(assignableUserTiers('full')).toEqual([]);
    expect(assignableUserTiers('')).toEqual([]);
  });

  it('never offers ultimate for a PEER (§9.4) even though the API takes it', () => {
    const tiers = assignablePeerTiers('admin');
    expect(tiers).not.toContain('ultimate');
    expect(tiers).toContain('never');
    expect(assignablePeerTiers('standard')).toEqual([]);
  });

  it('orders a tier list most-trusted first for the select', () => {
    expect(tiersHighestFirst(['unknown', 'admin', 'standard'])).toEqual(['admin', 'standard', 'unknown']);
  });

  it('flags an approved xpub with no key bound yet as awaiting proof (§5.3)', () => {
    expect(userNeedsKeyProof({ xpub: 'x', signing_pubkey_hex: '' })).toBe(true);
    expect(userNeedsKeyProof({ xpub: 'x' })).toBe(true);
    expect(userNeedsKeyProof({ xpub: 'x', signing_pubkey_hex: '0d80' })).toBe(false);
  });
});

describe('add-by-key-or-vcard (contract §8)', () => {
  it('classifies what the operator pasted', () => {
    expect(classifyPeerInput('').kind).toBe('empty');
    expect(classifyPeerInput('BEGIN:VCARD\r\nEND:VCARD').kind).toBe('vcard');
    expect(classifyPeerInput('xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8').kind).toBe('xpub');
    expect(classifyPeerInput('12D3KooWExampleExample').kind).toBe('peer_id');
    expect(classifyPeerInput('16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbY').kind).toBe('peer_id');
    expect(classifyPeerInput('0x' + 'ab'.repeat(32)).kind).toBe('public_key');
    expect(classifyPeerInput('ab'.repeat(32)).kind).toBe('public_key');
    expect(classifyPeerInput('hello there').kind).toBe('unknown');
  });

  it('reads a pasted card without inventing anything', () => {
    const card = [
      'BEGIN:VCARD',
      'VERSION:3.0',
      'FN:Peer Node',
      'ORG:DigitalArsenal',
      'EMAIL;type=INTERNET;type=xpub:xpub661MyMwAqRbcF@xpub.spacedatanetwork.org',
      'X-SDN-PEER-ID:12D3KooWExample',
      'END:VCARD',
    ].join('\r\n');
    const parsed = peerFromVCardText(card);
    expect(parsed.peerId).toBe('12D3KooWExample');
    expect(parsed.xpub).toBe('xpub661MyMwAqRbcF');
    expect(parsed.name).toBe('Peer Node');
    expect(parsed.organization).toBe('DigitalArsenal');
    expect(parsed.vcard).toContain('BEGIN:VCARD');

    const bare = peerFromVCardText('BEGIN:VCARD\r\nVERSION:3.0\r\nEND:VCARD');
    expect(bare.name).toBe('');
    expect(bare.organization).toBe('');
    expect(bare.peerId).toBe('');
  });

  it('builds a peer body carrying only what was supplied', () => {
    expect(buildAddPeerBody({ id: '12D3KooWX', trustLevel: 'full' })).toEqual({
      id: '12D3KooWX',
      trust_level: 'full',
    });
    const full = buildAddPeerBody({
      publicKey: '0xab',
      trustLevel: 'standard',
      name: ' Ops ',
      organization: '',
      notes: 'seen at conference',
      vcard: 'BEGIN:VCARD',
    });
    expect(full).toEqual({
      public_key: '0xab',
      trust_level: 'standard',
      name: 'Ops',
      notes: 'seen at conference',
      vcard: 'BEGIN:VCARD',
    });
    expect(full).not.toHaveProperty('organization');
    // An unrecognized tier normalizes to unknown rather than being sent raw.
    expect(buildAddPeerBody({ id: 'x', trustLevel: 'bogus' }).trust_level).toBe('unknown');
  });

  it('builds an operator body, lowercasing an optional signing key', () => {
    expect(buildAddUserBody({ xpub: ' xpub1 ', name: ' TJ ', trustLevel: 'admin' })).toEqual({
      xpub: 'xpub1',
      name: 'TJ',
      trust_level: 'admin',
    });
    expect(
      buildAddUserBody({ xpub: 'x', trustLevel: 'standard', signingPubKeyHex: '0x0D80E1FD' })
        .signing_pubkey_hex
    ).toBe('0d80e1fd');
  });
});
