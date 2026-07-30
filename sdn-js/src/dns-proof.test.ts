import { ed25519 } from '@noble/curves/ed25519';
import { secp256k1 } from '@noble/curves/secp256k1';
import { sha256 } from '@noble/hashes/sha256';
import { describe, expect, it } from 'vitest';

import {
  CLOCK_SKEW_SECONDS,
  MAX_VALIDITY_SECONDS,
  DEFAULT_DOH_RESOLVERS,
  DEFAULT_QUORUM,
  canonicalStatement,
  isValidProof,
  keyFingerprint,
  normalizeDomain,
  normalizeTxtPresentation,
  originCoveredByDomain,
  ownerName,
  parseRecord,
  proofBindsKey,
  selectProofs,
  verifyDomainProof,
  verifyProof,
  type DohResolver,
  type DomainProof,
} from './dns-proof';

// The Ed25519 EPM signing key sdn.spaceaware.io publishes about itself, read
// from its own EPM record on 2026-07-30 (signing_key_path m/44'/0'/0'/0'/0').
// Public key material; used here to pin the deployment shape.
const LIVE_PUBKEY_HEX = '0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c';
const LIVE_PEER_ID = '16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45';
const LIVE_DOMAIN = 'sdn.spaceaware.io';

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i += 1) out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return out;
}

function b64url(bytes: Uint8Array): string {
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** Mint a record the way the node will, so the test exercises the real shape. */
function mintEd25519Record(params: {
  domain: string;
  peerId?: string;
  issuedAt: number;
  expiresAt?: number;
  secret?: Uint8Array;
}): { record: string; publicKey: Uint8Array } {
  const secret = params.secret ?? ed25519.utils.randomPrivateKey();
  const publicKey = ed25519.getPublicKey(secret);
  const unsigned = {
    domain: params.domain,
    algorithm: 'ed25519' as const,
    publicKey,
    peerId: params.peerId ?? '',
    issuedAt: params.issuedAt,
    expiresAt: params.expiresAt ?? 0,
  };
  const signature = ed25519.sign(canonicalStatement(unsigned), secret);
  const parts = [`v=SDN1`, `p=${b64url(publicKey)}`];
  if (unsigned.peerId) parts.push(`id=${unsigned.peerId}`);
  parts.push(`ts=${unsigned.issuedAt}`);
  if (unsigned.expiresAt) parts.push(`xp=${unsigned.expiresAt}`);
  parts.push(`sig=${b64url(signature)}`);
  return { record: parts.join('; '), publicKey };
}

describe('canonical statement', () => {
  // THE cross-language contract test. These bytes are byte-identical to the
  // golden in sdn-server/internal/dnsproof/dnsproof_test.go. If one side
  // changes and the other does not, the node mints proofs the browser refuses.
  it('is byte-identical to the Go golden statement', () => {
    const statement = canonicalStatement({
      domain: LIVE_DOMAIN,
      algorithm: 'ed25519',
      publicKey: hexToBytes(LIVE_PUBKEY_HEX),
      peerId: LIVE_PEER_ID,
      issuedAt: 1785400000,
      expiresAt: 1816936000,
    });
    expect(new TextDecoder().decode(statement)).toBe(
      'sdn-domain-proof/1\n' +
        'domain=sdn.spaceaware.io\n' +
        `key=ed25519:${LIVE_PUBKEY_HEX}\n` +
        `peerid=${LIVE_PEER_ID}\n` +
        'issued=1785400000\n' +
        'expires=1816936000\n',
    );
  });

  it('always emits every line, so producers cannot disagree about layout', () => {
    const statement = new TextDecoder().decode(
      canonicalStatement({
        domain: 'example.org',
        algorithm: 'ed25519',
        publicKey: new Uint8Array(32),
        peerId: '',
        issuedAt: 1,
        expiresAt: 0,
      }),
    );
    expect(statement.split('\n')).toHaveLength(7); // 6 lines + trailing empty
    expect(statement).toContain('peerid=\n');
    expect(statement).toContain('expires=0\n');
    expect(statement.endsWith('\n')).toBe(true);
  });

  it('refuses a U-label rather than transcoding it', () => {
    expect(() => normalizeDomain('bücher.example')).toThrow(/A-label/);
    expect(() => normalizeDomain('localhost')).toThrow(/fully qualified/);
  });
});

// The strongest isomorphism check available: a record the GO generator actually
// produced, pasted here verbatim. If the two canonical-statement implementations
// ever drift by a single byte, this test fails and the drift is caught before a
// node mints a proof the browser refuses.
//
// Produced 2026-07-30 by sdn-server/internal/dnsproof from ed25519 seed
// 2f1e9d8c7b6a5948372615043f2e1d0c9b8a79685746352413f2e1d0c0b0a0908.
const GO_GENERATED_RECORD =
  'v=SDN1; p=DHGdfLBjud-ZJ-HrQfSk0sYV1aFxzvnosc70pjnst-A; ' +
  'id=16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45; ' +
  'ts=1785400000; xp=1816936000; ' +
  'sig=2X3zbiIlXnYOJcW4ZKOGQWgLbl92fy5xH-VURZbIyFjEE1NqlSj2tDWHA9naBmJuLFRX48XdTKa7p9fZ2dltBA';

describe('cross-language parity with the Go generator', () => {
  it('accepts a record signed by sdn-server/internal/dnsproof', () => {
    const proof = parseRecord(LIVE_DOMAIN, GO_GENERATED_RECORD);
    expect(() => verifyProof(proof, 1785400001)).not.toThrow();
    expect(keyFingerprint(proof)).toBe(
      'ed25519:0c719d7cb063b9df9927e1eb41f4a4d2c615d5a171cef9e8b1cef4a639ecb7e0',
    );
    expect(proof.peerId).toBe(LIVE_PEER_ID);
  });

  it('refuses that same Go-signed record at a different domain', () => {
    const proof = parseRecord('other.example', GO_GENERATED_RECORD);
    expect(() => verifyProof(proof, 1785400001)).toThrow(/does not verify/);
  });
});

describe('record parsing and verification', () => {
  const now = 1785400000;

  it('round-trips an ed25519 proof and fits one DNS character-string', () => {
    const { record, publicKey } = mintEd25519Record({
      domain: LIVE_DOMAIN,
      peerId: LIVE_PEER_ID,
      issuedAt: now,
      expiresAt: now + 31536000,
    });
    expect(record.length).toBeLessThanOrEqual(255);
    expect(record.startsWith('v=SDN1;')).toBe(true);
    // ed25519 is the default; emitting k= would only spend bytes.
    expect(record).not.toContain('k=');

    const proof = parseRecord(LIVE_DOMAIN, record);
    expect(() => verifyProof(proof, now)).not.toThrow();
    expect(keyFingerprint(proof)).toBe(`ed25519:${keyFingerprint({ algorithm: 'ed25519', publicKey }).split(':')[1]}`);
  });

  // THE security property: a record copied into another zone cannot verify,
  // because the verifier rebuilds the statement from the domain it queried.
  it('refuses a record copied to another domain', () => {
    const { record } = mintEd25519Record({ domain: 'honest.example', issuedAt: now });
    const stolen = parseRecord('attacker.example', record);
    expect(() => verifyProof(stolen, now)).toThrow(/does not verify/);
    expect(isValidProof(stolen, now)).toBe(false);
  });

  it('enforces the validity window and tolerates one-sided clock skew', () => {
    const { record } = mintEd25519Record({ domain: 'example.org', issuedAt: now, expiresAt: now + 100 });
    const proof = parseRecord('example.org', record);
    expect(isValidProof(proof, now + 99)).toBe(true);
    expect(() => verifyProof(proof, now + 100)).toThrow(/expired/);
    expect(isValidProof(proof, now - 1)).toBe(true);
    expect(() => verifyProof(proof, now - CLOCK_SKEW_SECONDS - 1)).toThrow(/future/);
  });

  it('refuses foreign, malformed and mis-ordered records', () => {
    const { record } = mintEd25519Record({ domain: 'example.org', issuedAt: now });
    expect(() => parseRecord('example.org', '   ')).toThrow(/empty/);
    expect(() => parseRecord('example.org', `note=hi; ${record}`)).toThrow(/first tag/);
    expect(() => parseRecord('example.org', 'v=DKIM1; k=rsa; p=AAAA')).toThrow(/unsupported proof version/);
    expect(() => parseRecord('example.org', `${record}; ts=2`)).toThrow(/duplicate tag/);
    expect(() =>
      parseRecord('example.org', record.replace('v=SDN1;', 'v=SDN1; d=other.example;')),
    ).toThrow(/queried/);
  });

  // DKIM's forward-compatibility rule (RFC 6376 §3.2).
  it('ignores unknown tags so a later version can extend the record', () => {
    const { record } = mintEd25519Record({ domain: 'example.org', issuedAt: now });
    const proof = parseRecord('example.org', `${record}; bond=0xdead; note=hello`);
    expect(isValidProof(proof, now)).toBe(true);
  });

  it('treats tag-value whitespace as insignificant, like a DNS console does', () => {
    const { record } = mintEd25519Record({ domain: 'example.org', issuedAt: now });
    const messy = `${record.replace(/; /g, ' ;  ')} ; `;
    const proof = parseRecord('EXAMPLE.ORG.', messy);
    expect(isValidProof(proof, now)).toBe(true);
  });

  it('verifies a secp256k1 proof as DER over sha256, matching the EPM convention', () => {
    const secret = secp256k1.utils.randomPrivateKey();
    const publicKey = secp256k1.getPublicKey(secret, true);
    const unsigned = {
      domain: 'example.org',
      algorithm: 'secp256k1' as const,
      publicKey,
      peerId: '',
      issuedAt: now,
      expiresAt: 0,
    };
    const der = secp256k1.sign(sha256(canonicalStatement(unsigned)), secret).toDERRawBytes();
    const proof: DomainProof = { ...unsigned, signature: der };
    expect(isValidProof(proof, now)).toBe(true);

    const tampered: DomainProof = { ...proof, domain: 'other.example' };
    expect(isValidProof(tampered, now)).toBe(false);
  });
});

// These cases encode a MEASURED disagreement between the browser-usable JSON
// DoH providers (2026-07-30, google._domainkey.anthropic.com, 410-byte DKIM
// key): Cloudflare and doh.sb return DNS presentation format (415 chars,
// quoted, split at 255), Google returns the concatenated value unquoted (410).
// Without normalizing first, a quorum comparing these strings never agrees.
describe('DoH TXT presentation normalization', () => {
  const long = 'a'.repeat(255);
  it.each([
    ['google form: bare value', 'v=SDN1; p=AAAA', 'v=SDN1; p=AAAA'],
    ['cloudflare form: one quoted string', '"v=SDN1; p=AAAA"', 'v=SDN1; p=AAAA'],
    ['cloudflare form: two character-strings', `"${long}" "tail"`, `${long}tail`],
    ['escaped quote', '"say \\"hi\\""', 'say "hi"'],
    ['escaped backslash', '"a\\\\b"', 'a\\b'],
  ])('%s', (_name, input, expected) => {
    expect(normalizeTxtPresentation(input)).toBe(expected);
  });

  it('refuses an unterminated quoted string rather than guessing', () => {
    expect(() => normalizeTxtPresentation('"unterminated')).toThrow(/unterminated/);
  });
});

describe('selectProofs', () => {
  const now = 1785400000;

  it('keeps good proofs when stale, foreign and unrelated records share the name', () => {
    const older = mintEd25519Record({ domain: 'example.org', issuedAt: now - 500 });
    const newer = mintEd25519Record({ domain: 'example.org', issuedAt: now });
    const expired = mintEd25519Record({ domain: 'example.org', issuedAt: now - 1000, expiresAt: now - 10 });
    const foreign = mintEd25519Record({ domain: 'other.example', issuedAt: now });

    const { proofs, rejected } = selectProofs(
      'example.org',
      [
        '"v=spf1 include:mailgun.org ~all"',
        expired.record,
        `"${older.record}"`,
        foreign.record,
        newer.record,
      ],
      now,
    );
    expect(proofs).toHaveLength(2);
    expect(proofs[0].issuedAt).toBeGreaterThan(proofs[1].issuedAt);
    expect(rejected).toHaveLength(2);
  });
});

describe('verifyDomainProof quorum', () => {
  const now = 1785400000;

  function fakeFetch(byOperator: Record<string, { status?: number; ad?: boolean; data?: string[]; fail?: boolean }>) {
    return (async (input: string | URL) => {
      const url = String(input);
      const operator = DEFAULT_DOH_RESOLVERS.find((r) => url.startsWith(r.url))?.operator ?? 'unknown';
      const plan = byOperator[operator];
      if (!plan || plan.fail) throw new Error('network down');
      return {
        ok: true,
        status: 200,
        json: async () => ({
          Status: plan.status ?? 0,
          AD: plan.ad ?? false,
          Answer: (plan.data ?? []).map((d) => ({ name: 'x', type: 16, TTL: 300, data: d })),
        }),
      } as unknown as Response;
    }) as unknown as typeof fetch;
  }

  it('admits a value only when the quorum of independent operators agrees', async () => {
    const { record } = mintEd25519Record({ domain: LIVE_DOMAIN, issuedAt: now });
    const result = await verifyDomainProof(LIVE_DOMAIN, {
      now,
      // Cloudflare/doh.sb presentation form vs Google concatenated form: the
      // normalizer must make these agree, or the quorum silently fails.
      fetchImpl: fakeFetch({
        cloudflare: { data: [`"${record}"`] },
        google: { data: [record] },
        'doh.sb': { fail: true },
      }),
    });
    expect(result.ownerName).toBe(`_sdnkey.${LIVE_DOMAIN}`);
    expect(result.quorumMet).toBe(true);
    expect(result.proofs).toHaveLength(1);
    expect(result.errors).toHaveLength(1);
    expect(result.dnssec).toBe('insecure');
  });

  it('refuses a value only one operator can see', async () => {
    const honest = mintEd25519Record({ domain: LIVE_DOMAIN, issuedAt: now });
    const injected = mintEd25519Record({ domain: LIVE_DOMAIN, issuedAt: now });
    const result = await verifyDomainProof(LIVE_DOMAIN, {
      now,
      fetchImpl: fakeFetch({
        cloudflare: { data: [honest.record, injected.record] },
        google: { data: [honest.record] },
        'doh.sb': { data: [honest.record] },
      }),
    });
    expect(result.proofs).toHaveLength(1);
    expect(keyFingerprint(result.proofs[0])).toBe(
      keyFingerprint({ algorithm: 'ed25519', publicKey: honest.publicKey }),
    );
    expect(result.rejected.join(' ')).toMatch(/only 1 of 2 required operators/);
  });

  // Fail-closed: unavailable is not "allow".
  it('reports quorum unmet and trusts nothing when too few operators answer', async () => {
    const { record } = mintEd25519Record({ domain: LIVE_DOMAIN, issuedAt: now });
    const result = await verifyDomainProof(LIVE_DOMAIN, {
      now,
      fetchImpl: fakeFetch({
        cloudflare: { data: [record] },
        google: { fail: true },
        'doh.sb': { fail: true },
      }),
    });
    expect(result.quorumMet).toBe(false);
    expect(result.proofs).toHaveLength(0);
    expect(proofBindsKey(result, 'ed25519:whatever')).toBeNull();
  });

  // One resolver validating DNSSEC and another not is the shape of a stripping
  // attack, so disagreement is refused rather than averaged.
  it('flags a DNSSEC downgrade when operators disagree about AD', async () => {
    const { record, publicKey } = mintEd25519Record({ domain: LIVE_DOMAIN, issuedAt: now });
    const result = await verifyDomainProof(LIVE_DOMAIN, {
      now,
      fetchImpl: fakeFetch({
        cloudflare: { data: [record], ad: true },
        google: { data: [record], ad: false },
        'doh.sb': { data: [record], ad: true },
      }),
    });
    expect(result.dnssec).toBe('downgrade');
    expect(result.proofs).toHaveLength(1);
    // The proof parses, but the trust predicate must still refuse it.
    expect(proofBindsKey(result, keyFingerprint({ algorithm: 'ed25519', publicKey }))).toBeNull();
  });

  it('reports secure when every responder validated', async () => {
    const { record, publicKey } = mintEd25519Record({ domain: LIVE_DOMAIN, issuedAt: now });
    const result = await verifyDomainProof(LIVE_DOMAIN, {
      now,
      fetchImpl: fakeFetch({
        cloudflare: { data: [record], ad: true },
        google: { data: [record], ad: true },
        'doh.sb': { data: [record], ad: true },
      }),
    });
    expect(result.dnssec).toBe('secure');
    expect(proofBindsKey(result, keyFingerprint({ algorithm: 'ed25519', publicKey }))).not.toBeNull();
  });

  it('treats NXDOMAIN as "no proof", not as an error', async () => {
    const result = await verifyDomainProof(LIVE_DOMAIN, {
      now,
      fetchImpl: fakeFetch({
        cloudflare: { status: 3 },
        google: { status: 3 },
        'doh.sb': { status: 3 },
      }),
    });
    expect(result.quorumMet).toBe(true);
    expect(result.proofs).toHaveLength(0);
    expect(result.errors).toHaveLength(0);
  });

  it('counts operators, not endpoints, so duplicating one resolver cannot fake a quorum', async () => {
    const { record } = mintEd25519Record({ domain: LIVE_DOMAIN, issuedAt: now });
    const doubled: DohResolver[] = [
      { name: 'cloudflare', operator: 'cloudflare', url: 'https://cloudflare-dns.com/dns-query' },
      { name: 'cloudflare-alias', operator: 'cloudflare', url: 'https://cloudflare-dns.com/dns-query' },
    ];
    const result = await verifyDomainProof(LIVE_DOMAIN, {
      now,
      resolvers: doubled,
      fetchImpl: fakeFetch({ cloudflare: { data: [record] } }),
    });
    expect(result.respondingOperators).toEqual(['cloudflare']);
    expect(result.quorumMet).toBe(false);
    expect(result.proofs).toHaveLength(0);
  });
});

describe('owner name and origin coverage', () => {
  it('builds the owner name, with an optional selector', () => {
    expect(ownerName('SDN.SpaceAware.io.')).toBe('_sdnkey.sdn.spaceaware.io');
    expect(ownerName('sdn.spaceaware.io', 'k2')).toBe('k2._sdnkey.sdn.spaceaware.io');
    expect(() => ownerName('sdn.spaceaware.io', 'a.b')).toThrow(/single label/);
  });

  // A naive endsWith() would auto-trust evil-spaceaware.io.
  it('requires a dotted boundary for subdomain coverage', () => {
    expect(originCoveredByDomain('spaceaware.io', 'spaceaware.io')).toBe(true);
    expect(originCoveredByDomain('sdn.spaceaware.io', 'spaceaware.io')).toBe(true);
    expect(originCoveredByDomain('evil-spaceaware.io', 'spaceaware.io')).toBe(false);
    expect(originCoveredByDomain('spaceaware.io.attacker.test', 'spaceaware.io')).toBe(false);
  });
});

describe('resolver roster', () => {
  // Each entry was verified by hand (HTTP 200 + access-control-allow-origin: *)
  // on 2026-07-30. Quad9's JSON API is retired; NextDNS and AdGuard send no
  // CORS header; Mullvad is wireformat only.
  it('ships three independent operators and a quorum below N', () => {
    expect(new Set(DEFAULT_DOH_RESOLVERS.map((r) => r.operator)).size).toBe(3);
    expect(DEFAULT_QUORUM).toBeLessThan(DEFAULT_DOH_RESOLVERS.length);
    expect(DEFAULT_QUORUM).toBeGreaterThan(1);
  });
});

// Seal Council condition (Hephaestus, 2026-07-30). Enforced in canonicalStatement,
// which both producers and verifiers pass through, so an over-long proof can
// neither be minted nor accepted. A cap only the signer honours is a cap an
// attacker holding the key simply ignores.
describe('validity cap', () => {
  const issued = 1785400000;
  const base = {
    domain: 'example.org',
    algorithm: 'ed25519' as const,
    publicKey: new Uint8Array(32),
    peerId: '',
    issuedAt: issued,
  };

  it('allows exactly the cap and refuses one second more', () => {
    expect(() =>
      canonicalStatement({ ...base, expiresAt: issued + MAX_VALIDITY_SECONDS }),
    ).not.toThrow();
    expect(() =>
      canonicalStatement({ ...base, expiresAt: issued + MAX_VALIDITY_SECONDS + 1 }),
    ).toThrow(/exceeds the .* maximum/);
  });

  it('refuses an over-long window at VERIFY time, whoever signed it', () => {
    const forged: DomainProof = {
      ...base,
      expiresAt: issued + MAX_VALIDITY_SECONDS + 1,
      signature: new Uint8Array(64),
    };
    expect(() => verifyProof(forged, issued)).toThrow(/exceeds the .* maximum/);
    expect(isValidProof(forged, issued)).toBe(false);
  });

  it('agrees with the Go MaxValidity constant', () => {
    expect(MAX_VALIDITY_SECONDS).toBe(365 * 24 * 60 * 60);
  });

  it('still permits expires=0, which is discouraged not forbidden', () => {
    expect(() => canonicalStatement({ ...base, expiresAt: 0 })).not.toThrow();
  });
});

// THE LIVE PRODUCTION RECORD, signed on 2026-07-30T13:03:11Z by the real
// sdn.spaceaware.io node key during the Seal-Council-concurred one-shot run.
//
// This is the strongest regression test in the suite: it is not synthetic. If any
// future change to the canonical statement, the tag grammar, the base64 handling
// or the Ed25519 path breaks this, the proof PUBLISHED IN DNS stops verifying and
// every provider trusting sdn.spaceaware.io silently loses that trust. Treat a
// failure here as a production incident, not a test to update.
const LIVE_PRODUCTION_RECORD =
  'v=SDN1; p=DYDh_V-aTjTf3zag4VK9maZc__i8xsqydXtISuRC_Iw; ' +
  'id=16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45; ' +
  'ts=1785416591; xp=1816952591; ' +
  'sig=bK3H0_h-7rSirzzXlS7_Wu2Pqlqj4y8ZP9jO2q9jjHfJksOyoYSrRgszvgYYNt1KnoKOOdRPuPy4zFWPfukxBA';

describe('the live production record for sdn.spaceaware.io', () => {
  // A fixed instant inside the published validity window, so the test does not
  // start failing on 2027-07-30 for the wrong reason.
  const insideWindow = 1785416600;

  it('verifies against the real node key', () => {
    const proof = parseRecord(LIVE_DOMAIN, LIVE_PRODUCTION_RECORD);
    expect(() => verifyProof(proof, insideWindow)).not.toThrow();
    expect(keyFingerprint(proof)).toBe(`ed25519:${LIVE_PUBKEY_HEX}`);
    expect(proof.peerId).toBe(LIVE_PEER_ID);
  });

  it('fits one DNS character-string and sits exactly at the validity cap', () => {
    expect(LIVE_PRODUCTION_RECORD.length).toBe(233);
    const proof = parseRecord(LIVE_DOMAIN, LIVE_PRODUCTION_RECORD);
    expect(proof.expiresAt - proof.issuedAt).toBe(MAX_VALIDITY_SECONDS);
  });

  it('cannot be replayed at another domain', () => {
    const stolen = parseRecord('attacker.example', LIVE_PRODUCTION_RECORD);
    expect(() => verifyProof(stolen, insideWindow)).toThrow(/does not verify/);
  });

  it('is refused once expired', () => {
    const proof = parseRecord(LIVE_DOMAIN, LIVE_PRODUCTION_RECORD);
    expect(() => verifyProof(proof, proof.expiresAt)).toThrow(/expired/);
  });
});
