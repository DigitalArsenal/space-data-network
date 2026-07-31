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
  WALLET_UI_VERSION,
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
    // The ?v= stamp is the pinned wallet version: it changes the CDN-edge
    // cache key on every restage so a proxy can never serve a prior
    // staging's bytes (the path itself stays same-origin).
    expect(WALLET_UI_ENTRY).toBe(`/wallet-ui/wallet-origin/index.js?v=${WALLET_UI_VERSION}`);
    expect(WALLET_UI_VERSION).toMatch(/^\d+\.\d+\.\d+$/);
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

/*
 * THE INERT LYING BUTTON (IRIS ruling 2026-07-30 §2). The owner chose FULL,
 * pressed APPLY, and EFFECTIVE stayed STANDARD — over a <select> holding one
 * option, because both tier lists above return `[]` below Admin and the modal
 * padded the empty list with the peer's CURRENT tier. A one-option select is
 * not a control, and a primary button that cannot fire is a claim about this
 * session that the node does not agree with.
 */
describe('trustControlState — an armed control can fire, or it is not rendered', async () => {
  const { trustControlState } = await import('./permissions.js');

  it('one option is not a control: > 1, not > 0', () => {
    expect(trustControlState({ hasSession: true, tiersKnown: true, tierCount: 1 })).toBe('needs-admin');
    expect(trustControlState({ hasSession: true, tiersKnown: true, tierCount: 0 })).toBe('needs-admin');
    expect(trustControlState({ hasSession: true, tiersKnown: true, tierCount: 2 })).toBe('armed');
  });

  it('"not asked yet" is never reported as "you may not"', () => {
    // The whole reason `tiersKnown` exists: an empty tier list means both.
    expect(trustControlState({ hasSession: true, tiersKnown: false, tierCount: 0 })).toBe('loading');
    expect(trustControlState({ hasSession: true, tiersKnown: false, tierCount: 6 })).toBe('loading');
  });

  it('no session outranks both — there is nobody to be below Admin', () => {
    expect(trustControlState({ hasSession: false, tiersKnown: true, tierCount: 6 })).toBe('needs-signin');
    expect(trustControlState({ hasSession: false, tiersKnown: false, tierCount: 0 })).toBe('needs-signin');
  });

  it('is exactly the gate the two tier lists produce for a real session', () => {
    const state = (level) =>
      trustControlState({
        hasSession: true,
        tiersKnown: true,
        tierCount: assignablePeerTiers(level).length,
      });
    expect(state('admin')).toBe('armed');
    expect(state('ultimate')).toBe('armed');
    // The owner's own case: /api/auth/me said standard.
    expect(state('standard')).toBe('needs-admin');
    expect(state('full')).toBe('needs-admin');
    expect(state('')).toBe('needs-admin');
    // …and the operator store's ceiling behaves identically (§7).
    expect(assignableUserTiers('standard')).toEqual([]);
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

/*
 * THE PIN REGISTRY — the interface the owner asked for by name on 2026-07-30
 * ("we need an interface for that"). A pin is the ONLY reason a peer this node
 * has never contacted appears in the peers table at all, so adding a peer pins
 * it, and every refusal is a sentence rather than a button that does nothing.
 */
describe('pins (POST/DELETE /api/peers/pins)', async () => {
  const {
    buildPinBody, multiaddrsFromVCard, pinIsLocked, pinSourceLabel, pinNoteIsPublishable,
    pinDisplayName, pinnedAtLabel, sortPins, pinnableNodes,
  } = await import('./peers.js');
  const { describeApiError, ApiError } = await import('./api.js');

  it('sends lowercase synthesized keys and only what was supplied', () => {
    expect(buildPinBody({ peerId: ' 12D3KooWX ' })).toEqual({ peer_id: '12D3KooWX' });
    expect(
      buildPinBody({
        peerId: '12D3KooWX',
        addrs: ['/ip4/1.2.3.4/tcp/4001', '', '  '],
        name: ' Ops ',
        note: ' third box ',
      })
    ).toEqual({
      peer_id: '12D3KooWX',
      addrs: ['/ip4/1.2.3.4/tcp/4001'],
      name: 'Ops',
      note: 'third box',
    });
    // Nothing empty is ever sent — the pin registry is not handed blanks.
    expect(buildPinBody({ peerId: 'x', addrs: [], name: '', note: '' })).toEqual({ peer_id: 'x' });
  });

  it('a full multiaddr is passed through as the identity (the API takes either)', () => {
    const addr = '/ip4/5.6.7.8/tcp/4001/p2p/12D3KooWX';
    expect(buildPinBody({ peerId: addr })).toEqual({ peer_id: addr });
  });

  it('takes multiaddrs from a pasted card verbatim, and invents none', () => {
    const card = [
      'BEGIN:VCARD',
      'VERSION:4.0',
      'FN:Peer Node',
      'X-SDN-MULTIADDR:/ip4/1.2.3.4/tcp/4001',
      'X-SDN-MULTIADDR:/ip4/1.2.3.4/udp/4001/quic-v1',
      'X-SDN-MULTIADDR:/ip4/1.2.3.4/tcp/4001',
      'END:VCARD',
    ].join('\r\n');
    expect(multiaddrsFromVCard(card)).toEqual([
      '/ip4/1.2.3.4/tcp/4001',
      '/ip4/1.2.3.4/udp/4001/quic-v1',
    ]);
    expect(multiaddrsFromVCard('BEGIN:VCARD\r\nFN:x\r\nEND:VCARD')).toEqual([]);
    expect(multiaddrsFromVCard('')).toEqual([]);
  });

  /*
   * AMENDED 2026-07-30 (IRIS ruling §6): this test used to assert
   * `pinNoteLabel(cfg) === 'CONFIG'` — the label over a rendered config path.
   * That render is gone, and with it the label: the only question left about a
   * note is whether it may be shown at ALL, which is what replaced it here.
   */
  it('a config-file pin is locked, named as such, and its note is never shown', () => {
    const cfg = { peer_id: 'a', source: 'config', note: '/etc/sdn/config.yaml  peers.trusted_peers' };
    const own = { peer_id: 'b', source: 'operator', note: 'the box in the lab' };
    expect(pinIsLocked(cfg)).toBe(true);
    expect(pinIsLocked(own)).toBe(false);
    expect(pinSourceLabel(cfg)).toBe('FROM CONFIG FILE');
    expect(pinSourceLabel(own)).toBe('PINNED BY OPERATOR');
    // The two notes are different KINDS of fact: one is prose, one is a place.
    expect(pinNoteIsPublishable(cfg)).toBe(false);
    expect(pinNoteIsPublishable(own)).toBe(true);
  });

  /*
   * AMENDED 2026-07-30 (IRIS ruling §4): "unknown" is a TRUST TIER and these
   * dialogs render one in the same head. An absent name is `unnamed`.
   */
  it('an unnamed pin reads "unnamed", never its own id promoted to a name', () => {
    expect(pinDisplayName({ peer_id: '12D3KooWX' })).toBe('unnamed');
    expect(pinDisplayName({ peer_id: '12D3KooWX', name: ' Ops ' })).toBe('Ops');
  });

  it('a missing pinned_at is an ABSENT cell, never an invented time', () => {
    const NOW = 1_700_000_000_000;
    expect(pinnedAtLabel({}, NOW)).toBe('');
    expect(pinnedAtLabel({ pinned_at: 0 }, NOW)).toBe('');
    expect(pinnedAtLabel({ pinned_at: 1_699_996_400 }, NOW)).toBe('PINNED 1h ago');
    // RFC3339 is accepted too — the wire shape is the node's to choose.
    expect(pinnedAtLabel({ pinned_at: '2023-11-14T22:13:20Z' }, NOW)).toBe('PINNED just now');
  });

  it('config pins sort first — they are the ones that need explaining', () => {
    const sorted = sortPins([
      { peer_id: 'b', name: 'Zulu' },
      { peer_id: 'a', name: 'Alpha' },
      { peer_id: 'c', name: 'Mike', source: 'config' },
    ]);
    expect(sorted.map((p) => p.peer_id)).toEqual(['c', 'a', 'b']);
  });

  it('offers PIN only for peers connected NOW that are not pinned already', () => {
    const nodes = [
      { peerId: 'self', isSelf: true, online: true, source: 'connected' },
      { peerId: 'live', online: true, source: 'connected' },
      { peerId: 'kept', online: true, source: 'connected', pinned: true },
      { peerId: 'gone', online: false, source: 'pinned' },
      { peerId: 'op', online: true, source: 'account' },
      { peerId: 'raced', online: true, source: 'connected' },
    ];
    // `raced` is already in the pin registry — the feed frame was one behind.
    const ids = pinnableNodes(nodes, [{ peer_id: 'raced' }]).map((n) => n.peerId);
    expect(ids).toEqual(['live']);
  });

  it('every pin refusal is a sentence an operator can act on', () => {
    const say = (code) => describeApiError(new ApiError(409, code, ''));
    expect(say('config_pin')).toContain('config file');
    expect(say('already_pinned')).toContain('already pinned');
    expect(say('not_pinned')).toContain('nothing to remove');
    expect(describeApiError(new ApiError(400, 'invalid_peer', ''))).toContain('multiaddr');
    // None of them is the bare code, which is what a silent failure looks like.
    for (const code of ['config_pin', 'already_pinned', 'not_pinned', 'invalid_peer']) {
      expect(say(code)).not.toBe(code);
    }
  });
});

/*
 * NO FAILURE IS EVER REPORTED AS A STATUS CODE (IRIS ruling 2026-07-30 §2).
 * The owner pressed APPLY on a peer's trust and the modal's error banner said
 * `HTTP 521` — which came from ApiError's own synthesized message, straight
 * through the old `default:` arm. A status is a fact about the transport; the
 * operator needs the fact about their action, starting with whether anything
 * changed.
 */
describe('describeApiError — a status is not a sentence', async () => {
  const { describeApiError, ApiError } = await import('./api.js');
  const STATUSES = [0, 400, 401, 403, 404, 409, 429, 500, 502, 503, 504, 521, 522, 524, 530];

  it('never renders a bare HTTP status, for any status the node can answer', () => {
    for (const status of STATUSES) {
      // The shape the owner hit: no code, no body — so ApiError's message IS
      // `HTTP <status>` and the fallback has to see through it.
      const said = describeApiError(new ApiError(status, '', ''));
      expect(said, `status ${status}`).not.toMatch(/HTTP\s*\d{3}/);
      expect(said.trim().length, `status ${status}`).toBeGreaterThan(0);
    }
  });

  it('says the node could not be reached when the fetch never landed', () => {
    expect(describeApiError(new ApiError(0, '', ''))).toBe('Could not reach the node.');
    expect(describeApiError(new ApiError(0, 'network_error', 'Could not reach the node.'))).toBe(
      'Could not reach the node.'
    );
  });

  it('says nothing was changed when nothing on the other end answered', () => {
    for (const status of [502, 503, 504, 521, 522, 523, 524, 525, 526, 530]) {
      const said = describeApiError(new ApiError(status, '', ''));
      expect(said, `status ${status}`).toBe(
        'The node did not answer. It may be restarting, or the connection to it is down. Nothing was changed.'
      );
    }
  });

  it('other 5xx: the node answered, and it still changed nothing', () => {
    expect(describeApiError(new ApiError(500, '', ''))).toBe(
      'The node answered with an error. Nothing was changed.'
    );
  });

  it('a real sentence from the node survives; a code masquerading as one does not', () => {
    // internal/peers answers http.Error — a text body and a status, no code.
    expect(describeApiError(new ApiError(409, '', 'peer is pinned by config'))).toBe(
      'peer is pinned by config'
    );
    expect(describeApiError(new ApiError(404, '', 'HTTP 404'))).toBe('The node refused that request.');
  });

  it('a contract code still wins over the status', () => {
    expect(describeApiError(new ApiError(403, 'forbidden', ''))).toContain('trust level');
  });
});

describe('ACCOUNTS — one list for nodes and logins (contract §16)', async () => {
  const {
    accountFromNode, accountFromEntry, mergeAccounts, accountDisplayName,
    isUnnamed, accountIdentifier, kindLabel, editTargets, sortAccounts,
  } = await import('./accounts.js');
  const { withoutQuarantine, readQuarantinedRecords, QUARANTINE_KEYS } = await import('./walletui.js');

  const feed = [
    { peerId: '12D3KooWAlpha', dn: 'Alpha Node', org: 'Ops', trustLevel: 'full', online: true, isSelf: false },
    { peerId: '12D3KooWSelf', dn: '', org: '', trustLevel: 'ultimate', online: true, isSelf: true },
  ];

  it('the anonymous tier is the feed, verbatim', () => {
    const rows = feed.map(accountFromNode);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ peerId: '12D3KooWAlpha', kind: 'peer', name: 'Alpha Node', canSignIn: false, xpub: '' });
    // Nothing operator-shaped is invented for an anonymous row.
    expect(rows[0].source).toBe('');
    expect(rows[0].connectionCount).toBe(0);
  });

  it('merges the Admin overlay onto feed rows by peer id', () => {
    const merged = mergeAccounts(feed.map(accountFromNode), [
      { peer_id: '12D3KooWAlpha', xpub: 'xpub6Alpha', kind: 'both', name: 'Alpha Node',
        trust_level: 'admin', can_sign_in: true, source: 'config', connection_count: 7, last_connected: 1785000000 },
    ]);
    expect(merged).toHaveLength(2); // merged, not appended
    const alpha = merged.find((r) => r.peerId === '12D3KooWAlpha');
    expect(alpha).toMatchObject({ kind: 'both', xpub: 'xpub6Alpha', canSignIn: true, connectionCount: 7 });
    // §16.2: the server already reconciled to the higher tier; take its word.
    expect(alpha.trustLevel).toBe('admin');
    // Live presence stays the feed's.
    expect(alpha.online).toBe(true);
  });

  it('appends an operator with no peer presence, and never merges on an EMPTY id', () => {
    const merged = mergeAccounts(feed.map(accountFromNode), [
      { peer_id: '', xpub: 'xpub6Orphan', kind: 'operator', name: 'Config Label', trust_level: 'standard', can_sign_in: true },
      { peer_id: '16Uiu2HAmOperator', xpub: 'xpub6Op', kind: 'operator', name: 'Remote Op', trust_level: 'full', can_sign_in: true },
    ]);
    expect(merged).toHaveLength(4);
    expect(merged.filter((r) => r.kind === 'operator')).toHaveLength(2);
    // An empty peer id matched nothing, rather than folding into a real row.
    expect(merged.find((r) => r.xpub === 'xpub6Orphan').peerId).toBe('');
  });

  it('NAME is always a primary line — "unknown" when there is none', () => {
    expect(accountDisplayName({ name: 'Alpha Node' })).toBe('Alpha Node');
    expect(accountDisplayName({ name: '', organization: 'Ops' })).toBe('Ops');
    expect(accountDisplayName({ name: '  ', organization: '' })).toBe('unknown');
    expect(accountDisplayName({})).toBe('unknown');
    // An identifier is NEVER promoted to look like a name.
    expect(accountDisplayName({ peerId: '12D3KooWAlpha' })).toBe('unknown');
    expect(isUnnamed({ peerId: '12D3KooWAlpha' })).toBe(true);
    expect(isUnnamed({ name: 'Alpha' })).toBe(false);
    // ...but it is still shown, underneath.
    expect(accountIdentifier({ peerId: '12D3KooWAlpha' })).toBe('12D3KooWAlpha');
    expect(accountIdentifier({ peerId: '', xpub: 'xpub6Op' })).toBe('xpub6Op');
  });

  it('names the two facets a `both` row can edit (§16.4.5)', () => {
    const both = editTargets({ kind: 'both', peerId: '12D3KooWAlpha', xpub: 'xpub6Alpha' });
    expect(both.map((t) => t.facet)).toEqual(['peer', 'operator']);
    expect(both[0].path).toBe('/api/peers/12D3KooWAlpha/trust');
    expect(both[1].path).toBe('/api/auth/users/xpub6Alpha');
    // A single-facet row offers exactly one target — no guessing.
    expect(editTargets({ kind: 'peer', peerId: '12D3KooWAlpha' })).toHaveLength(1);
    expect(editTargets({ kind: 'operator', xpub: 'xpub6Op' })).toHaveLength(1);
    expect(kindLabel('both')).toBe('NODE + LOGIN');
    expect(kindLabel('operator')).toBe('LOGIN');
  });

  it('sorts this node first, then by trust', () => {
    const rows = sortAccounts(mergeAccounts(feed.map(accountFromNode), []));
    expect(rows[0].isSelf).toBe(true);
  });

  // Owner rule, restated 2026-07-28: "the self node should not be in this
  // table." The filter is applied at the source, on BOTH sides of the merge,
  // so no /api/accounts overlay can put the row back.
  it('the listing never contains this node — not even via the Admin overlay', async () => {
    const { withoutSelf } = await import('./accounts.js');
    const selfId = '12D3KooWSelf';
    const rows = sortAccounts(withoutSelf(mergeAccounts(withoutSelf(feed.map(accountFromNode), selfId), [
      { peer_id: selfId, xpub: 'xpub6Self', kind: 'both', name: 'This Node', trust_level: 'admin', can_sign_in: true },
    ]), selfId));
    expect(rows.some((r) => r.isSelf)).toBe(false);
    // Without the id filter the overlay entry matched nothing and was APPENDED.
    expect(rows.map((r) => r.peerId)).toEqual(['12D3KooWAlpha']);
  });

  it('reads the published vCard FN when the feed carries no DN, but never a placeholder', async () => {
    const { vcardDisplayName } = await import('./accounts.js');
    const card = (fn) => `BEGIN:VCARD\r\nVERSION:4.0\r\nFN:${fn}\r\nEND:VCARD\r\n`;
    // A peer whose EPM names it but whose registry entry has no DN: the NAME
    // column reads the published name rather than falling to "unknown".
    const named = accountFromNode({ peerId: '16Uiu2HAmNamed', dn: '', org: '', vcard: card('Celestrak Ops') });
    expect(accountDisplayName(named)).toBe('Celestrak Ops');
    // The node's synthesized card for a peer it knows nothing about carries
    // the peer id in ShortString form — an identifier, so it stays "unknown".
    const placeholder = accountFromNode({ peerId: '16Uiu2HAmQMSobG4', dn: '', org: '', vcard: card('<peer.ID 16*cuvDMv>') });
    expect(accountDisplayName(placeholder)).toBe('unknown');
    expect(isUnnamed(placeholder)).toBe(true);
    // A verbatim peer id is refused for the same reason.
    expect(vcardDisplayName(card('16Uiu2HAmQMSobG4'), '16Uiu2HAmQMSobG4')).toBe('');
    // DN still wins when the feed has one.
    expect(accountFromNode({ peerId: 'p', dn: 'Config Trusted Peer', vcard: card('Other') }).name).toBe('Config Trusted Peer');
    expect(vcardDisplayName('', 'p')).toBe('');
  });

  it('the sign-in controller declines the quarantine capability (§15.1)', () => {
    const real = {
      listQuarantinedWalletRecords: () => [{ key: 'x' }],
      registerCredentialControls: () => 'registered',
      supportsRememberedWallet: () => true,
      unlockLegacy: () => 'unlocked',
    };
    const shim = withoutQuarantine(real);
    // The wallet bails on exactly this check — so the block never renders.
    expect(typeof shim.listQuarantinedWalletRecords).not.toBe('function');
    expect('listQuarantinedWalletRecords' in shim).toBe(false);
    // Everything the prompt legitimately needs still works.
    expect(shim.registerCredentialControls()).toBe('registered');
    expect(shim.supportsRememberedWallet()).toBe(true);
    expect(shim.unlockLegacy()).toBe('unlocked');
  });

  it('reads orphaned wallet records presence-based, like the wallet does', () => {
    const store = new Map([['wallet_storage_metadata', '{"a":1}']]);
    const fake = { getItem: (k) => (store.has(k) ? store.get(k) : null) };
    const rows = readQuarantinedRecords(fake);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ key: 'wallet_storage_metadata', bytes: 7 });
    // A clean origin yields NOTHING — the widget then renders nothing at all.
    expect(readQuarantinedRecords({ getItem: () => null })).toEqual([]);
  });

  it('covers ALL SIX quarantine keys, including the pre-2.0.6 vintage', () => {
    // Byte-identical to the wallet's LEGACY_WALLET_QUARANTINE_KEYS. A missing
    // name is a record the operator can neither see nor delete.
    expect(QUARANTINE_KEYS).toEqual([
      'wallet_storage_metadata',
      'wallet_storage_encrypted',
      'wallet_storage_passkey_credential',
      'encrypted_wallet',
      'passkey_credential',
      'passkey_wallet',
    ]);
  });

  it('finds a LEGACY-vintage record that the wallet_storage_* scan missed', () => {
    // An operator whose records predate the 2.0.6 CDN page holds ONLY these.
    const store = new Map([
      ['encrypted_wallet', 'LEGACYBLOB'],
      ['passkey_wallet', '{"id":"abc"}'],
    ]);
    const rows = readQuarantinedRecords({ getItem: (k) => (store.has(k) ? store.get(k) : null) });
    expect(rows.map((r) => r.key)).toEqual(['encrypted_wallet', 'passkey_wallet']);
    expect(rows[0].bytes).toBe(10);
    expect(rows[1].bytes).toBe(12);
  });
});

describe('contact card — the human vCard (owner directive 2026-07-27)', async () => {
  const { parseVCard, contactCard, splitStructured } = await import('./vcard.js');
  const CRLF = '\r\n';
  const FULL = [
    'BEGIN:VCARD', 'VERSION:3.0', 'N:Koury;TJ;Q;Dr;PhD', 'FN:TJ Koury',
    'ORG:DigitalArsenal', 'TITLE:Director', 'TEL:+1-555-0100',
    'EMAIL;TYPE=INTERNET:tj@example.org',
    'ADR:PO 9;;123 Main St;Sterling;VA;20164;USA',
    'URL:https://spaceaware.io', 'X-SOCIALPROFILE;TYPE=x:@spaceaware',
    'EMAIL;type=INTERNET;type=xpub:xpub661@xpub.spacedatanetwork.org',
    'END:VCARD',
  ].join(CRLF);

  it('splits structured values on UNESCAPED semicolons only', () => {
    expect(splitStructured('a;b;c')).toEqual(['a', 'b', 'c']);
    // NB: the vCard bytes contain a backslash, so the JS literal needs two.
    expect(splitStructured('Smith\\; Jr;John')).toEqual(['Smith; Jr', 'John']);
    expect(splitStructured('line\\nbreak;x')).toEqual(['line\nbreak', 'x']);
    expect(splitStructured('')).toEqual(['']);
  });

  it('reads the human fields out of a full card', () => {
    const rows = Object.fromEntries(contactCard(parseVCard(FULL)).map((r) => [r.key, r.value]));
    expect(rows.given_name).toBe('TJ');
    expect(rows.family_name).toBe('Koury');
    expect(rows.organization).toBe('DigitalArsenal');
    expect(rows.job_title).toBe('Director');
    expect(rows.telephone).toBe('+1-555-0100');
    expect(rows.street).toBe('123 Main St');
    expect(rows.locality).toBe('Sterling');
    expect(rows.postal_code).toBe('20164');
    expect(rows.country).toBe('USA');
    expect(rows.po_box).toBe('PO 9');
    expect(rows.website).toBe('https://spaceaware.io');
    expect(rows.social).toBe('x: @spaceaware');
  });

  it('shows the HUMAN email, never a machine alias', () => {
    const rows = Object.fromEntries(contactCard(parseVCard(FULL)).map((r) => [r.key, r.value]));
    expect(rows.email).toBe('tj@example.org');
    expect(rows.email).not.toContain('spacedatanetwork.org');
  });

  it('renders every field even when the card is empty — blank, never invented', () => {
    const bare = contactCard(parseVCard(['BEGIN:VCARD', 'VERSION:3.0', 'END:VCARD'].join(CRLF)));
    // The card shows its SHAPE: all rows present...
    expect(bare).toHaveLength(18);
    expect(bare.map((r) => r.label)).toContain('FIRST NAME');
    expect(bare.map((r) => r.label)).toContain('SOCIAL');
    // ...and every value is literally empty. Nothing is defaulted or guessed.
    expect(bare.every((r) => r.value === '')).toBe(true);
    // Same shape for a card that does not exist at all.
    expect(contactCard(parseVCard('')).every((r) => r.value === '')).toBe(true);
    expect(contactCard(null).every((r) => r.value === '')).toBe(true);
  });

  it('carries no machine identity — that lives in the other widgets', () => {
    const keys = contactCard(parseVCard(FULL)).map((r) => r.key);
    for (const machine of ['xpub', 'signing_key', 'epm_signature', 'peer_id', 'derivation']) {
      expect(keys).not.toContain(machine);
    }
  });
});
