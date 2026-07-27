/*
 * Unit coverage for the dashboard's operator surfaces (graph task
 * nst-node-edit-permissions-ui): the sign-in wire encodings, the THIS NODE
 * profile form serialization, and the permission gating. Every expectation
 * here is quoted from graph/tasks/nst-node-admin-contract.md
 * "## Contract (final)" — if one of these fails, the UI and the node
 * disagree about the wire.
 */
import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';

import {
  WALLET_UI_ENTRY,
  WALLET_UI_COMPAT,
  LEGACY_PROFILES,
  base64ToBase64Url,
  buildRawChallengeTransaction,
  buildRegistryBinding,
  canonicalJson,
  createInProcessRelay,
  createRegistry,
  isoMillis,
  randomHex,
  randomResultToken,
  readIdentity,
  readSignature,
} from './walletui.js';

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
    });
    expect(body.challenge).toBe(challenge);
    expect(body.challenge).not.toContain('=');
    expect(body.client_pubkey_hex).toBe('0d80e1fd');
    expect(body.signature_hex).toBe('aabb');
    // §4: an xpub is echoed at verify ONLY if one was sent at challenge —
    // and on the wallet path none ever is (§13.1).
    expect('xpub' in body).toBe(false);
  });

  it('sends ts in SECONDS and NO xpub — the node resolves by signing key (§13.1)', () => {
    const body = challengeRequestBody({ clientPubKeyHex: 'AB', nowMs: 1_785_000_000_123 });
    expect(body.ts).toBe(1_785_000_000);
    expect(body.client_pubkey_hex).toBe('ab');
    // The key is ABSENT, never an empty string: presence is what switches the
    // node from GetUserBySigningPubKey to xpub resolution.
    expect('xpub' in body).toBe(false);
    expect('xpub' in challengeRequestBody({ clientPubKeyHex: 'ab', xpub: '' })).toBe(false);
    // The field still exists for a future v2 identity with a real ACCOUNT
    // xpub; it is simply never populated on the legacy wallet path.
    expect(challengeRequestBody({ clientPubKeyHex: 'ab', xpub: 'xpub6Account' }).xpub).toBe('xpub6Account');
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

describe('hd-wallet-ui transaction plumbing (contract §11)', () => {
  it('loads BOTH wallet trees from this node only — never a site', () => {
    for (const url of [WALLET_ENTRY, WALLET_UI_ENTRY, WALLET_UI_COMPAT]) {
      expect(url.startsWith('/')).toBe(true);
      expect(url).not.toMatch(/^https?:/);
      expect(url).not.toContain('//');
    }
    expect(WALLET_UI_ENTRY).toBe('/wallet-ui/wallet-origin/index.js');
  });

  it('re-spells the node challenge into the wallet alphabet, same bytes', () => {
    // 32 bytes chosen to exercise every + and / in the standard alphabet.
    const raw = Uint8Array.from({ length: 32 }, (_, i) => (i * 251) % 256);
    const std = Buffer.from(raw).toString('base64').replace(/=+$/, '');
    const url = base64ToBase64Url(std);
    expect(url).toMatch(/^[A-Za-z0-9_-]{43}$/); // the wallet's own guard
    expect(url).not.toContain('+');
    expect(url).not.toContain('/');
    expect(Buffer.from(url, 'base64url')).toEqual(Buffer.from(raw));
  });

  it('canonicalizes exactly like the wallet (sorted keys, no whitespace)', () => {
    expect(canonicalJson({ protocolVersion: 1, challengeBase64url: 'abc' })).toBe(
      '{"challengeBase64url":"abc","protocolVersion":1}'
    );
    expect(canonicalJson({ b: [1, { d: 2, c: 3 }], a: null })).toBe('{"a":null,"b":[1,{"c":3,"d":2}]}');
  });

  it('mints identifiers in the shapes the wallet validates', () => {
    expect(randomHex(32)).toMatch(/^[0-9a-f]{64}$/);
    expect(randomResultToken()).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(isoMillis(1_785_000_000_000)).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    expect(randomHex(32)).not.toBe(randomHex(32));
  });

  it('builds a transaction with EXACTLY the 13 permitted keys', async () => {
    const binding = await buildRegistryBinding('http://127.0.0.1:5001', 'Local Test Node');
    const tx = await buildRawChallengeTransaction({
      binding,
      challengeBase64url: 'A'.repeat(43),
      nowMs: 1_785_000_000_000,
    });
    expect(Object.keys(tx).sort()).toEqual([
      'callbackUri', 'clientDisplayName', 'clientId', 'expiresAt', 'operation',
      'registryVersion', 'request', 'requestOrigin', 'requestSha256',
      'resultToken', 'schemaVersion', 'state', 'transactionId',
    ]);
    // Every field the wallet cross-checks against the binding.
    expect(tx.clientId).toBe(binding.clientId);
    expect(tx.operation).toBe('sdn.auth.raw-challenge.v1');
    expect(tx.requestOrigin).toBe(binding.requestOrigin);
    expect(tx.callbackUri).toBe(binding.callbackUri);
    expect(tx.clientDisplayName).toBe(binding.clientDisplayName);
    expect(tx.registryVersion).toBe(binding.registryReleaseSha256);
    expect(tx.request).toEqual({ challengeBase64url: 'A'.repeat(43), protocolVersion: 1 });
    // requestSha256 must be SHA-256 over the CANONICAL request, or the
    // wallet answers REQUEST_HASH_MISMATCH.
    const expected = createHash('sha256').update(canonicalJson(tx.request), 'utf8').digest('hex');
    expect(tx.requestSha256).toBe(expected);
    // Alive, and inside the binding's lifetime ceiling.
    const life = Date.parse(tx.expiresAt) - 1_785_000_000_000;
    expect(life).toBeGreaterThan(0);
    expect(life).toBeLessThanOrEqual(binding.maxLifetimeSeconds * 1000);
  });

  it('binds the registry to THIS origin and refuses any other', async () => {
    const binding = await buildRegistryBinding('http://127.0.0.1:5001', '');
    expect(binding.requestOrigin).toBe('http://127.0.0.1:5001');
    expect(binding.clientDisplayName).toBe('127.0.0.1:5001'); // factual, not invented
    expect(binding.registryReleaseSha256).toMatch(/^[0-9a-f]{64}$/);
    const registry = createRegistry(binding);
    expect(registry.resolveRegistryBinding({
      clientId: binding.clientId, operation: binding.operation, requestOrigin: binding.requestOrigin,
    })).toBe(binding);
    expect(() => registry.resolveRegistryBinding({
      clientId: binding.clientId, operation: binding.operation, requestOrigin: 'https://evil.example',
    })).toThrow();
    expect(() => registry.resolveRegistryBinding({
      clientId: binding.clientId, operation: 'sdn.auth.jcs-envelope.v2', requestOrigin: binding.requestOrigin,
    })).toThrow();
  });

  it('the injected relay performs NO network I/O and hands back the result', async () => {
    const bridge = createInProcessRelay();
    const { relay } = bridge;
    // Omitting fetchTransaction is what makes prepare(obj) take our object;
    // omitting navigate is what stops the app redirecting anywhere (§11.4).
    expect(relay.fetchTransaction).toBeUndefined();
    expect(relay.navigate).toBeUndefined();
    expect(bridge.read()).toBeNull();
    await relay.publishResult({}, { signatureHex: 'ab' });
    expect(bridge.read()).toEqual({ signatureHex: 'ab' });
  });

  it('reads ONLY the signing key — the master xpub is dropped (§13.1)', () => {
    const identity = {
      // Depth-0 MASTER xpub: what both raw-32-capable legacy profiles report.
      accountXpub: 'xpub661MyMwAqRbcH5EbKFAfEyc2osNwCbsTSgiZwpXudjG524wn1UuwUY',
      accountPeerId: '16Uiu2HAmExample',
      keys: [{ publicKeyHex: 'AB'.repeat(32), keyId: `sha256:${'c'.repeat(64)}`, path: "m/44'/0'/0'/0/0" }],
    };
    const read = readIdentity(identity);
    expect(read.clientPubKeyHex).toBe('ab'.repeat(32));
    // The master xpub must not survive into page state at all — it enumerates
    // every account and address under the wallet.
    expect(read).not.toHaveProperty('xpub');
    expect(read).not.toHaveProperty('peerId');
    expect(JSON.stringify(read)).not.toContain('xpub');
    // §11.2/§13.1: raw-challenge signs with the LEGACY non-hardened path, a
    // different key from §2's SLIP-10 path for the same phrase.
    expect(read.derivationPath).toBe("m/44'/0'/0'/0/0");
    // An identity with no usable auth key is refused; a missing xpub is fine.
    expect(() => readIdentity({ accountXpub: 'x', keys: [{}] })).toThrow();
    expect(readIdentity({ keys: [{ publicKeyHex: 'ab'.repeat(32) }] }).clientPubKeyHex).toBe('ab'.repeat(32));
  });

  it('reads the raw-32 signature, and refuses anything else', () => {
    expect(readSignature({ signatureHex: 'AB'.repeat(64), keyId: 'sha256:x' }).signatureHex).toBe('ab'.repeat(64));
    expect(() => readSignature({})).toThrow();
    expect(() => readSignature({ signatureHex: 'abcd' })).toThrow();
  });

  it('offers both legacy profiles the operation forces, and nothing else', () => {
    expect(LEGACY_PROFILES.map((p) => p.id)).toEqual([
      'bip39-mnemonic-v1-legacy',
      'password-fast-v1-legacy',
    ]);
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
