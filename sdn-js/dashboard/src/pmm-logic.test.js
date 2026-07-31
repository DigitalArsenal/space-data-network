/*
 * The $PMM read: where to ask, what may be believed, and how the offering is
 * sectioned. Everything here is pure — the one network shape is a stub fetch.
 *
 * Fixtures are the stack's design fixtures, vendored under dashboard/fixtures
 * (see that directory's README for provenance and the no-demo-keys rule).
 */
import { describe, it, expect } from 'vitest';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { webcrypto } from 'node:crypto';
import {
  MANIFEST_PATH,
  DATA_FAMILIES,
  UNPLACED_FAMILIES,
  GROUPING_UNSIGNED_NOTE,
  VERIFY_DETAIL,
  accessLabel,
  buildCanonicalStatement,
  compareUtf8,
  entryStateLabel,
  familyBucket,
  familyLabel,
  fetchManifest,
  hostFromMultiaddr,
  hostedState,
  manifestHosts,
  manifestOrigin,
  manifestURL,
  normalizeManifest,
  offeringSections,
  toBytes,
  verifyManifest,
} from './pmm.js';

const FIXTURES = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../fixtures');
const readFixture = (name) => fs.readFileSync(path.join(FIXTURES, name), 'utf8');
const manifestFixture = () => JSON.parse(readFixture('pmm-modules.json'));
const statementFixture = () => readFixture('pmm-modules.statement.txt');

describe('where to ask (a peer with no DNS name has nowhere to be asked)', () => {
  it('takes a hostname only from a dns multiaddr', () => {
    expect(hostFromMultiaddr('/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/16Uiu2HA')).toBe('sdn.spaceaware.io');
    expect(hostFromMultiaddr('/dns6/node.example.org/tcp/443/wss')).toBe('node.example.org');
    expect(hostFromMultiaddr('/dnsaddr/bootstrap.example.org')).toBe('bootstrap.example.org');
  });

  it('never promotes an IP literal to an origin', () => {
    // The NAT-hidden peer's shape: real addresses, no name TLS or a signed
    // PROVIDER_DOMAIN could ever match.
    expect(hostFromMultiaddr('/ip4/198.51.100.20/tcp/4001')).toBe('');
    expect(hostFromMultiaddr('/ip4/127.0.0.1/udp/4001/quic-v1')).toBe('');
    expect(hostFromMultiaddr('')).toBe('');
    expect(manifestOrigin({ addrs: ['/ip4/198.51.100.20/tcp/4001'] })).toBe('');
    expect(manifestURL({ addrs: ['/ip4/198.51.100.20/tcp/4001'] })).toBe('');
  });

  it('prefers what the node observed, then what the peer published, without duplicates', () => {
    const hosts = manifestHosts({
      addrs: ['/ip4/10.0.0.2/tcp/4001', '/dns4/a.example.org/tcp/443/wss'],
      vcardAddrs: ['/dns4/a.example.org/tcp/443/wss', '/dns4/b.example.org/tcp/443/wss'],
    });
    expect(hosts).toEqual(['a.example.org', 'b.example.org']);
    expect(manifestOrigin({ addrs: ['/dns4/a.example.org/tcp/443/wss'] })).toBe('https://a.example.org');
    expect(manifestURL({ addrs: ['/dns4/a.example.org/tcp/443/wss'] })).toBe(
      `https://a.example.org${MANIFEST_PATH}`
    );
  });
});

describe('the canonical statement is rebuilt, never trusted', () => {
  it('rebuilds the fixture manifest line-for-line, in the standard’s order', () => {
    // The fixture ships BOTH the record and a statement for it, so this is the
    // test that catches any divergence from the node's own CanonicalStatement
    // (internal/pmm/manifest.go:253-274).
    //
    // THE FIXTURE'S OWN COPY IS IN ARRAY ORDER, not bytewise MODULE_ID order —
    // measured 2026-07-31, and it is why this test sorts the fixture's module
    // lines before comparing rather than comparing the file verbatim. The
    // standard requires the sort, the Go signer performs it (SortModules before
    // CanonicalStatement), and the LIVE manifest on sdn.spaceaware.io is sorted
    // (`atmosphere-model`, `cislunar-propagator`, `com.dig…`). So: every FIELD
    // of every line must match the fixture exactly, and the ORDER must be the
    // standard's. A rebuild that matched the fixture verbatim would be the bug.
    const lines = statementFixture().replace(/\n$/, '').split('\n');
    const header = lines.slice(0, 6);
    const modules = lines.slice(6).sort(compareUtf8);
    expect(buildCanonicalStatement(manifestFixture())).toBe(`${[...header, ...modules].join('\n')}\n`);
  });

  it('sorts module lines bytewise, not by UTF-16 code unit', () => {
    // Above the BMP the two orders disagree; a single astral MODULE_ID would
    // reorder the statement and break every signature with no diagnosable cause.
    expect(compareUtf8('\u{1F600}', '�')).toBeGreaterThan(0);
    expect('\u{1F600}' < '�').toBe(true);
    const out = buildCanonicalStatement({
      PROVIDER_DOMAIN: 'x',
      EPOCH: 1,
      MODULES: [{ MODULE_ID: 'b' }, { MODULE_ID: 'a' }],
    });
    expect(out.indexOf('module:a')).toBeLessThan(out.indexOf('module:b'));
  });

  it('emits every header line and terminates every line, including the last', () => {
    const out = buildCanonicalStatement({ PROVIDER_DOMAIN: '', EPOCH: 0, MODULES: [] });
    expect(out).toBe('SDN-MODULE-MANIFEST-V1\ndomain:\npeer:\nkey:\nepoch:0\nexpires:\n');
  });

  it('renders DEFAULT_ENABLED as 1/0 and enums as their symbol names', () => {
    const line = buildCanonicalStatement({
      PROVIDER_DOMAIN: 'x',
      EPOCH: 2,
      MODULES: [
        {
          MODULE_ID: 'm',
          VERSION: '1.0.0',
          CONTENT_HASH: 'abc',
          TRUST_TIER: 'CORE',
          ACCESS_POLICY: 'ANONYMOUS',
          DEFAULT_ENABLED: true,
          ENTRY_STATE: 'ACTIVE',
        },
      ],
    })
      .split('\n')
      .find((l) => l.startsWith('module:'));
    expect(line).toBe('module:m 1.0.0 abc CORE ANONYMOUS 1 ACTIVE');
  });
});

describe('hex and bytes', () => {
  it('accepts hex, byte arrays and Uint8Array; refuses everything else', () => {
    expect(Array.from(toBytes('00ff'))).toEqual([0, 255]);
    expect(Array.from(toBytes([1, 2]))).toEqual([1, 2]);
    expect(toBytes('0f0')).toBeNull();
    expect(toBytes('zz')).toBeNull();
    expect(toBytes([300])).toBeNull();
    expect(toBytes(null)).toBeNull();
  });
});

describe('verification fails closed, in the order the standard mandates', () => {
  const SERVED = 'https://sdn.example.org/.well-known/sdn/modules.pmm';
  const future = new Date('2026-08-01T00:00:00Z');

  it('refuses a manifest with no trust anchor', async () => {
    const v = await verifyManifest({ PROVIDER_DOMAIN: 'sdn.example.org' }, { servedFrom: SERVED });
    expect(v.ok).toBe(false);
    expect(v.reason).toBe('NO_TRUST_ANCHOR');
    expect(v.detail).toBe(VERIFY_DETAIL.NO_TRUST_ANCHOR);
  });

  it('refuses a manifest served by a domain it does not name', async () => {
    const v = await verifyManifest(manifestFixture(), {
      servedFrom: 'https://someone-else.example.net/.well-known/sdn/modules.pmm',
      now: future,
    });
    expect(v.reason).toBe('DOMAIN_MISMATCH');
  });

  it('refuses when the carried statement disagrees with the record', async () => {
    const m = manifestFixture();
    m.MODULES[0].VERSION = '9.9.9'; // the record now says something its statement does not
    const v = await verifyManifest(m, { servedFrom: SERVED, now: future });
    expect(v.reason).toBe('STATEMENT_MISMATCH');
  });

  it('refuses a statement whose module lines are out of the standard’s order', async () => {
    // The fixture's own SIGNED_STATEMENT is in array order (see the rebuild test
    // above), which is exactly the shape a verifier must refuse: the carried
    // copy is a claim, and the record is the evidence.
    const v = await verifyManifest(manifestFixture(), { servedFrom: SERVED, now: future });
    expect(v.reason).toBe('STATEMENT_MISMATCH');
  });

  it('refuses a replayed older epoch BEFORE checking the signature', async () => {
    const m = manifestFixture();
    m.SIGNED_STATEMENT = ''; // a valid signature would not save it
    const v = await verifyManifest(m, {
      servedFrom: SERVED,
      now: future,
      highestVerifiedEpoch: m.EPOCH + 1,
    });
    expect(v.reason).toBe('EPOCH_ROLLBACK');
  });

  it('refuses an expired manifest', async () => {
    const m = manifestFixture();
    m.SIGNED_STATEMENT = '';
    const v = await verifyManifest(m, {
      servedFrom: SERVED,
      now: new Date('2027-01-01T00:00:00Z'),
    });
    expect(v.reason).toBe('MANIFEST_EXPIRED');
  });

  it('refuses an algorithm it does not implement rather than passing it', async () => {
    // The fixture is signed secp256k1-sha256 — this dashboard verifies ed25519.
    const m = manifestFixture();
    m.SIGNED_STATEMENT = '';
    const v = await verifyManifest(m, { servedFrom: SERVED, now: future });
    expect(v.reason).toBe('SIGNATURE_UNSUPPORTED');
  });

  it('refuses when WebCrypto is unavailable', async () => {
    const m = manifestFixture();
    m.SIGNED_STATEMENT = '';
    m.TRUST.SIGNATURE_ALGORITHM = 'ed25519';
    m.SIGNATURE = '00'.repeat(64);
    const v = await verifyManifest(m, { servedFrom: SERVED, now: future, subtle: null });
    expect(v.reason).toBe('SIGNATURE_UNSUPPORTED');
  });

  it('refuses a key or signature of the wrong length', async () => {
    const m = manifestFixture();
    m.SIGNED_STATEMENT = '';
    m.TRUST.SIGNATURE_ALGORITHM = 'ed25519';
    m.SIGNATURE = 'aabb';
    expect((await verifyManifest(m, { servedFrom: SERVED, now: future })).reason).toBe('SIGNATURE_INVALID');
  });

  it('accepts a real ed25519 signature over the REBUILT bytes', async () => {
    const { publicKey, privateKey } = await webcrypto.subtle.generateKey({ name: 'Ed25519' }, true, [
      'sign',
      'verify',
    ]);
    const raw = new Uint8Array(await webcrypto.subtle.exportKey('raw', publicKey));
    const hex = [...raw].map((b) => b.toString(16).padStart(2, '0')).join('');
    const m = normalizeManifest({
      PROVIDER_DOMAIN: 'sdn.example.org',
      EPOCH: 7,
      EXPIRES_AT: '2026-12-31T00:00:00.000Z',
      TRUST: { PROVIDER_DOMAIN: 'sdn.example.org', NODE_PEER_ID: '16Uiu2HAmEXAMPLE', SIGNING_PUBLIC_KEY: hex },
      MODULES: [
        {
          MODULE_ID: 'org.example.a',
          VERSION: '1.0.0',
          CONTENT_HASH: 'aa',
          TRUST_TIER: 'CORE',
          ACCESS_POLICY: 'ANONYMOUS',
          DEFAULT_ENABLED: true,
          ENTRY_STATE: 'ACTIVE',
          PLUGIN_TYPE: 'DataSource',
        },
      ],
    });
    const statement = buildCanonicalStatement(m);
    const sig = new Uint8Array(
      await webcrypto.subtle.sign({ name: 'Ed25519' }, privateKey, new TextEncoder().encode(statement))
    );
    m.SIGNED_STATEMENT = statement;
    m.SIGNATURE = sig;
    const ok = await verifyManifest(m, { servedFrom: SERVED, now: future, subtle: webcrypto.subtle });
    expect(ok.ok).toBe(true);

    // …and one flipped byte in a module the statement covers is caught.
    const tampered = { ...m, MODULES: [{ ...m.MODULES[0], CONTENT_HASH: 'ab' }], SIGNED_STATEMENT: '' };
    const bad = await verifyManifest(tampered, { servedFrom: SERVED, now: future, subtle: webcrypto.subtle });
    expect(bad.reason).toBe('SIGNATURE_INVALID');
  });
});

describe('the offering is sectioned by PLUGIN_TYPE and by nothing else', () => {
  it('places the record families Themis ruled on, and leaves Flow unplaced', () => {
    expect(DATA_FAMILIES).toEqual(['DataSource', 'Parser', 'Validator', 'Interpolator', 'Exporter']);
    expect(UNPLACED_FAMILIES).toEqual(['Flow']);
    for (const f of DATA_FAMILIES) expect(familyBucket(f)).toBe('data');
    for (const f of ['Propagator', 'Analysis', 'Comms', 'Sensor', 'Foundation', 'Infrastructure']) {
      expect(familyBucket(f)).toBe('functionality');
    }
    expect(familyBucket('Flow')).toBe('unplaced');
  });

  it('never files an unstated family under a family', () => {
    expect(familyBucket('')).toBe('unstated');
    expect(familyBucket('Unspecified')).toBe('unstated');
    expect(familyLabel('')).toBe('FAMILY NOT STATED');
    expect(familyLabel('Unspecified')).toBe('FAMILY NOT STATED');
    // A family newer than this page is shown VERBATIM, not folded into "not
    // stated": the enum is append-only and a newer node is not a silent node.
    expect(familyLabel('SomethingNewer')).toBe('SOMETHINGNEWER');
    expect(familyLabel('DataSource')).toBe('DATA SOURCE');
  });

  it('sections the fixture manifest into DATA, FUNCTIONALITY and COMPOSED FLOWS', () => {
    const sections = offeringSections(manifestFixture().MODULES);
    expect(sections.map((s) => s.id)).toEqual(['data', 'functionality', 'unplaced']);
    const data = sections.find((s) => s.id === 'data');
    expect(data.families.map((f) => f.label)).toEqual(['DATA SOURCE', 'PARSER']);
    expect(data.count).toBe(2);
    expect(sections.find((s) => s.id === 'unplaced').label).toBe('COMPOSED FLOWS');
    // Every module in the fixture is placed exactly once.
    expect(sections.reduce((n, s) => n + s.count, 0)).toBe(manifestFixture().MODULES.length);
  });

  it('gives an entry with no stated family its own section, ungrouped', () => {
    const sections = offeringSections([
      { MODULE_ID: 'a', PLUGIN_TYPE: 'Propagator' },
      { MODULE_ID: 'b' },
      { MODULE_ID: 'c', PLUGIN_TYPE: 'Unspecified' },
    ]);
    const unstated = sections.find((s) => s.id === 'unstated');
    expect(unstated.label).toBe('FAMILY NOT STATED');
    expect(unstated.count).toBe(2);
    expect(unstated.families).toHaveLength(1);
  });

  it('omits a bucket nobody is in', () => {
    expect(offeringSections([{ MODULE_ID: 'a', PLUGIN_TYPE: 'Propagator' }]).map((s) => s.id)).toEqual([
      'functionality',
    ]);
    expect(offeringSections([])).toEqual([]);
  });

  it('says out loud that the family headings are not signed', () => {
    // Themis: PLUGIN_TYPE is absent from the canonical statement. The page must
    // not let one VERIFIED badge cover a field the signature never touched.
    expect(buildCanonicalStatement(manifestFixture())).not.toContain('DataSource');
    expect(GROUPING_UNSIGNED_NOTE).toMatch(/FAMILY headings are not/);
  });

  it('renders lifecycle and access as words, and ACTIVE as no badge at all', () => {
    expect(entryStateLabel('ACTIVE')).toBe('');
    expect(entryStateLabel('REVOKED')).toBe('REVOKED');
    expect(entryStateLabel('')).toBe('');
    expect(accessLabel('ANONYMOUS')).toBe('ANONYMOUS');
    expect(accessLabel('AUTHENTICATED')).toBe('SIGN-IN REQUIRED');
    expect(accessLabel('ENTITLED')).toBe('LICENCE REQUIRED');
  });
});

describe('the fetch stays a simple cross-origin GET', () => {
  it('sends Accept only — one custom header would trip a preflight the node 405s', async () => {
    let seen = null;
    const stub = async (url, init) => {
      seen = { url, init };
      return { ok: true, status: 200, json: async () => manifestFixture() };
    };
    const res = await fetchManifest('https://sdn.example.org', { fetchImpl: stub });
    expect(res.ok).toBe(true);
    expect(seen.url).toBe(`https://sdn.example.org${MANIFEST_PATH}`);
    expect(Object.keys(seen.init.headers)).toEqual(['Accept']);
    expect(seen.init.headers.Accept).toBe('application/json');
    expect(seen.init.credentials).toBe('omit');
  });

  it('turns a refusal, a bad status and a bad body into states, never exceptions', async () => {
    const threw = await fetchManifest('https://x.example.org', {
      fetchImpl: async () => {
        throw new TypeError('Failed to fetch');
      },
    });
    expect(threw).toMatchObject({ ok: false, reason: 'unreachable' });

    const notFound = await fetchManifest('https://x.example.org', {
      fetchImpl: async () => ({ ok: false, status: 404 }),
    });
    expect(notFound).toMatchObject({ ok: false, reason: 'not-served', status: 404 });

    const junk = await fetchManifest('https://x.example.org', {
      fetchImpl: async () => ({
        ok: true,
        status: 200,
        json: async () => {
          throw new SyntaxError('unexpected token');
        },
      }),
    });
    expect(junk).toMatchObject({ ok: false, reason: 'not-a-manifest' });

    expect(await fetchManifest('')).toMatchObject({ ok: false, reason: 'no-origin' });
  });
});

describe('every read is a state with words on it', () => {
  it('a peer with no https origin is not an error', () => {
    const s = hostedState({ phase: 'done', origin: '' });
    expect(s.id).toBe('no-origin');
    expect(s.detail).toBe('This node has not reached an https origin for this peer.');
    // NOTHING may claim NAT, a firewall or reachability — this page measures none
    // of them (the standing PeerEditModal rule).
    expect(`${s.title} ${s.detail}`).not.toMatch(/NAT|firewall|unreachable peer/i);
  });

  it('never renders a bare transport code as the answer', () => {
    const s = hostedState({ phase: 'done', origin: 'https://x.example.org', fetchReason: 'not-served', status: 521 });
    expect(s.detail).toBe('A server answered, but did not serve a $PMM manifest.');
    expect(s.detail).not.toMatch(/521/);
    // The code survives — behind the DETAIL affordance, as evidence.
    expect(s.code).toBe('HTTP 521');
  });

  it('an unverified manifest renders its reason and NOTHING else', () => {
    const s = hostedState({
      phase: 'done',
      origin: 'https://x.example.org',
      verifyReason: 'SIGNATURE_INVALID',
      count: 67,
    });
    expect(s.id).toBe('unverified');
    expect(s.detail).toBe(VERIFY_DETAIL.SIGNATURE_INVALID);
    // The count is deliberately NOT in the sentence: "67 modules we cannot
    // prove are theirs" is still a claim about their offering.
    expect(s.detail).not.toMatch(/67/);
  });

  it('separates "hosts none" from "could not be read"', () => {
    expect(hostedState({ phase: 'done', origin: 'https://x.example.org', count: 0 }).id).toBe('empty');
    expect(hostedState({ phase: 'done', origin: 'https://x.example.org', count: 3 }).id).toBe('ok');
    expect(hostedState({ phase: 'reading', origin: 'https://x.example.org' }).id).toBe('reading');
    expect(hostedState({ phase: 'idle', origin: 'https://x.example.org' }).id).toBe('idle');
  });
});
